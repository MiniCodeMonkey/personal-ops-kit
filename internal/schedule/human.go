package schedule

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Human renders the spec the way a person would say it: "Every day at 19:00",
// "Weekdays at 17:30", "Mondays at 07:00", "The 1st of every month at 08:45".
// Anything the phrasing cannot cover honestly falls back to the raw expression,
// rather than describing a schedule that is not quite the real one.
func (s *Spec) Human() string {
	minutes, hours := sorted(s.minute), sorted(s.hour)
	if len(minutes) != 1 || len(hours) != 1 || len(s.month) != 12 {
		return s.raw
	}
	at := fmt.Sprintf("%02d:%02d", hours[0], minutes[0])

	switch {
	case s.domStar && s.dowStar:
		return "Every day at " + at
	case s.domStar:
		days := sorted(s.dow)
		if isRange(days, 1, 5) {
			return "Weekdays at " + at
		}
		if isRange(days, 0, 6) || (len(days) == 7) {
			return "Every day at " + at
		}
		names := make([]string, len(days))
		for i, d := range days {
			names[i] = time.Weekday(d%7).String() + "s"
		}
		return joinAnd(names) + " at " + at
	case s.dowStar:
		days := sorted(s.dom)
		if len(days) == 1 {
			return fmt.Sprintf("The %s of every month at %s", ordinal(days[0]), at)
		}
	}
	return s.raw
}

func sorted(f fieldSet) []int {
	out := make([]int, 0, len(f))
	for v := range f {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func isRange(vals []int, lo, hi int) bool {
	if len(vals) != hi-lo+1 {
		return false
	}
	for i, v := range vals {
		if v != lo+i {
			return false
		}
	}
	return true
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
