// Command jobctl runs the personal-ops daemon and talks to it.
//
// One binary. `jobctl daemon` is the long-lived process that schedules jobs and
// serves the dashboard; the other subcommands are thin clients over its local API, so
// the CLI and the dashboard's buttons drive exactly the same code path and cannot
// drift apart. `jobctl run --local` bypasses the daemon for debugging.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"log"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/runner"
)

const (
	notifierBundleID = "dev.personal-ops.notifier"
	// notifierAppName is what appears in System Settings > Notifications, so it is the
	// name for a human rather than an identifier. Changing it creates a second row and
	// abandons whatever notification style was set on the first.
	notifierAppName  = "Personal Ops"
	notifierExecName = "personal-ops"
	launchdLabel     = "dev.personal-ops.daemon"
	defaultPort      = 7777
)

// Config is where everything lives.
type Config struct {
	OpsRoot       string // the engine checkout: binaries and lib/
	ArtifactsRoot string // ~/ops
	JobsDir       string // the user's jobs, ~/personal-ops unless OPS_JOBS says otherwise
	Port          int
}

func (c Config) JobsRoot() string     { return c.JobsDir }
func (c Config) StatePath() string    { return filepath.Join(c.ArtifactsRoot, "daemon-state.json") }
func (c Config) DashboardURL() string { return fmt.Sprintf("http://127.0.0.1:%d", c.Port) }

func loadConfig() Config {
	home, _ := os.UserHomeDir()

	opsRoot := os.Getenv("OPS_ROOT")
	if opsRoot == "" {
		// The binary normally lives in <repo>/bin, so the repository is its parent.
		if exe, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				opsRoot = filepath.Dir(filepath.Dir(resolved))
			}
		}
	}
	if opsRoot == "" {
		opsRoot = filepath.Join(home, ".personal-ops-kit")
	}

	artifacts := os.Getenv("OPS_ARTIFACTS")
	if artifacts == "" {
		artifacts = filepath.Join(home, "ops")
	}
	jobs := os.Getenv("OPS_JOBS")
	if jobs == "" {
		jobs = filepath.Join(home, "personal-ops")
	}
	return Config{OpsRoot: opsRoot, ArtifactsRoot: artifacts, JobsDir: jobs, Port: defaultPort}
}

func main() {
	log.SetFlags(log.Ltime)
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	cfg := loadConfig()
	command, rest := args[0], args[1:]

	var err error
	switch command {
	case "daemon":
		err = cmdDaemon(cfg)
	case "list":
		err = cmdList(cfg)
	case "run":
		err = cmdRun(cfg, rest)
	case "logs":
		err = cmdLogs(cfg, rest)
	case "pause":
		err = cmdSetPaused(cfg, rest, true)
	case "resume":
		err = cmdSetPaused(cfg, rest, false)
	case "entries":
		err = cmdEntries(cfg, rest)
	case "open":
		err = cmdOpen(cfg)
	case "reload":
		err = cmdReload()
	case "update":
		err = cmdUpdate(cfg)
	case "install-daemon":
		err = cmdInstallDaemon(cfg)
	case "uninstall-daemon":
		err = cmdUninstallDaemon()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", command)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `jobctl -- scheduled personal jobs

  jobctl daemon              run the scheduler and dashboard (launchd starts this)
  jobctl list                jobs, schedules, last and next run
  jobctl run <job> [--local] [--force]
                             trigger a run; --local bypasses the daemon
  jobctl logs <job>          tail the most recent run's output
  jobctl pause <job>         stop scheduling a job
  jobctl resume <job>        schedule it again
  jobctl entries append|fold shared cross-job state (see templates/CLAUDE.md)
  jobctl open                open the dashboard in a browser
  jobctl reload              restart the daemon (after a rebuild or a job.env change)
  jobctl update              pull the latest engine and reinstall (your jobs are kept)
  jobctl install-daemon      install and load the launchd agent
  jobctl uninstall-daemon    unload and remove it
`)
}

func cmdDaemon(cfg Config) error {
	d, err := NewDaemon(cfg)
	if err != nil {
		return err
	}
	srv, err := NewServer(d)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Loopback only. Never 0.0.0.0: this serves a Run button.
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("dashboard on %s", cfg.DashboardURL())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server stopped: %v", err)
			stop()
		}
	}()

	d.Run(ctx)

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdown)
}

// daemonGet talks to a running daemon, or reports plainly that there is not one.
func daemonGet(cfg Config, path string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.DashboardURL() + path)
	if err != nil {
		return "", fmt.Errorf("the daemon is not responding on %s (start it with `jobctl install-daemon`)", cfg.DashboardURL())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("daemon returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func daemonPost(cfg Config, path string) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
		// The API answers a browser with a redirect; a CLI just wants the status.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodPost, cfg.DashboardURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", cfg.DashboardURL())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the daemon is not responding on %s", cfg.DashboardURL())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func cmdList(cfg Config) error {
	out, err := daemonGet(cfg, "/api/jobs")
	if err == nil {
		fmt.Print(out)
		return nil
	}
	// Fall back to reading from disk, so `list` still answers when the daemon is down
	// -- which is exactly when you most want to look.
	fmt.Fprintln(os.Stderr, "note:", err)
	jobs, problems := job.LoadAll(cfg.JobsRoot(), cfg.ArtifactsRoot)
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "job not loaded:", p)
	}
	for _, j := range jobs {
		last := "never"
		if runs, _ := ledger.Load(j.LedgerPath()); len(runs) > 0 {
			last = fmt.Sprintf("%s %s", runs[0].StartedAt.Format("2 Jan 15:04"), runs[0].Outcome)
		}
		fmt.Printf("%-18s %-14s last %s\n", j.Name, j.Schedule.String(), last)
	}
	return nil
}

func cmdRun(cfg Config, args []string) error {
	name, flags := splitArgs(args)
	if name == "" {
		return fmt.Errorf("usage: jobctl run <job> [--local] [--force]")
	}

	if !flags["local"] {
		path := "/api/run/" + name
		if flags["force"] {
			path += "?force=1"
		}
		if err := daemonPost(cfg, path); err != nil {
			return err
		}
		fmt.Printf("started %s -- follow it at %s/jobs/%s\n", name, cfg.DashboardURL(), name)
		return nil
	}

	j, err := job.Load(filepath.Join(cfg.JobsRoot(), name), cfg.ArtifactsRoot)
	if err != nil {
		return err
	}
	if flags["force"] {
		j.Gated = false
	}
	r := &runner.Runner{OpsRoot: cfg.OpsRoot, ArtifactsRoot: cfg.ArtifactsRoot}
	res, err := r.Run(context.Background(), j, runner.TriggerManual)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s -- %s\n", name, res.Record.Outcome, res.Record.Headline)
	if res.Record.Detail != "" {
		fmt.Println(res.Record.Detail)
	}
	fmt.Println("log:", res.LogPath)
	if res.Record.Outcome == ledger.Failed {
		os.Exit(1)
	}
	return nil
}

func cmdLogs(cfg Config, args []string) error {
	name, _ := splitArgs(args)
	if name == "" {
		return fmt.Errorf("usage: jobctl logs <job>")
	}
	j, err := job.Load(filepath.Join(cfg.JobsRoot(), name), cfg.ArtifactsRoot)
	if err != nil {
		return err
	}
	runs, err := ledger.Load(j.LedgerPath())
	if err != nil || len(runs) == 0 {
		return fmt.Errorf("%s has no recorded runs yet", name)
	}
	path := filepath.Join(j.LogDir(), runs[0].LogFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no log for the most recent run: %w", err)
	}
	fmt.Print(string(body))
	return nil
}

func cmdSetPaused(cfg Config, args []string, paused bool) error {
	name, _ := splitArgs(args)
	if name == "" {
		return fmt.Errorf("usage: jobctl pause|resume <job>")
	}
	verb := "resume"
	if paused {
		verb = "pause"
	}
	if err := daemonPost(cfg, "/api/"+verb+"/"+name); err != nil {
		return err
	}
	fmt.Printf("%s %sd\n", name, verb)
	return nil
}

// cmdUpdate pulls the engine and re-runs its installer, which rebuilds, refreshes
// the service and leaves the user's jobs alone.
func cmdUpdate(cfg Config) error {
	for _, args := range [][]string{
		{"git", "-C", cfg.OpsRoot, "pull", "--ff-only"},
		{"/bin/bash", filepath.Join(cfg.OpsRoot, "install.sh")},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func cmdOpen(cfg Config) error {
	return exec.Command("open", cfg.DashboardURL()).Run()
}

func splitArgs(args []string) (name string, flags map[string]bool) {
	flags = map[string]bool{}
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			flags[strings.TrimPrefix(a, "--")] = true
			continue
		}
		if name == "" {
			name = a
		}
	}
	return name, flags
}
