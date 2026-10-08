package schedule

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func mustParse(t *testing.T, expr string) *Spec {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, expr := range []string{
		"", "0 8 * *", "0 8 * * * *",
		"60 8 * * *",  // minute out of range
		"0 24 * * *",  // hour out of range
		"0 8 0 * *",   // day-of-month is 1-based
		"0 8 * 13 *",  // month out of range
		"0 8 * * 8",   // day-of-week out of range
		"5-1 8 * * *", // inverted range
		"*/0 8 * * *", // zero step
		"x 8 * * *",   // not a number
		"0 8 * * mon", // names are not supported
	} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", expr)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		expr string
		when string
		want bool
	}{
		{"0 8 * * *", "2026-08-11 08:00", true},
		{"0 8 * * *", "2026-08-11 08:01", false},
		{"0 8 * * *", "2026-08-11 09:00", false},
		{"47 18 * * *", "2026-08-11 18:47", true},
		// Every day of the week listed individually, as the old plist did.
		{"47 18 * * 0,1,2,3,4,5,6", "2026-08-15 18:47", true},
		// 2026-08-11 is a Tuesday.
		{"0 8 * * 2", "2026-08-11 08:00", true},
		{"0 8 * * 3", "2026-08-11 08:00", false},
		// Sunday is both 0 and 7. 2026-08-16 is a Sunday.
		{"0 8 * * 7", "2026-08-16 08:00", true},
		{"0 8 * * 0", "2026-08-16 08:00", true},
		{"*/15 * * * *", "2026-08-11 09:30", true},
		{"*/15 * * * *", "2026-08-11 09:31", false},
		{"0 9-17 * * *", "2026-08-11 17:00", true},
		{"0 9-17 * * *", "2026-08-11 18:00", false},
		// When both day-of-month and day-of-week are restricted, cron ORs them.
		// The 1st of August 2026 is a Saturday, so a Monday rule still fires on it.
		{"0 8 1 * 1", "2026-08-01 08:00", true},
		{"0 8 1 * 1", "2026-08-03 08:00", true},  // a Monday
		{"0 8 1 * 1", "2026-08-04 08:00", false}, // neither
	}
	for _, c := range cases {
		if got := mustParse(t, c.expr).Matches(at(c.when)); got != c.want {
			t.Errorf("Parse(%q).Matches(%s) = %v, want %v", c.expr, c.when, got, c.want)
		}
	}
}

func TestNext(t *testing.T) {
	cases := []struct{ expr, from, want string }{
		{"0 8 * * *", "2026-08-11 07:59", "2026-08-11 08:00"},
		{"0 8 * * *", "2026-08-11 08:00", "2026-08-12 08:00"}, // strictly after
		{"0 8 * * *", "2026-08-11 08:01", "2026-08-12 08:00"},
		{"47 18 * * *", "2026-08-11 20:17", "2026-08-12 18:47"},
		{"0 7 * * 1", "2026-08-11 12:00", "2026-08-17 07:00"}, // next Monday
	}
	for _, c := range cases {
		got, ok := mustParse(t, c.expr).Next(at(c.from))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("Parse(%q).Next(%s) = %v (ok=%v), want %s", c.expr, c.from, got, ok, c.want)
		}
	}
}

// The behaviour this package exists for. Each case is a real situation the daemon
// hits, and getting any of them wrong means a job silently stops or double-fires.
func TestEvaluate(t *testing.T) {
	daily8 := mustParse(t, "0 8 * * *")
	const window = 12 * time.Hour

	cases := []struct {
		name     string
		lastFire string
		now      string
		want     Decision
		wantFire string
	}{
		{
			name:     "on time",
			lastFire: "2026-08-10 08:00", now: "2026-08-11 08:00",
			want: Due, wantFire: "2026-08-11 08:00",
		},
		{
			name:     "nothing due between fires",
			lastFire: "2026-08-11 08:00", now: "2026-08-11 14:30",
			want: Idle,
		},
		{
			name:     "asleep across the fire, woken six hours later",
			lastFire: "2026-08-10 08:00", now: "2026-08-11 14:00",
			want: Catchup, wantFire: "2026-08-11 08:00",
		},
		{
			// Closed for days and reopened before the next fire is due. What matters
			// is the age of the most recent missed slot, not how long the gap was:
			// at 06:00 the newest missed fire is the previous day's 08:00, 22 hours
			// old, so it is abandoned rather than run nearly a day late.
			name:     "long gap, newest missed fire is itself stale",
			lastFire: "2026-08-08 08:00", now: "2026-08-11 06:00",
			want: Expired, wantFire: "2026-08-10 08:00",
		},
		{
			// Same long gap, but reopened after today's fire time. The newest missed
			// slot is only four hours old, so it still runs.
			name:     "long gap, newest missed fire is recent",
			lastFire: "2026-08-08 08:00", now: "2026-08-11 12:00",
			want: Catchup, wantFire: "2026-08-11 08:00",
		},
		{
			name:     "just inside the window",
			lastFire: "2026-08-10 08:00", now: "2026-08-11 19:59",
			want: Catchup, wantFire: "2026-08-11 08:00",
		},
		{
			name:     "just outside the window",
			lastFire: "2026-08-10 08:00", now: "2026-08-11 20:01",
			want: Expired, wantFire: "2026-08-11 08:00",
		},
		{
			name:     "never run, and not due this minute",
			lastFire: "", now: "2026-08-11 14:00",
			want: Idle,
		},
		{
			name:     "never run, due right now",
			lastFire: "", now: "2026-08-11 08:00",
			want: Due, wantFire: "2026-08-11 08:00",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var last time.Time
			if c.lastFire != "" {
				last = at(c.lastFire)
			}
			got, fire := Evaluate(daily8, last, at(c.now), window)
			if got != c.want {
				t.Fatalf("Evaluate = %v, want %v", got, c.want)
			}
			if c.wantFire != "" && !fire.Equal(at(c.wantFire)) {
				t.Errorf("fire = %v, want %s", fire, c.wantFire)
			}
		})
	}
}

// A missed fire must run exactly once. If Evaluate kept reporting Catchup after the
// daemon recorded the run, a job would loop; if it reported Idle before, the run
// would be lost. Both failures are silent, so they are asserted directly.
func TestCatchupFiresExactlyOnce(t *testing.T) {
	spec := mustParse(t, "0 8 * * *")
	const window = 12 * time.Hour

	last := at("2026-08-10 08:00")
	now := at("2026-08-11 14:00")

	d, fire := Evaluate(spec, last, now, window)
	if d != Catchup {
		t.Fatalf("first evaluation = %v, want Catchup", d)
	}

	// The daemon records the fire it honoured, then ticks again a minute later.
	d2, _ := Evaluate(spec, fire, now.Add(time.Minute), window)
	if d2 != Idle {
		t.Fatalf("second evaluation = %v, want Idle -- the job would run twice", d2)
	}
}

// A long gap must not replay every missed fire, only the most recent one.
func TestLongGapCollapsesToOneFire(t *testing.T) {
	spec := mustParse(t, "0 8 * * *")
	last := at("2026-08-01 08:00")
	now := at("2026-08-11 09:00")

	d, fire := Evaluate(spec, last, now, 12*time.Hour)
	if d != Catchup {
		t.Fatalf("decision = %v, want Catchup", d)
	}
	if !fire.Equal(at("2026-08-11 08:00")) {
		t.Errorf("fire = %v, want the most recent missed slot 2026-08-11 08:00", fire)
	}
}
