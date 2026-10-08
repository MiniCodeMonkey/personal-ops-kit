// Package runner executes a job and records what happened.
//
// One execution path serves the scheduler, the CLI and the dashboard's Run button, so
// a manually triggered run cannot behave differently from a scheduled one.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
)

// Trigger records why a run happened, so the dashboard can distinguish a scheduled
// run from one that was caught up after sleep or started by hand.
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerCatchup  Trigger = "catchup"
	TriggerManual   Trigger = "manual"
)

// Runner executes jobs.
type Runner struct {
	// OpsRoot is the repository root, used to find lib/gate.sh.
	OpsRoot string
	// ArtifactsRoot is where job output lives, normally ~/ops.
	ArtifactsRoot string
	// Notifier is optional; a nil Notifier means no notifications, which is what a
	// dry run or a test wants.
	Notifier Notifier
	// Now is injectable for tests.
	Now func() time.Time
}

// Notifier is the subset of internal/notify the runner needs, kept as an interface
// so a run can be tested without posting to Notification Centre.
type Notifier interface {
	Post(ctx context.Context, job, outcome, headline, runID string) error
}

// Result is what one execution produced.
type Result struct {
	Record  ledger.Record
	LogPath string
}

// gateSkip is the exit status gate.sh uses for "affordable, but not now".
const gateSkip = 10

// Run executes a job and appends exactly one ledger record, whatever happens.
//
// The ledger write is the point: a job that crashes, times out or is gated off must
// leave a trace, because a run that leaves nothing behind is indistinguishable from a
// run that never happened.
func (r *Runner) Run(ctx context.Context, j *job.Job, trigger Trigger) (Result, error) {
	now := r.now()
	runID := now.Format("2006-01-02T15-04-05")

	rec := ledger.Record{
		Job:       j.Name,
		RunID:     runID,
		StartedAt: now,
		Trigger:   string(trigger),
	}

	for _, dir := range []string{j.ArtifactDir, j.LogDir(), j.StateDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	logPath := filepath.Join(j.LogDir(), runID+".log")
	rec.LogFile = filepath.Base(logPath)

	// A gated job asks the quota gate first. Being gated is an outcome, not a
	// failure: the job deliberately did not run.
	if j.Gated {
		reason, allowed, err := r.gate(ctx, j)
		if err != nil {
			rec.Outcome = ledger.Failed
			rec.Headline = "Could not evaluate the quota gate"
			rec.Detail = err.Error()
			rec.FinishedAt = r.now()
			return r.finish(ctx, j, rec, logPath)
		}
		if !allowed {
			rec.Outcome = ledger.Gated
			rec.Headline = "Skipped by the quota gate"
			rec.Detail = reason
			rec.FinishedAt = r.now()
			_ = os.WriteFile(logPath, []byte(reason+"\n"), 0o644)
			return r.finish(ctx, j, rec, logPath)
		}
	}

	resultPath := filepath.Join(j.StateDir(), "last-result.env")
	_ = os.Remove(resultPath) // never inherit the previous run's verdict

	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{}, fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	runCtx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/bash", j.RunScript())
	cmd.Dir = j.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = r.env(j, runID, resultPath, logPath)

	// Own process group, so a timeout kills the whole tree. Without this a killed
	// run.sh leaves `claude` and its children running, holding the quota the gate
	// exists to protect.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	runErr := cmd.Run()
	rec.FinishedAt = r.now()

	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	r.applyOutcome(&rec, resultPath, runErr, timedOut, j.Timeout)

	return r.finish(ctx, j, rec, logPath)
}

// applyOutcome decides how the run ended.
//
// The job declares its own verdict by writing a result file; the runner only falls
// back to exit codes when it does not. What counts as success is job-specific -- zero
// new auction lots is a perfectly good day, while zero auctions found is a broken
// scraper -- and only the job knows the difference.
func (r *Runner) applyOutcome(rec *ledger.Record, resultPath string, runErr error, timedOut bool, timeout time.Duration) {
	if timedOut {
		rec.Outcome = ledger.Failed
		rec.Headline = "Timed out"
		rec.Detail = fmt.Sprintf("Killed after %s.", timeout)
		return
	}

	declared, err := readResult(resultPath)
	if err == nil && declared.Outcome != "" {
		rec.Outcome = declared.Outcome
		rec.Headline = declared.Headline
		rec.Detail = declared.Detail
		rec.Artifact = declared.Artifact
		rec.Lane = declared.Lane
		// A job that declares success while exiting non-zero is not to be believed.
		if runErr != nil && rec.Outcome != ledger.Failed {
			rec.Outcome = ledger.Failed
			rec.Detail = strings.TrimSpace(fmt.Sprintf(
				"Reported %q but exited with an error: %v. %s", declared.Outcome, runErr, declared.Detail))
		}
		return
	}

	if runErr != nil {
		rec.Outcome = ledger.Failed
		rec.Headline = "Run failed"
		rec.Detail = runErr.Error()
		return
	}

	// Exited cleanly and said nothing. Treat as success, but say so plainly rather
	// than inventing a headline the job never produced.
	rec.Outcome = ledger.OK
	if rec.Headline == "" {
		rec.Headline = "Finished"
	}
}

func (r *Runner) finish(ctx context.Context, j *job.Job, rec ledger.Record, logPath string) (Result, error) {
	if err := ledger.AppendRun(j.LedgerPath(), rec); err != nil {
		return Result{Record: rec, LogPath: logPath}, fmt.Errorf("append ledger: %w", err)
	}
	if r.Notifier != nil {
		// A notification that fails to send must not turn a good run into a failed
		// one; the result is already safely in the ledger and the dashboard.
		_ = r.Notifier.Post(ctx, j.Name, string(rec.Outcome), rec.Headline, rec.RunID)
	}
	return Result{Record: rec, LogPath: logPath}, nil
}

// gate reads the quota of the profile the job will spend, at the job's threshold.
func (r *Runner) gate(ctx context.Context, j *job.Job) (reason string, allowed bool, err error) {
	gate := filepath.Join(r.OpsRoot, "lib", "gate.sh")
	cmd := exec.CommandContext(ctx, "/bin/bash", gate)
	cmd.Env = os.Environ()
	if j.ClaudeProfileDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_PROFILE_DIR="+j.ClaudeProfileDir)
	}
	if j.GateThreshold != "" {
		cmd.Env = append(cmd.Env, "WEEKLY_THRESHOLD="+j.GateThreshold)
	}
	out, err := cmd.CombinedOutput()
	reason = strings.TrimSpace(string(out))

	if err == nil {
		return reason, true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == gateSkip {
		return reason, false, nil
	}
	return reason, false, fmt.Errorf("gate.sh: %w: %s", err, reason)
}

// env builds the environment a run.sh sees. Everything a job needs is passed
// explicitly rather than left to be derived, so a job never has to guess where its
// own output belongs.
func (r *Runner) env(j *job.Job, runID, resultPath, logPath string) []string {
	env := append(os.Environ(),
		"OPS_ROOT="+r.OpsRoot,
		"OPS_JOB="+j.Name,
		"OPS_RUN_ID="+runID,
		"OPS_ARTIFACT_DIR="+j.ArtifactDir,
		"OPS_STATE_DIR="+j.StateDir(),
		"OPS_LOG_FILE="+logPath,
		"OPS_RESULT_FILE="+resultPath,
		"OPS_JOB_DIR="+j.Dir,
		"OPS_JOBS="+filepath.Dir(j.Dir),
		"OPS_ARTIFACTS="+r.ArtifactsRoot,
	)
	if j.Model != "" {
		env = append(env, "OPS_MODEL="+j.Model)
	}
	if j.ClaudeProfileDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+j.ClaudeProfileDir)
	}
	// launchd starts the daemon with only the PATH its plist names, so the usual
	// install locations are appended to whatever that is.
	env = append(env, "PATH="+opsPath())
	return env
}

func opsPath() string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}
	if path := os.Getenv("PATH"); path != "" {
		dirs = append([]string{path}, dirs...)
	}
	return strings.Join(dirs, ":")
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// declaredResult is what a job writes to OPS_RESULT_FILE.
type declaredResult struct {
	Outcome  ledger.Outcome
	Headline string
	Detail   string
	Artifact string
	// Lane is optional and job-specific: the research job rotates between lanes and
	// its picker scores the next night from which lane last appeared in the ledger.
	// A run recorded without it silently resets that lane's clock.
	Lane string
}

// readResult parses the KEY=value file a job writes to declare its own verdict:
//
//	outcome=no-change
//	headline=No new lots today
//
// KEY=value because a bash job has to write it in one heredoc without reaching for a
// JSON encoder.
func readResult(path string) (declaredResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return declaredResult{}, err
	}
	defer f.Close()

	var out declaredResult
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "outcome":
			if o, ok := validOutcome(value); ok {
				out.Outcome = o
			}
		case "headline":
			out.Headline = value
		case "detail":
			out.Detail = value
		case "artifact":
			out.Artifact = value
		case "lane":
			out.Lane = value
		}
	}
	return out, scanner.Err()
}

func validOutcome(v string) (ledger.Outcome, bool) {
	switch ledger.Outcome(strings.ToLower(v)) {
	case ledger.OK:
		return ledger.OK, true
	case ledger.Warning:
		return ledger.Warning, true
	case ledger.NoChange:
		return ledger.NoChange, true
	case ledger.Gated:
		return ledger.Gated, true
	case ledger.Failed:
		return ledger.Failed, true
	}
	return "", false
}
