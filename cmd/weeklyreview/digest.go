// Package main compiles the weekly review digest from two jobctl entry
// folds: the current set of open items ("-open") and a recent window
// ("-recent") that supplies this week's events and closures. This file is
// the pure, argv-free digest assembler; main.go is the CLI shell around it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// foldItem mirrors the fields of jobctl's fold output this job reads.
type foldItem struct {
	TS        time.Time `json:"ts"`
	Job       string    `json:"job"`
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Title     string    `json:"title"`
	FirstSeen time.Time `json:"first_seen"`
	AgeDays   int       `json:"age_days"`
}

type foldFile struct {
	Items   []foldItem `json:"items"`
	Skipped int        `json:"skipped"`
}

// maxDigestBytes bounds the compiled digest so a runaway fold can never
// blow up the memo the weekly review is supposed to be a short read of.
const maxDigestBytes = 80000

var scheduleRe = regexp.MustCompile(`SCHEDULE\s*=\s*"([^"]*)"`)

// cadenceDays returns the staleness threshold for a job: 3 (daily), 21
// (weekly: cron has a specific day-of-week), 90 (monthly: specific
// day-of-month), and (0, false) when the job dir/env is missing or the
// SCHEDULE line can't be parsed as a 5-field cron expression.
func cadenceDays(jobsDir, job string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(jobsDir, job, "job.env"))
	if err != nil {
		return 0, false
	}
	m := scheduleRe.FindSubmatch(data)
	if m == nil {
		return 0, false
	}
	fields := strings.Fields(string(m[1]))
	if len(fields) != 5 {
		return 0, false
	}
	dom, dow := fields[2], fields[4]
	switch {
	case dow != "*":
		return 21, true
	case dom != "*":
		return 90, true
	default:
		return 3, true
	}
}

// openEntry is an open-fold item plus its computed staleness.
type openEntry struct {
	item         foldItem
	stale        bool
	jobGone      bool
	lastSeenDays int // whole days since item.TS, as of "now"
}

// buildDigest renders the deterministic digest. empty=true when there is
// nothing at all to compile (no opens, no events, no closures) -- the
// caller should print nothing and exit 3.
func buildDigest(open, recent foldFile, jobsDir string, now, since time.Time) (digest string, empty bool) {
	entries := classifyOpen(open.Items, jobsDir, now)

	// The fold's uniqueness key is (job, kind, id) -- cross-job (or
	// cross-kind) id collisions are by design in the entries format, so
	// dedup against that full triple, not the bare id.
	openKeys := make(map[[3]string]bool, len(entries))
	for _, e := range entries {
		openKeys[[3]string{e.item.Job, e.item.Kind, e.item.ID}] = true
	}

	var events, closures []foldItem
	for _, it := range recent.Items {
		switch it.Status {
		case "event":
			if openKeys[[3]string{it.Job, it.Kind, it.ID}] {
				continue
			}
			events = append(events, it)
		case "closed":
			closures = append(closures, it)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if !a.TS.Equal(b.TS) {
			return a.TS.After(b.TS) // newest first within a kind
		}
		return a.ID < b.ID
	})
	sort.SliceStable(closures, func(i, j int) bool {
		a, b := closures[i], closures[j]
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		return a.ID < b.ID
	})

	if len(entries) == 0 && len(events) == 0 && len(closures) == 0 {
		return "", true
	}

	skipped := open.Skipped + recent.Skipped

	render := func(staleKeep, openDrop int) string {
		return renderDigest(entries, events, closures, now, since, skipped, staleKeep, openDrop)
	}

	full := render(-1, 0)
	if len(full) <= maxDigestBytes {
		return full, false
	}

	staleTotal := 0
	for _, e := range entries {
		if e.stale {
			staleTotal++
		}
	}

	// Phase 1: drop stale items first. Binary search the largest staleKeep
	// (in canonical order) that still fits.
	lo, hi, bestKeep := 0, staleTotal, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if len(render(mid, 0)) <= maxDigestBytes {
			bestKeep = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if bestKeep >= 0 {
		return render(bestKeep, 0), false
	}

	// Phase 2: even with all stale items dropped from Stale it doesn't fit
	// (a huge Open loops section). Drop the oldest open items too. Binary
	// search the smallest drop count that fits.
	openTotal := len(entries)
	lo, hi, bestDrop := 0, openTotal, openTotal
	for lo <= hi {
		mid := (lo + hi) / 2
		if len(render(0, mid)) <= maxDigestBytes {
			bestDrop = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	return render(0, bestDrop), false
}

// classifyOpen sorts open items into canonical order (job asc, kind asc,
// first_seen asc, id asc as a final tiebreak) and computes each one's
// staleness against its writing job's cadence.
func classifyOpen(items []foldItem, jobsDir string, now time.Time) []openEntry {
	sorted := append([]foldItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if !a.FirstSeen.Equal(b.FirstSeen) {
			return a.FirstSeen.Before(b.FirstSeen)
		}
		return a.ID < b.ID
	})

	type cadence struct {
		days int
		ok   bool
	}
	cache := map[string]cadence{}

	entries := make([]openEntry, len(sorted))
	for i, it := range sorted {
		c, ok := cache[it.Job]
		if !ok {
			d, found := cadenceDays(jobsDir, it.Job)
			c = cadence{d, found}
			cache[it.Job] = c
		}
		stale := !c.ok
		if c.ok {
			threshold := time.Duration(c.days) * 24 * time.Hour
			stale = now.Sub(it.TS) > threshold
		}
		entries[i] = openEntry{
			item:         it,
			stale:        stale,
			jobGone:      !c.ok,
			lastSeenDays: int(now.Sub(it.TS) / (24 * time.Hour)),
		}
	}
	return entries
}

// renderDigest builds the digest text. staleKeep caps how many stale
// entries (in canonical order) appear in the Stale section; -1 means no
// cap. openDrop drops that many of the globally-oldest open items (by
// first_seen ascending) from the Open loops section entirely -- and from
// the Stale section, since a dropped item can't stale-render either.
func renderDigest(entries []openEntry, events, closures []foldItem, now, since time.Time, skipped, staleKeep, openDrop int) string {
	kept := entries
	openTruncated := 0
	if openDrop > 0 {
		oldest := append([]openEntry(nil), entries...)
		sort.SliceStable(oldest, func(i, j int) bool {
			return oldest[i].item.FirstSeen.Before(oldest[j].item.FirstSeen)
		})
		n := openDrop
		if n > len(oldest) {
			n = len(oldest)
		}
		dropped := make(map[string]bool, n)
		for _, e := range oldest[:n] {
			dropped[e.item.Job+"\x00"+e.item.Kind+"\x00"+e.item.ID] = true
		}
		kept = kept[:0:0]
		for _, e := range entries {
			key := e.item.Job + "\x00" + e.item.Kind + "\x00" + e.item.ID
			if dropped[key] {
				openTruncated++
				continue
			}
			kept = append(kept, e)
		}
	}

	// Counts always reflect the true totals, independent of what the cap
	// forces the digest to omit from view.
	openCount := len(entries)
	staleCount := 0
	for _, e := range entries {
		if e.stale {
			staleCount++
		}
	}

	staleCandidates := make([]openEntry, 0, len(kept))
	for _, e := range kept {
		if e.stale {
			staleCandidates = append(staleCandidates, e)
		}
	}

	staleShown := staleCandidates
	if staleKeep >= 0 && staleKeep < len(staleCandidates) {
		staleShown = staleCandidates[:staleKeep]
	}
	// The dropped count is against the true total (staleCount), not just
	// the candidates still present in kept -- a stale item can vanish from
	// view either because Stale itself was truncated (staleKeep) or
	// because Phase 2 dropped it from Open loops entirely. Either way it
	// must still be accounted for here: "no silent truncation" means the
	// marker's N always equals total-stale minus what's actually rendered.
	staleTruncated := staleCount - len(staleShown)

	var blocks []string

	blocks = append(blocks, fmt.Sprintf(
		"# Weekly review digest -- compiled %s, covering since %s",
		now.Format("2006-01-02"), since.Format("2006-01-02")))

	blocks = append(blocks, fmt.Sprintf(
		"## Counts\nopen: %d (%d stale) · events: %d · closed: %d · skipped-lines: %d",
		openCount, staleCount, len(events), len(closures), skipped))

	if openCount > 0 {
		blocks = append(blocks, renderOpenLoops(kept, openTruncated))
	}

	if staleCount > 0 {
		blocks = append(blocks, renderStale(staleShown, staleTruncated))
	}

	if len(events) > 0 {
		blocks = append(blocks, renderEvents(events))
	}

	if len(closures) > 0 {
		blocks = append(blocks, renderClosures(closures))
	}

	return strings.Join(blocks, "\n\n") + "\n"
}

func renderOpenLoops(entries []openEntry, truncated int) string {
	var sub []string
	var curJob, curKind string
	var curLines []string
	flush := func() {
		if curLines != nil {
			sub = append(sub, fmt.Sprintf("### %s / %s\n%s", curJob, curKind, strings.Join(curLines, "\n")))
		}
	}
	for _, e := range entries {
		if e.item.Job != curJob || e.item.Kind != curKind {
			flush()
			curJob, curKind = e.item.Job, e.item.Kind
			curLines = nil
		}
		curLines = append(curLines, fmt.Sprintf("- %s (open %dd)", e.item.Title, e.item.AgeDays))
	}
	flush()

	if truncated > 0 {
		sub = append(sub, fmt.Sprintf("[+%d more dropped]", truncated))
	}

	body := strings.Join(sub, "\n\n")
	if body == "" {
		return "## Open loops"
	}
	return "## Open loops\n\n" + body
}

func renderStale(entries []openEntry, truncated int) string {
	lines := make([]string, 0, len(entries)+1)
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf("- [%s] %s (last seen %dd ago)%s",
			e.item.Job, e.item.Title, e.lastSeenDays, jobGoneSuffix(e.jobGone)))
	}
	if truncated > 0 {
		lines = append(lines, fmt.Sprintf("[+%d more dropped]", truncated))
	}
	return "## Stale\n" + strings.Join(lines, "\n")
}

func jobGoneSuffix(gone bool) string {
	if gone {
		return " (job gone)"
	}
	return ""
}

func renderEvents(events []foldItem) string {
	var sub []string
	curKind := events[0].Kind
	var curLines []string
	flush := func() {
		if curLines != nil {
			sub = append(sub, fmt.Sprintf("### %s\n%s", curKind, strings.Join(curLines, "\n")))
		}
	}
	for _, e := range events {
		if e.Kind != curKind {
			flush()
			curKind = e.Kind
			curLines = nil
		}
		curLines = append(curLines, fmt.Sprintf("- %s", e.Title))
	}
	flush()
	return "## Events this week\n\n" + strings.Join(sub, "\n\n")
}

func renderClosures(closures []foldItem) string {
	lines := make([]string, len(closures))
	for i, c := range closures {
		lines[i] = fmt.Sprintf("- [%s] %s (%s)", c.Job, c.Title, c.ID)
	}
	return "## Closed this week\n" + strings.Join(lines, "\n")
}
