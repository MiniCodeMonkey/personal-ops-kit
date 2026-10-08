package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/notify"
)

// The launchd agent. One agent for the whole platform, not one per job: the daemon
// owns scheduling now, so launchd's only responsibility is keeping it alive.
//
// KeepAlive restarts it on crash and RunAtLoad starts it at login. A crash-loop is
// still silent, which is why the daemon writes a heartbeat and the dashboard reports
// downtime -- launchd will happily restart something forever without telling anyone.
const agentTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>

    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>OPS_ROOT</key><string>%s</string>
        <key>OPS_ARTIFACTS</key><string>%s</string>
        <key>OPS_JOBS</key><string>%s</string>
        <!-- launchd starts with a near-empty PATH, which every job would inherit. -->
        <key>PATH</key><string>%s</string>
    </dict>

    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>

    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>

    <key>ProcessType</key><string>Background</string>
    <!-- The daemon spends its life asleep between ticks but its jobs do real network
         work, so it must not be throttled into missing them. -->
    <key>LowPriorityIO</key><false/>
</dict>
</plist>
`

// The notifier app bundle.
//
// A small Swift program (internal/notify/notifier.swift) compiled into
// ~/Applications/Personal Ops.app at install time. It is both the process that posts
// notifications and the app macOS launches when one is clicked, and it has to be both:
// since macOS 26 usernoted matches the click target against the code identity of
// whatever posted the notification, which is why the previous arrangement -- posting
// through terminal-notifier with -sender spoofing this bundle -- delivered fine and then
// dropped every click on the floor ("Failed to find appropriate application to launch").
//
// Owning the bundle also gives notifications their own row in System Settings >
// Notifications, so they can be set to Alerts without dragging every other command-line
// tool along, and an icon, without which a notification shows an empty grey tile.
//
// LSUIElement keeps it out of the Dock and the app switcher. PersonalOpsOpenURL is where
// a launch with no notification attached goes -- the dashboard's /open, which knows what
// is unread.
const notifierAppPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>%s</string>
  <key>CFBundleDisplayName</key><string>%s</string>
  <key>CFBundleIdentifier</key><string>%s</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>%s</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundleVersion</key><string>2.0</string>
  <key>CFBundleShortVersionString</key><string>2.0</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>LSUIElement</key><true/>
  <key>NSUserNotificationAlertStyle</key><string>alert</string>
  <key>PersonalOpsOpenURL</key><string>%s/open</string>
</dict>
</plist>
`

func agentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func notifierAppPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications", notifierAppName+".app")
}

// notifierExecPath is the executable the daemon runs to post and clear notifications.
func notifierExecPath() string {
	return filepath.Join(notifierAppPath(), "Contents", "MacOS", notifierExecName)
}

// installNotifierApp compiles, signs and registers the notifier bundle.
//
// Rebuilt on every install so the dashboard URL baked into it cannot drift from the
// daemon's, and so a change to notifier.swift ships with the next install like any other
// change. Compilation needs swiftc, which comes with Xcode or the Command Line Tools.
// The signature is ad-hoc: Notification Centre needs the bundle to have *an* identity,
// not a paid one. Registration is explicit because ~/Applications is not always scanned
// promptly, and an unregistered bundle means clicks land nowhere.
func installNotifierApp(cfg Config) error {
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return fmt.Errorf("swiftc not found; install the Xcode Command Line Tools (xcode-select --install)")
	}

	app := notifierAppPath()
	macOS := filepath.Join(app, "Contents", "MacOS")
	resources := filepath.Join(app, "Contents", "Resources")
	for _, dir := range []string{macOS, resources} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create app bundle: %w", err)
		}
	}

	build, err := os.MkdirTemp("", "personal-ops-notifier-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(build)

	src := filepath.Join(build, "notifier.swift")
	if err := os.WriteFile(src, notify.AppSource, 0o644); err != nil {
		return err
	}
	// Compile beside the bundle and move into place, so a failed build leaves the
	// previous executable working rather than half-written.
	exe := filepath.Join(build, notifierExecName)
	if out, err := exec.Command(swiftc, "-O", "-o", exe, src).CombinedOutput(); err != nil {
		return fmt.Errorf("compile notifier: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(exe, filepath.Join(macOS, notifierExecName)); err != nil {
		return fmt.Errorf("install notifier executable: %w", err)
	}

	if err := writeIcon(build, filepath.Join(resources, "AppIcon.icns")); err != nil {
		return fmt.Errorf("app icon: %w", err)
	}

	plist := fmt.Sprintf(notifierAppPlist,
		notifierAppName, notifierAppName, notifierBundleID, notifierExecName, cfg.DashboardURL())
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write Info.plist: %w", err)
	}

	if out, err := exec.Command("codesign", "--force", "-s", "-", app).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Touch the bundle so LaunchServices notices the Info.plist changed; without it a
	// rewritten bundle can keep serving the cached registration.
	_ = os.Chtimes(app, time.Now(), time.Now())
	_ = exec.Command(lsregisterPath, "-f", app).Run()
	return nil
}

// writeIcon turns the embedded 1024px PNG into an .icns with sips and iconutil, both of
// which ship with macOS.
func writeIcon(build, dst string) error {
	master := filepath.Join(build, "icon.png")
	if err := os.WriteFile(master, notify.AppIcon, 0o644); err != nil {
		return err
	}
	set := filepath.Join(build, "AppIcon.iconset")
	if err := os.MkdirAll(set, 0o755); err != nil {
		return err
	}
	for _, size := range []int{16, 32, 128, 256, 512} {
		for scale, suffix := range map[int]string{1: "", 2: "@2x"} {
			px := fmt.Sprint(size * scale)
			out := filepath.Join(set, fmt.Sprintf("icon_%dx%d%s.png", size, size, suffix))
			if res, err := exec.Command("sips", "-z", px, px, master, "--out", out).CombinedOutput(); err != nil {
				return fmt.Errorf("sips: %w: %s", err, strings.TrimSpace(string(res)))
			}
		}
	}
	if res, err := exec.Command("iconutil", "-c", "icns", set, "-o", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("iconutil: %w: %s", err, strings.TrimSpace(string(res)))
	}
	return nil
}

// authorizeNotifications asks macOS for permission on the app's behalf and waits for
// the answer. Done at install, while someone is at the keyboard: the first post from
// an unattended daemon would otherwise raise the prompt overnight, and until it is
// answered nothing is delivered.
func authorizeNotifications() {
	fmt.Println("\nRequesting notification permission -- allow it if macOS asks.")
	if err := exec.Command(notifierExecPath(), "authorize").Run(); err != nil {
		fmt.Printf("notifications are not permitted yet; enable them in System Settings > Notifications > %s\n", notifierAppName)
	}
}

const lsregisterPath = "/System/Library/Frameworks/CoreServices.framework/" +
	"Frameworks/LaunchServices.framework/Support/lsregister"

func cmdInstallDaemon(cfg Config) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate jobctl: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	logDir := filepath.Join(cfg.ArtifactsRoot, "daemon")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}
	// claude can live anywhere npm or an installer put it; wherever install found it is
	// where the daemon's jobs will look first.
	if claude, err := exec.LookPath("claude"); err == nil {
		dirs = append([]string{filepath.Dir(claude)}, dirs...)
	}
	path := strings.Join(dirs, ":")

	// OPS_SKIP_LAUNCHCTL=1 writes the agent file and stops there: no notifier app, no
	// launchctl. It is how install.sh's dry run checks the plist without loading it.
	skipLaunchctl := os.Getenv("OPS_SKIP_LAUNCHCTL") == "1"

	// Before the agent: a daemon posting notifications nothing can attribute or open is
	// worse than a slightly slower install. Not fatal, though: jobs still run and the
	// dashboard still works without it.
	if !skipLaunchctl && os.Getenv("OPS_NOTIFICATIONS") != "0" {
		if err := installNotifierApp(cfg); err != nil {
			fmt.Printf("warning: notifications disabled: %v\n", err)
		} else {
			authorizeNotifications()
		}
	}

	plist := fmt.Sprintf(agentTemplate, launchdLabel, exe, cfg.OpsRoot, cfg.ArtifactsRoot, cfg.JobsDir, path,
		filepath.Join(logDir, "daemon.out.log"), filepath.Join(logDir, "daemon.err.log"))

	target := agentPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write agent: %w", err)
	}
	if skipLaunchctl {
		fmt.Printf("wrote %s (OPS_SKIP_LAUNCHCTL=1: not loaded)\n", target)
		return nil
	}

	// Boot out first so install is repeatable: bootstrap fails if the label is
	// already loaded, which would make a reinstall look like a broken install.
	//
	// bootout returns before the service is actually gone, and bootstrapping into that
	// window fails with "Input/output error" (error 5) -- having already unloaded the
	// old one, which leaves no daemon at all. So wait for the unload to land, and treat
	// a failure as worth retrying rather than fatal.
	_ = exec.Command("launchctl", "bootout", guiDomain()+"/"+launchdLabel).Run()
	waitUntilUnloaded(3 * time.Second)

	var out []byte
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		if out, err = exec.Command("launchctl", "bootstrap", guiDomain(), target).CombinedOutput(); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Printf("installed %s\n  binary:    %s\n  engine:    %s\n  jobs:      %s\n  output:    %s\n  dashboard: %s\n  notifier:  %s\n",
		launchdLabel, exe, cfg.OpsRoot, cfg.JobsDir, cfg.ArtifactsRoot, cfg.DashboardURL(), notifierAppPath())
	fmt.Printf("\nSet System Settings > Notifications > %s to Alerts, so an overnight\nresult is still on screen in the morning.\n", notifierAppName)
	return nil
}

// cmdReload restarts the daemon in place.
//
// Needed after anything compiled into the binary changes -- Go code, dashboard
// templates, the stylesheet -- and after a job.env, since manifests are read once at
// startup. A run.sh, a lane file or profile.md is read fresh on every run, so those
// take effect without touching the daemon.
//
// kickstart -k rather than bootout-then-bootstrap: it restarts the existing agent
// without a window where the label is unloaded, so a fire during a rebuild is not
// silently dropped.
func cmdReload() error {
	out, err := exec.Command("launchctl", "kickstart", "-k", guiDomain()+"/"+launchdLabel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %w: %s (is the daemon installed?)", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("daemon restarted")
	return nil
}

func cmdUninstallDaemon() error {
	_ = exec.Command("launchctl", "bootout", guiDomain()+"/"+launchdLabel).Run()
	if err := os.Remove(agentPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	// The app bundle is left in place deliberately. Removing it would take its
	// System Settings > Notifications row -- and the Alerts choice made there by hand --
	// with it, which a reinstall cannot put back.
	fmt.Println("removed", launchdLabel)
	fmt.Println("left in place:", notifierAppPath())
	return nil
}

func guiDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// waitUntilUnloaded blocks until launchd no longer knows the label, or the deadline
// passes. `launchctl print` exits non-zero once the service is gone, which is the only
// signal launchd offers that an asynchronous bootout has finished.
func waitUntilUnloaded(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := exec.Command("launchctl", "print", guiDomain()+"/"+launchdLabel).Run(); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
