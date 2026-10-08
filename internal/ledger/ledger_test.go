package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempLedger(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ledger.jsonl")
}

func TestLoadMissingLedgerIsNotAnError(t *testing.T) {
	runs, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || runs != nil {
		t.Fatalf("Load(missing) = %v, %v; want nil, nil -- a job that has never run is normal", runs, err)
	}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	path := tempLedger(t)
	start := time.Now().Add(-5 * time.Minute).Truncate(time.Second)

	if err := AppendRun(path, Record{
		Job: "garden-log", RunID: "r1", StartedAt: start,
		FinishedAt: start.Add(3 * time.Minute), Outcome: OK,
		Headline: "1,555 lots · 20 flagged", Artifact: "digests/2026-08-11.md",
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Outcome != OK || runs[0].Headline != "1,555 lots · 20 flagged" {
		t.Errorf("round trip lost data: %+v", runs[0].Record)
	}
	if runs[0].Acknowledged() {
		t.Error("a fresh run should not be acknowledged")
	}
}

// Every outcome must be recordable, including the ones the old ledger could not
// express. A gated night leaving no trace is the bug this package exists to fix.
func TestEveryOutcomeIsRecorded(t *testing.T) {
	path := tempLedger(t)
	base := time.Now().Add(-24 * time.Hour)

	for i, o := range []Outcome{OK, Warning, NoChange, Gated, Failed} {
		if err := AppendRun(path, Record{
			Job: "j", RunID: string(o), StartedAt: base.Add(time.Duration(i) * time.Hour),
			Outcome: o,
		}); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 5 {
		t.Fatalf("got %d runs, want 5 -- an outcome was dropped", len(runs))
	}
	seen := map[Outcome]bool{}
	for _, r := range runs {
		seen[r.Outcome] = true
	}
	for _, o := range []Outcome{OK, Warning, NoChange, Gated, Failed} {
		if !seen[o] {
			t.Errorf("outcome %q did not survive the round trip", o)
		}
	}
}

func TestAcknowledgeFoldsIntoRun(t *testing.T) {
	path := tempLedger(t)
	if err := AppendRun(path, Record{Job: "j", RunID: "r1", StartedAt: time.Now(), Outcome: OK}); err != nil {
		t.Fatal(err)
	}

	if runs, _ := Load(path); len(Unacknowledged(runs)) != 1 {
		t.Fatal("run should start unacknowledged")
	}

	ackAt := time.Now().Truncate(time.Second)
	if err := Acknowledge(path, "j", "r1", ackAt); err != nil {
		t.Fatal(err)
	}

	runs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("acknowledging created a phantom run: got %d, want 1", len(runs))
	}
	if !runs[0].Acknowledged() {
		t.Error("acknowledgement did not fold into the run")
	}
	if len(Unacknowledged(runs)) != 0 {
		t.Error("run still counted as unacknowledged")
	}
}

func TestRemindFoldsIntoRun(t *testing.T) {
	path := tempLedger(t)
	if err := AppendRun(path, Record{Job: "j", RunID: "r1", StartedAt: time.Now(), Outcome: OK}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := Remind(path, "j", "r1", at); err != nil {
		t.Fatal(err)
	}
	runs, _ := Load(path)
	if runs[0].RemindedAt.IsZero() {
		t.Error("reminder did not fold into the run")
	}
	if runs[0].Acknowledged() {
		t.Error("being reminded is not the same as having been seen")
	}
}

func TestNewestFirst(t *testing.T) {
	path := tempLedger(t)
	base := time.Now().Add(-72 * time.Hour)
	for i, id := range []string{"old", "middle", "new"} {
		if err := AppendRun(path, Record{
			Job: "j", RunID: id, StartedAt: base.Add(time.Duration(i) * 24 * time.Hour), Outcome: OK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := Load(path)
	if runs[0].RunID != "new" {
		t.Errorf("newest run is %q, want \"new\"", runs[0].RunID)
	}
	last, ok := Last(runs)
	if !ok || last.RunID != "new" {
		t.Errorf("Last = %q, want \"new\"", last.RunID)
	}
}

// A partial write from a hard kill must cost one line, not the whole history.
func TestMalformedLineIsSkipped(t *testing.T) {
	path := tempLedger(t)
	if err := AppendRun(path, Record{Job: "j", RunID: "good", StartedAt: time.Now(), Outcome: OK}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"run","job":"j","run_id":"trunc`) // no closing brace or newline
	f.Close()

	runs, err := Load(path)
	if err != nil {
		t.Fatalf("one bad line made the whole ledger unreadable: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "good" {
		t.Errorf("got %d runs, want the 1 good one", len(runs))
	}
}

// The real research ledger predates this package: its lines have no type, no run id
// and no outcome. They must still load, or the dashboard opens with no history for
// the job that has the most.
func TestLegacyResearchLedgerStillLoads(t *testing.T) {
	path := tempLedger(t)
	legacy := `{"date":"2026-08-10","lane":"sourdough","topic":"Why the second rise stalls in a cold kitchen","summary":"Dough temperature, not starter strength, explains the stall.","memo":"/Users/me/research/2026-08-10-sourdough.html"}
{"date":"2026-08-10","lane":"ai-tooling","topic":"Prompt caching for long-running agents","summary":"Caching the system prompt halves the cost of a nightly run.","memo":"/Users/me/research/2026-08-10-ai-tooling.html","notified":true}
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	runs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d legacy runs, want 2", len(runs))
	}
	for _, r := range runs {
		if r.Outcome != OK {
			t.Errorf("legacy run %q: outcome %q, want it defaulted to ok", r.RunID, r.Outcome)
		}
		if r.Headline == "" {
			t.Errorf("legacy run %q lost its topic", r.RunID)
		}
		if r.Artifact == "" {
			t.Errorf("legacy run %q lost its memo link", r.RunID)
		}
		if r.RunID == "" {
			t.Error("legacy run got no synthesized id, so it can never be acknowledged")
		}
	}
}

func TestOutstandingIgnoresSupersededRuns(t *testing.T) {
	base := time.Date(2026, 8, 11, 21, 55, 0, 0, time.Local)
	runs := []Run{
		{Record: Record{Job: "j", RunID: "r3", StartedAt: base.Add(34 * time.Hour), Outcome: NoChange},
			AcknowledgedAt: base.Add(36 * time.Hour)},
		{Record: Record{Job: "j", RunID: "r2", StartedAt: base.Add(3 * time.Minute), Outcome: OK},
			AcknowledgedAt: base.Add(24 * time.Hour)},
		{Record: Record{Job: "j", RunID: "r1", StartedAt: base, Outcome: Failed}},
	}

	// r1 failed, but r2 and r3 ran after it and were both read. Nothing about r1 is
	// still outstanding, and it will never be acknowledged, so nagging about it would
	// go on until MaxAge.
	if got, ok := Outstanding(runs); ok {
		t.Errorf("Outstanding = %q, want none -- a superseded failure is still nagging", got.RunID)
	}
}

func TestOutstandingReturnsUnreadLatestRun(t *testing.T) {
	base := time.Date(2026, 8, 11, 21, 55, 0, 0, time.Local)
	runs := []Run{
		{Record: Record{Job: "j", RunID: "r2", StartedAt: base.Add(24 * time.Hour), Outcome: Failed}},
		{Record: Record{Job: "j", RunID: "r1", StartedAt: base, Outcome: Failed}},
	}

	// A newer failure replaces an older one rather than queueing behind it: one
	// reminder, about the run that still describes the job's state.
	got, ok := Outstanding(runs)
	if !ok {
		t.Fatal("Outstanding = none, want the unread latest run")
	}
	if got.RunID != "r2" {
		t.Errorf("Outstanding = %q, want r2", got.RunID)
	}
}

func TestOutstandingEmptyLedger(t *testing.T) {
	if _, ok := Outstanding(nil); ok {
		t.Error("Outstanding found a run in an empty ledger")
	}
}
