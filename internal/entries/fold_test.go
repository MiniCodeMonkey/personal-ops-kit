package entries

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func day(d int) time.Time {
	return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC)
}

// writeLines writes raw lines (already JSON or deliberately broken) to a file.
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func entryLine(t *testing.T, e Entry) string {
	t.Helper()
	b, err := jsonMarshal(e) // helper below
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mk(job, kind, id, status, title string, ts time.Time) Entry {
	return Entry{TS: ts, Job: job, Kind: kind, ID: id, Status: status, Title: title}
}

func TestFoldLatestWinsPerJobKindID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repo-activity", "state", "entries.jsonl")
	writeLines(t, path,
		entryLine(t, mk("repo-activity", "stranded-branch", "x#main", "open", "day 1", day(1))),
		entryLine(t, mk("repo-activity", "stranded-branch", "x#main", "open", "day 3", day(3))),
		// Same natural id under a different kind must NOT collide:
		entryLine(t, mk("repo-activity", "shipped", "x#main", "event", "shipped stuff", day(2))),
	)
	res, err := Fold([]string{path}, FoldOptions{}, day(5))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("want 2 folded items, got %d: %+v", len(res.Items), res.Items)
	}
	var branch *Folded
	for i := range res.Items {
		if res.Items[i].Kind == "stranded-branch" {
			branch = &res.Items[i]
		}
	}
	if branch == nil || branch.Title != "day 3" {
		t.Fatalf("latest-wins failed: %+v", res.Items)
	}
}

func TestFoldEpisodeAging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	writeLines(t, path,
		entryLine(t, mk("j", "k", "a", "open", "first episode", day(1))),
		entryLine(t, mk("j", "k", "a", "closed", "fixed", day(4))),
		entryLine(t, mk("j", "k", "a", "open", "re-stranded", day(10))),
	)
	res, err := Fold([]string{path}, FoldOptions{OpenOnly: true}, day(12))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 open item, got %d", len(res.Items))
	}
	got := res.Items[0]
	if !got.FirstSeen.Equal(day(10)) {
		t.Fatalf("first_seen must restart after closed: got %v, want %v", got.FirstSeen, day(10))
	}
	if got.AgeDays != 2 {
		t.Fatalf("age_days: got %d, want 2", got.AgeDays)
	}
}

func TestFoldFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	writeLines(t, path,
		entryLine(t, mk("j", "k", "old-event", "event", "old", day(1))),
		entryLine(t, mk("j", "k", "new-event", "event", "new", day(10))),
		entryLine(t, mk("j", "k", "loop", "open", "open thing", day(2))),
		entryLine(t, mk("other", "k", "loop2", "open", "other job", day(2))),
	)
	// Since keeps only keys whose latest ts >= since.
	res, _ := Fold([]string{path}, FoldOptions{Since: day(9)}, day(12))
	if len(res.Items) != 1 || res.Items[0].ID != "new-event" {
		t.Fatalf("since filter: %+v", res.Items)
	}
	// OpenOnly ignores Since and drops events/closed.
	res, _ = Fold([]string{path}, FoldOptions{OpenOnly: true, Since: day(9)}, day(12))
	if len(res.Items) != 2 {
		t.Fatalf("open-only: want 2 open loops, got %+v", res.Items)
	}
	// Job filter.
	res, _ = Fold([]string{path}, FoldOptions{Job: "other"}, day(12))
	if len(res.Items) != 1 || res.Items[0].Job != "other" {
		t.Fatalf("job filter: %+v", res.Items)
	}
}

func TestFoldToleratesMalformedAndDuplicateLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	dup := entryLine(t, mk("j", "k", "a", "open", "dup", day(1)))
	writeLines(t, path,
		dup,
		"{this is not json",
		"",
		dup, // identical duplicate: folds away
	)
	res, err := Fold([]string{path}, FoldOptions{}, day(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	if res.Skipped != 2 {
		t.Fatalf("want 2 skipped lines (bad json + blank), got %d", res.Skipped)
	}
}

func TestFoldToleratesOversizedLine(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "entries.jsonl")
	goodPath := filepath.Join(t.TempDir(), "entries.jsonl")
	// Raw garbage line far larger than bufio.Scanner's old 1MB token cap.
	huge := strings.Repeat("x", 2*1024*1024)
	writeLines(t, badPath, huge)
	writeLines(t, goodPath,
		entryLine(t, mk("j", "k", "a", "open", "healthy", day(1))),
		entryLine(t, mk("j", "k", "b", "event", "also healthy", day(2))),
	)
	res, err := Fold([]string{badPath, goodPath}, FoldOptions{}, day(3))
	if err != nil {
		t.Fatalf("oversized line must not abort the fold: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("healthy file's items must all fold: got %d: %+v", len(res.Items), res.Items)
	}
	if res.Skipped < 1 {
		t.Fatalf("want at least 1 skipped for the oversized line, got %d", res.Skipped)
	}
}

func TestFoldResultItemsEncodesAsEmptyArrayNotNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	writeLines(t, path,
		entryLine(t, mk("j", "k", "a", "open", "hi", day(1))))
	res, err := Fold([]string{path}, FoldOptions{Job: "nope-no-such-job"}, day(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("want 0 items, got %d", len(res.Items))
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"items":null`) {
		t.Fatalf("items must encode as [], not null: %s", b)
	}
}

func TestStatePathsGlobsArtifactsRoot(t *testing.T) {
	root := t.TempDir()
	for _, j := range []string{"a-job", "b-job"} {
		writeLines(t, filepath.Join(root, j, "state", "entries.jsonl"),
			entryLine(t, mk(j, "k", "x", "event", "hi", day(1))))
	}
	// A job dir without entries must simply not appear.
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := StatePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 paths, got %v", paths)
	}
}

func jsonMarshal(e Entry) ([]byte, error) {
	return json.Marshal(e)
}
