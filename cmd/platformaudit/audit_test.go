package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
)

// fixedNow pins "now" for every test that cares about month labels: a run
// on 2026-09-01 09:00 reports August 2026.
var fixedNow = time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)

// writeLedger appends n run records for job, evenly spread starting at
// startedAt and one minute apart, all with the given outcome and a fixed
// 5-second duration. Returns the path.
func writeLedger(t *testing.T, opsDir, job string, recs []ledger.Record) string {
	t.Helper()
	path := filepath.Join(opsDir, job, "ledger.jsonl")
	for _, r := range recs {
		r.Job = job
		if err := ledger.AppendRun(path, r); err != nil {
			t.Fatalf("AppendRun: %v", err)
		}
	}
	return path
}

func appendRawLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func runRecords(n int, start time.Time, step time.Duration, outcome ledger.Outcome, dur time.Duration) []ledger.Record {
	recs := make([]ledger.Record, n)
	for i := 0; i < n; i++ {
		s := start.Add(time.Duration(i) * step)
		recs[i] = ledger.Record{
			RunID:      s.Format(time.RFC3339Nano),
			StartedAt:  s,
			FinishedAt: s.Add(dur),
			Outcome:    outcome,
		}
	}
	return recs
}

func TestCollectStats_WindowBoundary(t *testing.T) {
	opsDir := t.TempDir()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	curStart := now.AddDate(0, 0, -30)

	path := filepath.Join(opsDir, "alpha", "ledger.jsonl")
	// Exactly at curStart: current window (inclusive lower bound).
	if err := ledger.AppendRun(path, ledger.Record{Job: "alpha", RunID: "at-boundary", StartedAt: curStart, FinishedAt: curStart.Add(time.Second), Outcome: ledger.OK}); err != nil {
		t.Fatal(err)
	}
	// One nanosecond before curStart: previous window.
	before := curStart.Add(-time.Nanosecond)
	if err := ledger.AppendRun(path, ledger.Record{Job: "alpha", RunID: "before-boundary", StartedAt: before, FinishedAt: before.Add(time.Second), Outcome: ledger.OK}); err != nil {
		t.Fatal(err)
	}

	cur, prev, skipped, err := collectStats(opsDir, now)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if cur["alpha"].Runs != 1 {
		t.Errorf("cur runs = %d, want 1", cur["alpha"].Runs)
	}
	if prev["alpha"].Runs != 1 {
		t.Errorf("prev runs = %d, want 1", prev["alpha"].Runs)
	}
}

// TestCollectStats_LegacyDateOnlyLineCounted mirrors a real fixture: three
// pre-runner-package lines in ~/ops/nightly-research/ledger.jsonl carry only
// date/lane/topic/summary/memo, no type and no started_at. ledger.Load
// (internal/ledger/ledger.go lines 168-192) treats these as runs regardless
// of StartedAt and derives StartedAt from Date when StartedAt is zero;
// collectStats must mirror that exactly instead of dropping the line
// silently and uncounted.
func TestCollectStats_LegacyDateOnlyLineCounted(t *testing.T) {
	opsDir := t.TempDir()
	path := filepath.Join(opsDir, "nightly-research", "ledger.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Within the current window (fixedNow=2026-09-01 09:00, curStart=2026-08-02 09:00).
	appendRawLine(t, path, `{"date":"2026-08-15","lane":"research","topic":"nightly digest","summary":"","memo":"2026-08-15.md"}`)

	cur, _, skipped, err := collectStats(opsDir, fixedNow)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 (legacy line is valid JSON, not malformed)", skipped)
	}
	js, ok := cur["nightly-research"]
	if !ok || js.Runs != 1 {
		t.Fatalf("legacy date-only line must be counted as a run, got cur=%+v", cur)
	}
	if js.OK != 1 {
		t.Errorf("legacy line with empty outcome should default to OK, got %+v", js)
	}
}

func TestCollectStats_MalformedLineCounted(t *testing.T) {
	opsDir := t.TempDir()
	path := writeLedger(t, opsDir, "alpha", runRecords(2, fixedNow.AddDate(0, 0, -1), time.Hour, ledger.OK, 5*time.Second))
	appendRawLine(t, path, `{not valid json`)
	appendRawLine(t, path, `   `) // blank after trim, not malformed

	cur, _, skipped, err := collectStats(opsDir, fixedNow)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if cur["alpha"].Runs != 2 {
		t.Errorf("runs = %d, want 2", cur["alpha"].Runs)
	}
}

func TestCollectStats_DaemonDirSkipped(t *testing.T) {
	opsDir := t.TempDir()
	writeLedger(t, opsDir, "daemon", runRecords(3, fixedNow.AddDate(0, 0, -1), time.Hour, ledger.OK, time.Second))
	writeLedger(t, opsDir, "alpha", runRecords(1, fixedNow.AddDate(0, 0, -1), time.Hour, ledger.OK, time.Second))

	cur, _, _, err := collectStats(opsDir, fixedNow)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if _, ok := cur["daemon"]; ok {
		t.Errorf("daemon dir should be skipped, got %+v", cur["daemon"])
	}
	if cur["alpha"].Runs != 1 {
		t.Errorf("alpha runs = %d, want 1", cur["alpha"].Runs)
	}
}

func TestCollectStats_DirWithoutLedgerSilentlySkipped(t *testing.T) {
	opsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(opsDir, "empty-job"), 0o755); err != nil {
		t.Fatal(err)
	}
	cur, prev, skipped, err := collectStats(opsDir, fixedNow)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if len(cur) != 0 || len(prev) != 0 || skipped != 0 {
		t.Errorf("expected nothing, got cur=%v prev=%v skipped=%d", cur, prev, skipped)
	}
}

func TestComputeFlags_AlwaysQuietEdges(t *testing.T) {
	t.Run("19 runs no flag", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 19, NoChange: 19}}
		if flags := computeFlags(cur); len(flags) != 0 {
			t.Errorf("19 runs should not flag, got %+v", flags)
		}
	})
	t.Run("20 runs all no-change flags", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 20, NoChange: 20}}
		flags := computeFlags(cur)
		if len(flags) != 1 || flags[0].Kind != "always-quiet" {
			t.Fatalf("20 runs all no-change should flag, got %+v", flags)
		}
		if flags[0].id() != "platform-flag#alpha#always-quiet" {
			t.Errorf("id = %q", flags[0].id())
		}
		want := "alpha: 100% quiet over 20 runs -- slow it down or kill it"
		if flags[0].title() != want {
			t.Errorf("title = %q, want %q", flags[0].title(), want)
		}
	})
	t.Run("20 runs one ok no flag", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 20, NoChange: 19, OK: 1}}
		if flags := computeFlags(cur); len(flags) != 0 {
			t.Errorf("one ok among 20 should not flag, got %+v", flags)
		}
	})
}

func TestComputeFlags_FailingEdges(t *testing.T) {
	t.Run("5 runs 20% no flag", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 5, Failed: 1, OK: 4}}
		if flags := computeFlags(cur); len(flags) != 0 {
			t.Errorf("exactly 20%% should not flag (strict >), got %+v", flags)
		}
	})
	t.Run("5 runs 40% flags", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 5, Failed: 2, OK: 3}}
		flags := computeFlags(cur)
		if len(flags) != 1 || flags[0].Kind != "failing" || flags[0].Pct != 40 {
			t.Fatalf("40%% of 5 should flag at 40%%, got %+v", flags)
		}
		want := "alpha: 40% failed over 5 runs"
		if flags[0].title() != want {
			t.Errorf("title = %q, want %q", flags[0].title(), want)
		}
	})
	t.Run("4 runs never flags regardless of failure rate", func(t *testing.T) {
		cur := map[string]jobStats{"alpha": {Job: "alpha", Runs: 4, Failed: 4}}
		if flags := computeFlags(cur); len(flags) != 0 {
			t.Errorf("under min runs should not flag, got %+v", flags)
		}
	})
}

func TestMedianAndDrift(t *testing.T) {
	opsDir := t.TempDir()
	// Current window: durations 4, 6, 8 (median 6).
	cur := runRecords(3, fixedNow.AddDate(0, 0, -1), time.Hour, ledger.OK, 0)
	cur[0].FinishedAt = cur[0].StartedAt.Add(4 * time.Second)
	cur[1].FinishedAt = cur[1].StartedAt.Add(6 * time.Second)
	cur[2].FinishedAt = cur[2].StartedAt.Add(8 * time.Second)
	writeLedger(t, opsDir, "alpha", cur)

	// Previous window: durations 10, 20 (median 15).
	prevRecs := runRecords(2, fixedNow.AddDate(0, 0, -40), time.Hour, ledger.OK, 0)
	prevRecs[0].FinishedAt = prevRecs[0].StartedAt.Add(10 * time.Second)
	prevRecs[1].FinishedAt = prevRecs[1].StartedAt.Add(20 * time.Second)
	writeLedger(t, opsDir, "alpha", prevRecs)

	curStats, prevStats, _, err := collectStats(opsDir, fixedNow)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if got := curStats["alpha"].MedianSecs; got != 6 {
		t.Errorf("cur median = %v, want 6", got)
	}
	if got := curStats["alpha"].PrevMedianSecs; got != 15 {
		t.Errorf("cur.PrevMedianSecs = %v, want 15", got)
	}
	if got := prevStats["alpha"].MedianSecs; got != 15 {
		t.Errorf("prev median = %v, want 15", got)
	}
}

func TestFlagCloseOnClearAndForeignKindsUntouched(t *testing.T) {
	cur := map[string]jobStats{
		"alpha": {Job: "alpha", Runs: 20, OK: 20}, // no longer always-quiet
	}
	open := []openItem{
		{Kind: "platform-flag", ID: "platform-flag#alpha#always-quiet"},
		{Kind: "repeated-correction", ID: "repeated-correction#x"},
	}
	flags := computeFlags(cur)
	cands := buildCandidates(flags, open, cur, "2026-08")

	var closed, foreignTouched bool
	for _, c := range cands {
		if c.Kind == "platform-flag" && c.ID == "platform-flag#alpha#always-quiet" && c.Status == "closed" {
			closed = true
			if c.Title != "resolved" || c.Data["reason"] != "gone" {
				t.Errorf("unexpected close candidate: %+v", c)
			}
		}
		if c.ID == "repeated-correction#x" {
			foreignTouched = true
		}
	}
	if !closed {
		t.Errorf("expected close for cleared flag, got %+v", cands)
	}
	if foreignTouched {
		t.Errorf("foreign kind must never be touched, got %+v", cands)
	}
}

func TestMonthlyEventIDAndTitle(t *testing.T) {
	month := monthLabel(fixedNow)
	if month != "2026-08" {
		t.Fatalf("month = %q, want 2026-08", month)
	}

	cur := map[string]jobStats{
		"alpha": {Job: "alpha", Runs: 10, NoChange: 5, OK: 5},
	}
	flags := computeFlags(cur) // none, under always-quiet threshold
	cands := buildCandidates(flags, nil, cur, month)

	var event *candidate
	for i := range cands {
		if cands[i].Kind == "platform-audit" {
			event = &cands[i]
		}
	}
	if event == nil {
		t.Fatalf("expected a platform-audit event, got %+v", cands)
	}
	if event.ID != "platform-audit#2026-08" {
		t.Errorf("id = %q", event.ID)
	}
	if event.Status != "event" {
		t.Errorf("status = %q, want event", event.Status)
	}
	want := "Aug: 10 runs, 50% quiet, no flags"
	if event.Title != want {
		t.Errorf("title = %q, want %q", event.Title, want)
	}
}

func TestBuildMemo_Deterministic(t *testing.T) {
	cur := map[string]jobStats{
		"alpha": {
			Job: "alpha", Runs: 2, OK: 1, NoChange: 1,
			MedianSecs: 10, PrevMedianSecs: 5,
			NotifyPerWeek: float64(1) / (30.0 / 7.0),
		},
	}
	prev := map[string]jobStats{
		"alpha": {Job: "alpha", Runs: 1, MedianSecs: 5},
	}

	got := buildMemo(cur, prev, nil, "2026-08", 0)
	want := "Platform Audit -- August 2026\n\n" +
		"Flags:\n" +
		"No flags.\n\n" +
		"Outcomes per job:\n" +
		"alpha: runs=2 ok=1 no-change=1 warning=0 gated=0 failed=0\n\n" +
		"Notification volume (per week):\n" +
		"alpha: 0.23/week\n\n" +
		"Run duration (median seconds):\n" +
		"alpha: current=10.0 previous=5.0 drift=+5.0\n\n" +
		"Malformed lines skipped: 0\n"

	if got != want {
		t.Errorf("memo mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildMemo_NoDataDrift(t *testing.T) {
	cur := map[string]jobStats{
		"beta": {Job: "beta", Runs: 1, OK: 1, MedianSecs: 3, PrevMedianSecs: 0},
	}
	got := buildMemo(cur, map[string]jobStats{}, nil, "2026-08", 2)
	if !strings.Contains(got, "beta: current=3.0 previous=0.0 drift=–\n") {
		t.Errorf("expected a – drift with no previous data, got:\n%s", got)
	}
	if !strings.Contains(got, "Malformed lines skipped: 2\n") {
		t.Errorf("expected skipped count in memo, got:\n%s", got)
	}
}

func TestRunMain_CLIEndToEnd(t *testing.T) {
	opsDir := t.TempDir()
	// alpha: 20 no-change runs -> always-quiet flag.
	writeLedger(t, opsDir, "alpha", runRecords(20, fixedNow.AddDate(0, 0, -1), -time.Hour, ledger.NoChange, time.Second))
	// beta: 5 runs, 2 failed -> failing flag.
	betaRecs := append(
		runRecords(3, fixedNow.AddDate(0, 0, -2), -time.Hour, ledger.OK, time.Second),
		runRecords(2, fixedNow.AddDate(0, 0, -3), -time.Hour, ledger.Failed, time.Second)...,
	)
	writeLedger(t, opsDir, "beta", betaRecs)

	openPath := filepath.Join(t.TempDir(), "open.json")
	openJSON := `{"items":[{"kind":"platform-flag","id":"platform-flag#gamma#failing","status":"open"}]}`
	if err := os.WriteFile(openPath, []byte(openJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "memo.txt")

	var stdout, stderr bytes.Buffer
	if err := runMain(opsDir, openPath, outPath, fixedNow, &stdout, &stderr); err != nil {
		t.Fatalf("runMain: %v (stderr: %s)", err, stderr.String())
	}

	memo, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading memo: %v", err)
	}
	if !strings.Contains(string(memo), "alpha: 100% quiet over 20 runs") {
		t.Errorf("memo missing always-quiet flag:\n%s", memo)
	}

	var cands []candidate
	for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var c candidate
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		cands = append(cands, c)
	}

	var sawAlwaysQuiet, sawFailing, sawEvent, sawClose bool
	for _, c := range cands {
		switch {
		case c.ID == "platform-flag#alpha#always-quiet" && c.Status == "open":
			sawAlwaysQuiet = true
		case c.ID == "platform-flag#beta#failing" && c.Status == "open":
			sawFailing = true
		case c.Kind == "platform-audit" && c.ID == "platform-audit#2026-08":
			sawEvent = true
		case c.ID == "platform-flag#gamma#failing" && c.Status == "closed":
			sawClose = true
		}
	}
	if !sawAlwaysQuiet {
		t.Errorf("expected always-quiet open for alpha, got %+v", cands)
	}
	if !sawFailing {
		t.Errorf("expected failing open for beta, got %+v", cands)
	}
	if !sawEvent {
		t.Errorf("expected monthly event, got %+v", cands)
	}
	if !sawClose {
		t.Errorf("expected close for gone gamma flag, got %+v", cands)
	}
}

func TestRunMain_RequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runMain("", "x", "y", fixedNow, &stdout, &stderr); err == nil {
		t.Errorf("empty -ops should error")
	}
	if err := runMain(t.TempDir(), "x", "", fixedNow, &stdout, &stderr); err == nil {
		t.Errorf("empty -out should error")
	}
	if err := runMain(filepath.Join(t.TempDir(), "nope"), "x", "y", fixedNow, &stdout, &stderr); err == nil {
		t.Errorf("unreadable -ops should error")
	}
	if err := runMain(t.TempDir(), "", filepath.Join(t.TempDir(), "memo.txt"), fixedNow, &stdout, &stderr); err == nil {
		t.Errorf("empty -open should error")
	}
}

func TestRunMain_MissingOpenFileIsEmptySet(t *testing.T) {
	opsDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "memo.txt")
	var stdout, stderr bytes.Buffer
	if err := runMain(opsDir, filepath.Join(opsDir, "missing.json"), outPath, fixedNow, &stdout, &stderr); err != nil {
		t.Fatalf("missing -open file should not error: %v", err)
	}
}
