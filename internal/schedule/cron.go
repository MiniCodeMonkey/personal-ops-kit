// Package schedule matches cron specifications against wall-clock time and works
// out which fires were missed while the daemon was not running.
//
// This is the part of the platform that replaced launchd, so it carries the two
// things launchd was doing for free: firing at the right minute, and catching up a
// fire that was skipped because the machine was asleep. A bug here is invisible --
// a job that stops running looks exactly like a quiet week -- which is why the
// package is small, pure, and covered by table tests.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed five-field cron expression: minute hour day-of-month month
// day-of-week, interpreted in the machine's local time.
type Spec struct {
	minute  fieldSet
	hour    fieldSet
	dom     fieldSet
	month   fieldSet
	dow     fieldSet
	raw     string
	domStar bool
	dowStar bool
}

// fieldSet is a presence set over a small integer range.
type fieldSet map[int]bool

func (f fieldSet) has(v int) bool { return f[v] }

type fieldRange struct{ min, max int }

var fieldRanges = []fieldRange{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 7},  // day of week, where both 0 and 7 mean Sunday
}

// Parse reads a five-field cron expression. Supported per field: `*`, a number, a
// range `a-b`, a list `a,b,c`, and a step `*/n` or `a-b/n`.
func Parse(expr string) (*Spec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron spec %q has %d fields, want 5 (minute hour day-of-month month day-of-week)", expr, len(fields))
	}

	sets := make([]fieldSet, 5)
	for i, f := range fields {
		set, err := parseField(f, fieldRanges[i])
		if err != nil {
			return nil, fmt.Errorf("cron spec %q, field %d (%q): %w", expr, i+1, f, err)
		}
		sets[i] = set
	}

	spec := &Spec{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		raw:     expr,
		domStar: fields[2] == "*",
		dowStar: fields[4] == "*",
	}
	// Sunday is both 0 and 7 in cron. Normalise so callers can just check Weekday().
	if spec.dow.has(7) {
		spec.dow[0] = true
	}
	return spec, nil
}

func parseField(f string, r fieldRange) (fieldSet, error) {
	set := fieldSet{}
	for _, part := range strings.Split(f, ",") {
		if part == "" {
			return nil, fmt.Errorf("empty element")
		}

		step := 1
		if slash := strings.Index(part, "/"); slash >= 0 {
			var err error
			step, err = strconv.Atoi(part[slash+1:])
			if err != nil || step < 1 {
				return nil, fmt.Errorf("bad step %q", part[slash+1:])
			}
			part = part[:slash]
		}

		lo, hi := r.min, r.max
		switch {
		case part == "*":
			// full range
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			var err error
			if lo, err = strconv.Atoi(bounds[0]); err != nil {
				return nil, fmt.Errorf("bad range start %q", bounds[0])
			}
			if hi, err = strconv.Atoi(bounds[1]); err != nil {
				return nil, fmt.Errorf("bad range end %q", bounds[1])
			}
			if lo > hi {
				return nil, fmt.Errorf("range %d-%d is inverted", lo, hi)
			}
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("not a number")
			}
			lo, hi = v, v
		}

		if lo < r.min || hi > r.max {
			return nil, fmt.Errorf("value out of range %d-%d", r.min, r.max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// String returns the original expression.
func (s *Spec) String() string { return s.raw }

// Matches reports whether the spec fires at t, to minute resolution.
//
// Day-of-month and day-of-week combine with OR when both are restricted, which is
// what cron does and what surprises people: "0 8 1 * 1" fires on the 1st AND on
// every Monday, not on Mondays that fall on the 1st.
func (s *Spec) Matches(t time.Time) bool {
	if !s.minute.has(t.Minute()) || !s.hour.has(t.Hour()) || !s.month.has(int(t.Month())) {
		return false
	}
	domOK := s.dom.has(t.Day())
	dowOK := s.dow.has(int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// maxScan bounds the minute-by-minute walks below. A spec that does not fire within
// a year either never fires or is a typo; either way, returning "never" beats
// looping.
const maxScan = 366 * 24 * 60

// Next returns the first fire strictly after t, and whether one was found.
func (s *Spec) Next(t time.Time) (time.Time, bool) {
	c := t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < maxScan; i++ {
		if s.Matches(c) {
			return c, true
		}
		c = c.Add(time.Minute)
	}
	return time.Time{}, false
}

// LastBetween returns the most recent fire in the half-open interval (after, until],
// and whether one exists. It walks forwards rather than backwards so that a very old
// `after` costs no more than the interval the caller actually asked about.
func (s *Spec) LastBetween(after, until time.Time) (time.Time, bool) {
	c := after.Truncate(time.Minute).Add(time.Minute)
	last, found := time.Time{}, false
	for i := 0; i < maxScan && !c.After(until); i++ {
		if s.Matches(c) {
			last, found = c, true
		}
		c = c.Add(time.Minute)
	}
	return last, found
}
