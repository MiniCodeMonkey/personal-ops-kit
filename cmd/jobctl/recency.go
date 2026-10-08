package main

import "time"

// recencyLabel buckets a time for the reading list. Buckets are calendar-day based,
// not rolling: a memo written at 23:00 yesterday is "Yesterday" this morning, which is
// how a person would describe it.
func recencyLabel(t, now time.Time) string {
	if t.IsZero() {
		return "Undated"
	}
	day := func(x time.Time) time.Time {
		y, m, d := x.In(now.Location()).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}
	days := int(day(now).Sub(day(t)).Hours() / 24)
	switch {
	case days <= 0:
		return "Today"
	case days == 1:
		return "Yesterday"
	case days < 7:
		return "This week"
	case days < 14:
		return "Last week"
	case days < 31:
		return "This month"
	}
	return "Older"
}

// artifactGroup is one recency bucket of the reading list.
type artifactGroup struct {
	Label string
	Rows  []artifactRow
}

// groupArtifacts splits rows (already newest-first) into consecutive recency buckets.
func groupArtifacts(rows []artifactRow, now time.Time) []artifactGroup {
	var groups []artifactGroup
	for _, r := range rows {
		label := recencyLabel(r.sortKey, now)
		if n := len(groups); n == 0 || groups[n-1].Label != label {
			groups = append(groups, artifactGroup{Label: label})
		}
		groups[len(groups)-1].Rows = append(groups[len(groups)-1].Rows, r)
	}
	return groups
}
