package notify

import (
	"testing"
	"time"
)

func TestWorthSuppressesNonEvents(t *testing.T) {
	cases := map[Outcome]bool{
		OutcomeOK:       true,
		OutcomeWarning:  true,
		OutcomeFailed:   true,
		OutcomeNoChange: false, // ran fine, found nothing -- not news
		OutcomeGated:    false, // deliberately did not run -- not an event
	}
	for outcome, want := range cases {
		if got := Worth(outcome); got != want {
			t.Errorf("Worth(%q) = %v, want %v", outcome, got, want)
		}
	}
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDueAt(t *testing.T) {
	p := DefaultReminderPolicy()
	if !p.DueAt(at("2026-08-12 08:30")) {
		t.Error("08:30 should be the nudge minute")
	}
	for _, s := range []string{"2026-08-12 08:29", "2026-08-12 08:31", "2026-08-12 20:30"} {
		if p.DueAt(at(s)) {
			t.Errorf("%s should not be the nudge minute", s)
		}
	}
}

// The behaviour the user actually asked for: a result produced last night is still
// waiting in the morning.
func TestOvernightResultIsRemindedNextMorning(t *testing.T) {
	p := DefaultReminderPolicy()
	memo := Pending{
		Job: "nightly-research", Outcome: OutcomeOK,
		Headline:   "CI critical path",
		FinishedAt: at("2026-08-11 19:19"), // finished last night
	}
	due := p.Select([]Pending{memo}, at("2026-08-12 08:30"))
	if len(due) != 1 {
		t.Fatalf("got %d reminders, want 1 -- the overnight memo would be missed", len(due))
	}
}

func TestSelectFiltersCorrectly(t *testing.T) {
	p := DefaultReminderPolicy()
	now := at("2026-08-12 08:30")

	cases := []struct {
		name string
		in   Pending
		want bool
	}{
		{
			name: "finished overnight, unread",
			in:   Pending{Job: "a", Outcome: OutcomeOK, FinishedAt: at("2026-08-11 19:19")},
			want: true,
		},
		{
			// A run that finished at 08:29 is not something you have failed to notice.
			name: "finished minutes ago",
			in:   Pending{Job: "b", Outcome: OutcomeOK, FinishedAt: at("2026-08-12 08:29")},
			want: false,
		},
		{
			name: "already reminded today",
			in: Pending{Job: "c", Outcome: OutcomeOK, FinishedAt: at("2026-08-11 19:19"),
				RemindedAt: at("2026-08-12 08:30")},
			want: false,
		},
		{
			name: "reminded yesterday, still unread",
			in: Pending{Job: "d", Outcome: OutcomeOK, FinishedAt: at("2026-08-10 19:19"),
				RemindedAt: at("2026-08-11 08:30")},
			want: true,
		},
		{
			name: "older than the give-up horizon",
			in:   Pending{Job: "e", Outcome: OutcomeOK, FinishedAt: at("2026-07-20 19:19")},
			want: false,
		},
		{
			name: "quiet day is never reminded about",
			in:   Pending{Job: "f", Outcome: OutcomeNoChange, FinishedAt: at("2026-08-11 19:19")},
			want: false,
		},
		{
			name: "failure from overnight",
			in:   Pending{Job: "g", Outcome: OutcomeFailed, FinishedAt: at("2026-08-11 19:19")},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := len(p.Select([]Pending{c.in}, now)) == 1
			if got != c.want {
				t.Errorf("selected = %v, want %v", got, c.want)
			}
		})
	}
}

// A daemon restart inside the nudge minute must not re-post what it already sent.
func TestNudgeIsIdempotentWithinTheMinute(t *testing.T) {
	p := DefaultReminderPolicy()
	now := at("2026-08-12 08:30")
	x := Pending{Job: "a", Outcome: OutcomeOK, FinishedAt: at("2026-08-11 19:19")}

	first := p.Select([]Pending{x}, now)
	if len(first) != 1 {
		t.Fatalf("first pass selected %d, want 1", len(first))
	}

	// The daemon records that it reminded, then restarts and evaluates again.
	x.RemindedAt = now
	if second := p.Select([]Pending{x}, now); len(second) != 0 {
		t.Errorf("second pass selected %d, want 0 -- the reminder would double-post", len(second))
	}
}

// The contract with notifier.swift: a click must be able to find the exact result, so
// the URL names the job and run, and the identifier is per job so a re-post replaces.
func TestPostArgsCarryTheRunBehindTheClick(t *testing.T) {
	n := &Notifier{DashboardURL: "http://127.0.0.1:7777"}
	args := n.postArgs(Notification{
		Job: "nightly-research", Outcome: OutcomeFailed, Headline: "boom", RunID: "r-1",
	})
	if args[0] != "post" {
		t.Fatalf("args[0] = %q, want post", args[0])
	}
	want := map[string]string{
		"--group":   "dev.personal-ops.nightly-research",
		"--url":     "http://127.0.0.1:7777/open?job=nightly-research&run=r-1",
		"--message": "boom",
		"--sound":   "Basso", // failures make a noise
	}
	got := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 {
		got[args[i]] = args[i+1]
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	ok := n.postArgs(Notification{Job: "j", Outcome: OutcomeOK, RunID: "r"})
	for _, a := range ok {
		if a == "--sound" {
			t.Error("a successful run should be silent")
		}
	}
}

func TestSubtitleDistinguishesReminderFromFreshRun(t *testing.T) {
	n := &Notifier{}
	fresh := n.subtitle(Notification{Job: "nightly-research", Outcome: OutcomeOK})
	remind := n.subtitle(Notification{Job: "nightly-research", Outcome: OutcomeOK, Reminder: true})
	if fresh == remind {
		t.Fatal("a morning reminder reads identically to a fresh run")
	}
	if !contains(remind, "unread") {
		t.Errorf("reminder subtitle %q should say it is unread", remind)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > 0 && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
