package main

import (
	"testing"
	"time"
)

func TestRecencyLabelIsCalendarBased(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.Local)
	cases := map[string]time.Time{
		"Today":      time.Date(2026, 8, 25, 0, 30, 0, 0, time.Local),
		"Yesterday":  time.Date(2026, 8, 24, 23, 0, 0, 0, time.Local), // 10h ago, still yesterday
		"This week":  time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local),
		"Last week":  time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local),
		"This month": time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local),
		"Older":      time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local),
		"Undated":    {},
	}
	for want, at := range cases {
		if got := recencyLabel(at, now); got != want {
			t.Errorf("%v: got %q, want %q", at, got, want)
		}
	}
}

func TestGroupArtifactsKeepsOrderAndMergesRuns(t *testing.T) {
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.Local)
	rows := []artifactRow{
		{File: "a", sortKey: now},
		{File: "b", sortKey: now.Add(-time.Hour)},
		{File: "c", sortKey: now.AddDate(0, 0, -1)},
		{File: "d", sortKey: now.AddDate(0, 0, -20)},
	}
	g := groupArtifacts(rows, now)
	if len(g) != 3 || g[0].Label != "Today" || len(g[0].Rows) != 2 || g[1].Label != "Yesterday" || g[2].Label != "This month" {
		t.Errorf("groups = %+v", g)
	}
}
