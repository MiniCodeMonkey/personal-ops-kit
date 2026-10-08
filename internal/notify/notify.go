// Package notify delivers run results as native macOS notifications.
//
// The hard requirement is that an overnight result is still waiting in the morning.
// macOS cannot be made to guarantee that on its own: whether a banner stays on
// screen or vanishes after a few seconds is the Alerts-versus-Banners setting, which
// is per-app, user-controlled, and not settable from code. A Focus mode can suppress
// delivery entirely, and a banner brushed aside while reaching for the trackpad is
// gone.
//
// So the guarantee lives here instead. A notification is posted under a per-job
// identifier so it replaces its predecessor rather than stacking; it stays in
// Notification Centre until the result is acknowledged in the dashboard; and the daemon
// re-posts a job's latest result at a set morning hour if it is still unacknowledged.
// The Alerts setting then becomes an improvement rather than a load-bearing dependency.
//
// Delivery goes through the Personal Ops app (notifier.swift, compiled into
// ~/Applications by `jobctl install-daemon`). It used to go through terminal-notifier
// with -sender spoofing this bundle's identity, which stopped routing clicks on macOS
// 26: usernoted now matches the app it launches against the code identity of whatever
// posted the notification, and a spoofed sender is by definition not that. The only
// arrangement that works is for the app that owns the notifications to be the one that
// posts them, so that is what the Swift program is.
package notify

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AppSource is the notifier app's Swift source, compiled at install time. Shipping the
// source rather than a binary keeps the repository free of unsigned Mach-O blobs and
// lets the install step ad-hoc sign for the machine it runs on.
//
//go:embed notifier.swift
var AppSource []byte

// AppIcon is the 1024px render of the dashboard favicon, turned into an .icns at
// install time. Without an icon a notification shows an empty grey tile.
//
//go:embed icon.png
var AppIcon []byte

// Outcome mirrors a ledger entry's result and decides both whether a notification is
// worth sending and how loud it should be.
type Outcome string

const (
	OutcomeOK       Outcome = "ok"
	OutcomeWarning  Outcome = "warning"
	OutcomeNoChange Outcome = "no-change"
	OutcomeGated    Outcome = "gated"
	OutcomeFailed   Outcome = "failed"
)

// Notifier posts to macOS Notification Centre via the Personal Ops app.
type Notifier struct {
	// Binary is the app's executable, inside its bundle. It has to be run from there:
	// UNUserNotificationCenter refuses a process with no bundle identity.
	Binary string
	// DashboardURL is the base a click opens under.
	DashboardURL string
	// Timeout bounds a single post. Posting is fast, but a wedged call must never stall
	// the run loop.
	Timeout time.Duration
}

// New returns a Notifier for the app executable at binary.
func New(binary, dashboardURL string) (*Notifier, error) {
	if _, err := os.Stat(binary); err != nil {
		return nil, fmt.Errorf("notifier app not installed (run `jobctl install-daemon`): %w", err)
	}
	return &Notifier{
		Binary:       binary,
		DashboardURL: strings.TrimRight(dashboardURL, "/"),
		Timeout:      30 * time.Second,
	}, nil
}

// Notification is one result worth telling the user about.
type Notification struct {
	Job      string
	Outcome  Outcome
	Headline string // one line: what came out of the run
	RunID    string
	// Reminder marks a re-post of something already delivered but not acknowledged,
	// which is labelled differently so a morning nudge is not mistaken for a fresh run.
	Reminder bool
}

// Worth reports whether an outcome deserves interrupting someone.
//
// A quiet day is not news: `no-change` means the job ran correctly and found nothing,
// and notifying about it trains you to dismiss this app's notifications without
// reading them, which costs the ones that matter. `gated` is likewise not an event --
// the job deliberately did not run. Both remain visible in the dashboard.
func Worth(o Outcome) bool {
	switch o {
	case OutcomeOK, OutcomeWarning, OutcomeFailed:
		return true
	default:
		return false
	}
}

// group keeps one notification per job. A second post replaces the first, so a job
// that runs daily cannot accumulate a week of stale banners in Notification Centre.
func group(job string) string { return "dev.personal-ops." + job }

func (n *Notifier) subtitle(x Notification) string {
	switch {
	case x.Reminder && x.Outcome == OutcomeFailed:
		return x.Job + " · failed overnight, still unread"
	case x.Reminder:
		return x.Job + " · from overnight, still unread"
	case x.Outcome == OutcomeFailed:
		return x.Job + " · failed"
	case x.Outcome == OutcomeWarning:
		return x.Job + " · finished with a warning"
	default:
		return x.Job
	}
}

// sound is reserved for failures. Everything else arrives silently: a job that worked
// does not need to make a noise, and a sound on every success is the fastest way to
// make someone turn the whole thing off.
func (n *Notifier) sound(x Notification) string {
	if x.Outcome == OutcomeFailed {
		return "Basso"
	}
	return ""
}

// clickURL is what opening the notification lands on. It goes through the dashboard's
// /open rather than straight to the artifact because only the daemon knows whether the
// run left one and what it is called; /open redirects to the memo when there is one and
// to the run's entry on the job page when there is not.
func (n *Notifier) clickURL(x Notification) string {
	q := url.Values{}
	q.Set("job", x.Job)
	if x.RunID != "" {
		q.Set("run", x.RunID)
	}
	return n.DashboardURL + "/open?" + q.Encode()
}

// postArgs is the app invocation for one notification, separated out so the contract
// with notifier.swift can be tested without Notification Centre.
func (n *Notifier) postArgs(x Notification) []string {
	message := x.Headline
	if message == "" {
		message = "Run finished."
	}
	args := []string{
		"post",
		"--title", "Personal Ops",
		"--subtitle", n.subtitle(x),
		"--message", message,
		"--group", group(x.Job),
		"--url", n.clickURL(x),
	}
	if s := n.sound(x); s != "" {
		args = append(args, "--sound", s)
	}
	return args
}

// Post delivers one notification.
func (n *Notifier) Post(ctx context.Context, x Notification) error {
	ctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, n.Binary, n.postArgs(x)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("notifier post: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Clear removes a job's notification from Notification Centre. Called when a result
// is acknowledged in the dashboard, so what is sitting there is always what still
// needs attention rather than a log of everything that ever happened.
func (n *Notifier) Clear(ctx context.Context, job string) error {
	ctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, n.Binary, "remove", "--group", group(job)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("notifier remove: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
