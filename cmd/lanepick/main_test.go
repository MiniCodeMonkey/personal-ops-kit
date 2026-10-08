package main

import (
	"sort"
	"testing"
)

func monthList(t *testing.T, spec string) []int {
	t.Helper()
	m, err := parseMonths(spec)
	if err != nil {
		t.Fatalf("parseMonths(%q): %v", spec, err)
	}
	var out []int
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Expected values taken from running the Python original, not from reading it. The
// season window is the one part of the port the CLI-level differential cannot reach,
// because neither program lets you pretend it is a different month.
func TestParseMonthsMatchesTheOriginal(t *testing.T) {
	all := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	cases := []struct {
		spec string
		want []int
	}{
		{"all", all},
		{"", all}, // an absent value means the whole year
		{"7-11", []int{7, 8, 9, 10, 11}},
		{"2-8", []int{2, 3, 4, 5, 6, 7, 8}}, // garden-projects
		{"6-11", []int{6, 7, 8, 9, 10, 11}}, // halloween-props
		{"11-2", []int{1, 2, 11, 12}},       // wraps the year end
		{"12-1", []int{1, 12}},              // the tightest wrap
		{"3,4,5", []int{3, 4, 5}},
		{"5", []int{5}},
		{"1-12", all},
		{"0-13", all}, // out-of-range values are dropped after expansion
		{"7,", []int{7}},
	}
	for _, c := range cases {
		if got := monthList(t, c.spec); !equal(got, c.want) {
			t.Errorf("parseMonths(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

// The original raised ValueError on these and crashed the night. Silently returning
// an empty set would be worse: the lane would drop out of rotation permanently and
// nothing would ever report it.
func TestParseMonthsRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"bogus", "x-y", "3-", "-4", "jan"} {
		if _, err := parseMonths(spec); err == nil {
			t.Errorf("parseMonths(%q) was accepted; a typo would silently retire the lane", spec)
		}
	}
}

func TestFormatWeightMatchesPythonPercentG(t *testing.T) {
	cases := map[float64]string{1: "1", 2: "2", 3: "3", 2.5: "2.5", 0: "0", 0.5: "0.5"}
	for in, want := range cases {
		if got := formatWeight(in); got != want {
			t.Errorf("formatWeight(%v) = %q, want %q", in, got, want)
		}
	}
}
