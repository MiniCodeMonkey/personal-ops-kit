package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return ts
}

// writeJobEnv creates jobsDir/<job>/job.env with the given SCHEDULE line.
func writeJobEnv(t *testing.T, jobsDir, job, schedule string) {
	t.Helper()
	dir := filepath.Join(jobsDir, job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("JOB_NAME=%q\nSCHEDULE=%q\n", job, schedule)
	if err := os.WriteFile(filepath.Join(dir, "job.env"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCadenceDays(t *testing.T) {
	jobsDir := t.TempDir()
	writeJobEnv(t, jobsDir, "daily-job", "15 8 * * *")
	writeJobEnv(t, jobsDir, "weekly-job", "30 7 * * 1")
	writeJobEnv(t, jobsDir, "monthly-job", "45 8 1 * *")

	cases := []struct {
		job    string
		want   int
		wantOK bool
	}{
		{"daily-job", 3, true},
		{"weekly-job", 21, true},
		{"monthly-job", 90, true},
		{"nonexistent-job", 0, false},
	}
	for _, c := range cases {
		got, ok := cadenceDays(jobsDir, c.job)
		if got != c.want || ok != c.wantOK {
			t.Errorf("cadenceDays(%q) = (%d, %v), want (%d, %v)", c.job, got, ok, c.want, c.wantOK)
		}
	}
}

// fullScenarioInputs builds the open and recent folds used by
// TestDigestFullScenario and TestDigestDeterministicOrdering.
func fullScenarioInputs(t *testing.T) (jobsDir string, open, recent foldFile, now, since time.Time) {
	t.Helper()
	jobsDir = t.TempDir()
	writeJobEnv(t, jobsDir, "auction-watch", "0 8 * * 1")  // weekly -> 21
	writeJobEnv(t, jobsDir, "repo-activity", "15 8 * * *") // daily -> 3

	now = mustParse(t, "2026-08-23T09:00:00Z")
	since = mustParse(t, "2026-08-16T00:00:00Z")

	openTS1 := mustParse(t, "2026-07-01T08:00:00Z") // stale under weekly (21d) cadence
	openTS2 := mustParse(t, "2026-08-22T08:00:00Z") // fresh under daily (3d) cadence

	open = foldFile{
		Items: []foldItem{
			{
				TS: openTS1, Job: "auction-watch", Kind: "lot", ID: "lot-1",
				Status: "open", Title: "Rare desk, hammer soon",
				FirstSeen: openTS1, AgeDays: int(now.Sub(openTS1) / (24 * time.Hour)),
			},
			{
				TS: openTS2, Job: "repo-activity", Kind: "branch", ID: "branch-1",
				Status: "open", Title: "feature/foo unpushed",
				FirstSeen: openTS2, AgeDays: int(now.Sub(openTS2) / (24 * time.Hour)),
			},
		},
		Skipped: 2,
	}

	recent = foldFile{
		Items: []foldItem{
			{
				TS: mustParse(t, "2026-08-21T10:00:00Z"), Job: "repo-activity", Kind: "commit",
				ID: "commit-1", Status: "event", Title: "Pushed 3 commits to main",
			},
			{
				TS: mustParse(t, "2026-08-22T09:00:00Z"), Job: "repo-activity", Kind: "commit",
				ID: "commit-2", Status: "event", Title: "Pushed hotfix",
			},
			{
				TS: mustParse(t, "2026-08-20T12:00:00Z"), Job: "auction-watch", Kind: "sighting",
				ID: "sighting-1", Status: "event", Title: "New lot spotted: oak cabinet",
			},
			{
				TS: mustParse(t, "2026-08-19T09:00:00Z"), Job: "repo-activity", Kind: "branch",
				ID: "branch-2", Status: "closed", Title: "feature/bar merged",
			},
		},
		Skipped: 0,
	}
	return jobsDir, open, recent, now, since
}

func TestDigestFullScenario(t *testing.T) {
	jobsDir, open, recent, now, since := fullScenarioInputs(t)

	got, empty := buildDigest(open, recent, jobsDir, now, since)
	if empty {
		t.Fatal("buildDigest reported empty, want content")
	}

	want := "# Weekly review digest -- compiled 2026-08-23, covering since 2026-08-16\n\n" +
		"## Counts\n" +
		"open: 2 (1 stale) · events: 3 · closed: 1 · skipped-lines: 2\n\n" +
		"## Open loops\n\n" +
		"### auction-watch / lot\n" +
		"- Rare desk, hammer soon (open 53d)\n\n" +
		"### repo-activity / branch\n" +
		"- feature/foo unpushed (open 1d)\n\n" +
		"## Stale\n" +
		"- [auction-watch] Rare desk, hammer soon (last seen 53d ago)\n\n" +
		"## Events this week\n\n" +
		"### commit\n" +
		"- Pushed hotfix\n" +
		"- Pushed 3 commits to main\n\n" +
		"### sighting\n" +
		"- New lot spotted: oak cabinet\n\n" +
		"## Closed this week\n" +
		"- [repo-activity] feature/bar merged (branch-2)\n"

	if got != want {
		t.Errorf("digest mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDigestDeterministicOrdering(t *testing.T) {
	jobsDir, open, recent, now, since := fullScenarioInputs(t)
	baseline, _ := buildDigest(open, recent, jobsDir, now, since)

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5; i++ {
		shuffledOpen := append([]foldItem(nil), open.Items...)
		rng.Shuffle(len(shuffledOpen), func(a, b int) { shuffledOpen[a], shuffledOpen[b] = shuffledOpen[b], shuffledOpen[a] })
		shuffledRecent := append([]foldItem(nil), recent.Items...)
		rng.Shuffle(len(shuffledRecent), func(a, b int) { shuffledRecent[a], shuffledRecent[b] = shuffledRecent[b], shuffledRecent[a] })

		got, _ := buildDigest(
			foldFile{Items: shuffledOpen, Skipped: open.Skipped},
			foldFile{Items: shuffledRecent, Skipped: recent.Skipped},
			jobsDir, now, since,
		)
		if got != baseline {
			t.Fatalf("shuffle %d produced different output:\n--- baseline ---\n%s\n--- shuffled ---\n%s", i, baseline, got)
		}
	}
}

func TestDigestTruncation(t *testing.T) {
	jobsDir := t.TempDir()
	writeJobEnv(t, jobsDir, "stale-mill", "0 8 * * 1") // weekly -> 21d threshold
	writeJobEnv(t, jobsDir, "daily-job", "15 8 * * *") // daily -> 3d threshold

	now := mustParse(t, "2026-08-23T09:00:00Z")
	since := mustParse(t, "2026-08-16T00:00:00Z")
	oldTS := mustParse(t, "2026-05-15T08:00:00Z") // well past the weekly threshold

	const staleN = 1500
	items := make([]foldItem, 0, staleN+2)
	for i := 0; i < staleN; i++ {
		id := fmt.Sprintf("lot-%04d", i)
		items = append(items, foldItem{
			TS: oldTS, Job: "stale-mill", Kind: "lot", ID: id,
			Status: "open", Title: fmt.Sprintf("Stale lot %04d", i),
			FirstSeen: oldTS, AgeDays: int(now.Sub(oldTS) / (24 * time.Hour)),
		})
	}
	freshTS := now.Add(-24 * time.Hour)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("task-%d", i)
		items = append(items, foldItem{
			TS: freshTS, Job: "daily-job", Kind: "task", ID: id,
			Status: "open", Title: fmt.Sprintf("Fresh task %d", i),
			FirstSeen: freshTS, AgeDays: int(now.Sub(freshTS) / (24 * time.Hour)),
		})
	}

	open := foldFile{Items: items, Skipped: 0}
	recent := foldFile{}

	got, empty := buildDigest(open, recent, jobsDir, now, since)
	if empty {
		t.Fatal("buildDigest reported empty, want content")
	}
	if len(got) > maxDigestBytes {
		t.Fatalf("digest is %d bytes, want <= %d", len(got), maxDigestBytes)
	}
	if !strings.Contains(got, "more dropped]") {
		t.Error("expected a truncation marker in the digest")
	}
	if got := strings.Count(got, "(open "); got != staleN+2 {
		t.Errorf("Open loops entries = %d, want %d (opens must be preserved)", got, staleN+2)
	}
	if n := strings.Count(got, "(last seen"); n >= staleN {
		t.Errorf("Stale entries = %d, want fewer than %d (stale list must be truncated)", n, staleN)
	}
	for _, title := range []string{"Fresh task 0", "Fresh task 1"} {
		if !strings.Contains(got, title) {
			t.Errorf("expected fresh open item %q to survive truncation", title)
		}
	}
}

func TestDigestEmpty(t *testing.T) {
	jobsDir := t.TempDir()
	now := mustParse(t, "2026-08-23T09:00:00Z")
	since := mustParse(t, "2026-08-16T00:00:00Z")

	got, empty := buildDigest(foldFile{}, foldFile{}, jobsDir, now, since)
	if !empty {
		t.Error("expected empty=true for empty folds")
	}
	if got != "" {
		t.Errorf("expected empty digest, got %q", got)
	}
}

func TestStaleJobGone(t *testing.T) {
	jobsDir := t.TempDir()
	// Deliberately no job.env for "ghost-job".
	now := mustParse(t, "2026-08-23T09:00:00Z")
	since := mustParse(t, "2026-08-16T00:00:00Z")
	ts := now.Add(-48 * time.Hour)

	open := foldFile{
		Items: []foldItem{
			{
				TS: ts, Job: "ghost-job", Kind: "task", ID: "task-1",
				Status: "open", Title: "Orphaned open item",
				FirstSeen: ts, AgeDays: 2,
			},
		},
	}

	got, empty := buildDigest(open, foldFile{}, jobsDir, now, since)
	if empty {
		t.Fatal("buildDigest reported empty, want content")
	}
	if !strings.Contains(got, "## Stale") {
		t.Fatal("expected a Stale section")
	}
	want := "- [ghost-job] Orphaned open item (last seen 2d ago) (job gone)"
	if !strings.Contains(got, want) {
		t.Errorf("expected line %q in digest:\n%s", want, got)
	}
}

// TestDigestCrossJobEventDedup guards against dedup-by-bare-id: the
// entries format's uniqueness key is (job, kind, id), so an event whose id
// collides with an open item's id under a *different* job is a distinct
// subject and must still render.
func TestDigestCrossJobEventDedup(t *testing.T) {
	jobsDir := t.TempDir()
	writeJobEnv(t, jobsDir, "job-a", "15 8 * * *")
	now := mustParse(t, "2026-08-23T09:00:00Z")
	since := mustParse(t, "2026-08-16T00:00:00Z")
	ts := now.Add(-24 * time.Hour)

	open := foldFile{
		Items: []foldItem{
			{
				TS: ts, Job: "job-a", Kind: "task", ID: "shared-id",
				Status: "open", Title: "Open item in job-a",
				FirstSeen: ts, AgeDays: 1,
			},
		},
	}
	recent := foldFile{
		Items: []foldItem{
			{
				TS: ts, Job: "job-b", Kind: "task", ID: "shared-id",
				Status: "event", Title: "Event in job-b with the same id",
			},
		},
	}

	got, empty := buildDigest(open, recent, jobsDir, now, since)
	if empty {
		t.Fatal("buildDigest reported empty, want content")
	}
	if !strings.Contains(got, "Event in job-b with the same id") {
		t.Errorf("expected the cross-job event (same id, different job) to still render:\n%s", got)
	}
}

var droppedRe = regexp.MustCompile(`\[\+(\d+) more dropped\]`)

// sectionBlock extracts the text of a "## <header>" section from digest,
// up to (but not including) the next "\n\n## " section boundary.
func sectionBlock(t *testing.T, digest, header string) string {
	t.Helper()
	marker := "## " + header
	start := strings.Index(digest, marker)
	if start < 0 {
		t.Fatalf("section %q not found in digest:\n%s", header, digest)
	}
	rest := digest[start:]
	if idx := strings.Index(rest, "\n\n## "); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

// droppedCount reads the "[+N more dropped]" marker's N out of a section
// block, or 0 if the section carries no marker.
func droppedCount(t *testing.T, block string) int {
	t.Helper()
	m := droppedRe.FindStringSubmatch(block)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parsing dropped count from %q: %v", m[1], err)
	}
	return n
}

// TestDigestTruncationStaleMarkerExact reproduces the case where the Open
// loops section alone (all items, before any Stale-only truncation)
// already exceeds the byte cap. This forces Phase 2 (dropping the oldest
// open items entirely), which used to make the Stale section's own
// "[+N more dropped]" marker undercount -- it was computed only against
// what Phase 2 left in the kept pool, not the true total. Both section
// markers must equal exactly (true total - what's actually rendered).
func TestDigestTruncationStaleMarkerExact(t *testing.T) {
	jobsDir := t.TempDir()
	writeJobEnv(t, jobsDir, "big-job", "0 8 * * 1") // weekly -> 21d threshold
	now := mustParse(t, "2026-08-23T09:00:00Z")
	since := mustParse(t, "2026-08-16T00:00:00Z")
	oldTS := mustParse(t, "2026-01-01T08:00:00Z") // well past 21d -> every item is stale

	const n = 400
	longSuffix := strings.Repeat("x", 200)
	items := make([]foldItem, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("item-%04d", i)
		items = append(items, foldItem{
			TS: oldTS, Job: "big-job", Kind: "lot", ID: id,
			Status: "open", Title: fmt.Sprintf("Item %04d %s", i, longSuffix),
			FirstSeen: oldTS, AgeDays: int(now.Sub(oldTS) / (24 * time.Hour)),
		})
	}
	open := foldFile{Items: items}

	got, empty := buildDigest(open, foldFile{}, jobsDir, now, since)
	if empty {
		t.Fatal("buildDigest reported empty, want content")
	}
	if len(got) > maxDigestBytes {
		t.Fatalf("digest is %d bytes, want <= %d", len(got), maxDigestBytes)
	}

	renderedOpen := strings.Count(got, "(open ")
	renderedStale := strings.Count(got, "(last seen")
	if renderedOpen == n {
		t.Fatal("test setup didn't force Open loops truncation -- widen the fixture")
	}

	openBlock := sectionBlock(t, got, "Open loops")
	staleBlock := sectionBlock(t, got, "Stale")

	wantOpenDropped := n - renderedOpen
	wantStaleDropped := n - renderedStale // every item here is stale

	if d := droppedCount(t, openBlock); d != wantOpenDropped {
		t.Errorf("Open loops dropped marker = %d, want exactly %d (total %d - rendered %d)", d, wantOpenDropped, n, renderedOpen)
	}
	if d := droppedCount(t, staleBlock); d != wantStaleDropped {
		t.Errorf("Stale dropped marker = %d, want exactly %d (total %d - rendered %d)", d, wantStaleDropped, n, renderedStale)
	}
}
