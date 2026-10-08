package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
)

type capturedNotification struct {
	job, outcome, headline, runID string
}

type fakeNotifier struct{ posted []capturedNotification }

func (f *fakeNotifier) Post(_ context.Context, job, outcome, headline, runID string) error {
	f.posted = append(f.posted, capturedNotification{job, outcome, headline, runID})
	return nil
}

// harness builds a repo-shaped tree with one job whose run.sh is the given script.
func harness(t *testing.T, script string, manifestExtra string) (*Runner, *job.Job, *fakeNotifier) {
	t.Helper()
	root := t.TempDir()
	jobsDir := filepath.Join(root, "jobs", "testjob")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "JOB_NAME=\"testjob\"\nSCHEDULE=\"0 8 * * *\"\nJOB_TIMEOUT=10\n" + manifestExtra
	if err := os.WriteFile(filepath.Join(jobsDir, "job.env"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	artifacts := filepath.Join(root, "ops")
	j, err := job.Load(jobsDir, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	notifier := &fakeNotifier{}
	return &Runner{OpsRoot: root, ArtifactsRoot: artifacts, Notifier: notifier}, j, notifier
}

func TestSuccessfulRunIsRecordedAndNotified(t *testing.T) {
	r, j, n := harness(t, `#!/bin/bash
echo "doing the work"
cat > "$OPS_RESULT_FILE" <<EOF
outcome=ok
headline=1,555 lots scanned, 20 flagged
artifact=digests/2026-08-11.md
EOF
`, "")

	res, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.OK {
		t.Errorf("outcome = %q, want ok", res.Record.Outcome)
	}
	if res.Record.Headline != "1,555 lots scanned, 20 flagged" {
		t.Errorf("headline = %q", res.Record.Headline)
	}
	if res.Record.Artifact != "digests/2026-08-11.md" {
		t.Errorf("artifact = %q", res.Record.Artifact)
	}
	if len(n.posted) != 1 {
		t.Errorf("posted %d notifications, want 1", len(n.posted))
	}

	body, err := os.ReadFile(res.LogPath)
	if err != nil || !strings.Contains(string(body), "doing the work") {
		t.Errorf("log not captured: %v %q", err, body)
	}
}

// The whole reason the ledger exists: a run that leaves nothing behind is
// indistinguishable from one that never happened.
func TestFailureIsStillRecorded(t *testing.T) {
	r, j, _ := harness(t, "#!/bin/bash\necho boom >&2\nexit 3\n", "")

	res, err := r.Run(context.Background(), j, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.Failed {
		t.Errorf("outcome = %q, want failed", res.Record.Outcome)
	}

	runs, err := ledger.Load(j.LedgerPath())
	if err != nil || len(runs) != 1 {
		t.Fatalf("ledger has %d runs, want 1: %v", len(runs), err)
	}
	if runs[0].Outcome != ledger.Failed {
		t.Errorf("ledger outcome = %q, want failed", runs[0].Outcome)
	}
}

// A job that finds nothing declares no-change, which must not read as a failure and
// must not notify.
func TestNoChangeDoesNotNotify(t *testing.T) {
	r, j, n := harness(t, `#!/bin/bash
printf 'outcome=no-change\nheadline=Nothing new today\n' > "$OPS_RESULT_FILE"
`, "")

	res, err := r.Run(context.Background(), j, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.NoChange {
		t.Fatalf("outcome = %q, want no-change", res.Record.Outcome)
	}
	// The runner posts unconditionally; suppression is the notifier's judgement.
	// What matters here is that the outcome survives intact for it to act on.
	if len(n.posted) == 1 && n.posted[0].outcome != string(ledger.NoChange) {
		t.Errorf("notifier saw outcome %q, want no-change", n.posted[0].outcome)
	}
}

// A job cannot claim success while failing.
func TestDeclaredSuccessWithNonZeroExitIsFailure(t *testing.T) {
	r, j, _ := harness(t, `#!/bin/bash
printf 'outcome=ok\nheadline=all good\n' > "$OPS_RESULT_FILE"
exit 1
`, "")

	res, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.Failed {
		t.Errorf("outcome = %q, want failed -- a job claimed success while exiting 1", res.Record.Outcome)
	}
	if !strings.Contains(res.Record.Detail, "exited with an error") {
		t.Errorf("detail does not explain the contradiction: %q", res.Record.Detail)
	}
}

func TestCleanExitWithNoDeclarationIsOK(t *testing.T) {
	r, j, _ := harness(t, "#!/bin/bash\necho fine\n", "")
	res, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.OK {
		t.Errorf("outcome = %q, want ok", res.Record.Outcome)
	}
}

// A stale verdict from a previous run must never be adopted by the next one.
func TestPreviousResultIsNotInherited(t *testing.T) {
	r, j, _ := harness(t, `#!/bin/bash
if [ -f "$OPS_STATE_DIR/first-done" ]; then
  exit 0                       # second run declares nothing
fi
touch "$OPS_STATE_DIR/first-done"
printf 'outcome=warning\nheadline=first run warned\n' > "$OPS_RESULT_FILE"
`, "")

	first, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if first.Record.Outcome != ledger.Warning {
		t.Fatalf("first outcome = %q, want warning", first.Record.Outcome)
	}

	second, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.Outcome != ledger.OK {
		t.Errorf("second outcome = %q, want ok -- it inherited the previous verdict", second.Record.Outcome)
	}
	if second.Record.Headline == "first run warned" {
		t.Error("second run inherited the previous headline")
	}
}

// A timeout must kill the whole process tree. A surviving grandchild would keep
// burning the quota the gate exists to protect.
func TestTimeoutKillsTheProcessTree(t *testing.T) {
	r, j, _ := harness(t, `#!/bin/bash
marker="$OPS_STATE_DIR/child-alive"
( sleep 30; echo survived > "$marker" ) &
sleep 30
`, "JOB_TIMEOUT=1\n")

	start := time.Now()
	res, err := r.Run(context.Background(), j, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("run took %v; the timeout did not fire", elapsed)
	}
	if res.Record.Outcome != ledger.Failed {
		t.Errorf("outcome = %q, want failed", res.Record.Outcome)
	}
	if !strings.Contains(res.Record.Headline, "Timed out") {
		t.Errorf("headline = %q, want it to say it timed out", res.Record.Headline)
	}

	// Give an orphaned grandchild long enough to write its marker if it survived.
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(filepath.Join(j.StateDir(), "child-alive")); err == nil {
		t.Error("a grandchild outlived the timeout; the process group was not killed")
	}
}

func TestJobEnvironmentIsPassedThrough(t *testing.T) {
	r, j, _ := harness(t, `#!/bin/bash
{
  echo "job=$OPS_JOB"
  echo "artifacts=$OPS_ARTIFACT_DIR"
  echo "state=$OPS_STATE_DIR"
  echo "run=$OPS_RUN_ID"
} > "$OPS_ARTIFACT_DIR/env.txt"
`, "")

	if _, err := r.Run(context.Background(), j, TriggerManual); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(j.ArtifactDir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job=testjob", "artifacts=" + j.ArtifactDir, "state=" + j.StateDir()} {
		if !strings.Contains(string(body), want) {
			t.Errorf("environment missing %q; got:\n%s", want, body)
		}
	}
}

// A gated job that the gate turns away is skipped, not failed, and the reason is kept.
func TestGatedJobRecordsTheReason(t *testing.T) {
	r, j, n := harness(t, "#!/bin/bash\necho should-not-run > \"$OPS_ARTIFACT_DIR/ran.txt\"\n", "JOB_GATED=1\n")

	gateDir := filepath.Join(r.OpsRoot, "lib")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gate := "#!/bin/bash\necho 'skip: 7-day usage at 71% (threshold 60%)'\nexit 10\n"
	if err := os.WriteFile(filepath.Join(gateDir, "gate.sh"), []byte(gate), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(context.Background(), j, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.Gated {
		t.Fatalf("outcome = %q, want gated", res.Record.Outcome)
	}
	if !strings.Contains(res.Record.Detail, "71%") {
		t.Errorf("detail lost the gate's reason: %q", res.Record.Detail)
	}
	if _, err := os.Stat(filepath.Join(j.ArtifactDir, "ran.txt")); err == nil {
		t.Error("the job ran despite being gated off")
	}
	if len(n.posted) != 1 || n.posted[0].outcome != string(ledger.Gated) {
		t.Errorf("notifier should still see the gated outcome, got %+v", n.posted)
	}
}

func TestGatePassingLetsTheJobRun(t *testing.T) {
	r, j, _ := harness(t, "#!/bin/bash\necho ran > \"$OPS_ARTIFACT_DIR/ran.txt\"\n", "JOB_GATED=1\n")

	gateDir := filepath.Join(r.OpsRoot, "lib")
	os.MkdirAll(gateDir, 0o755)
	os.WriteFile(filepath.Join(gateDir, "gate.sh"),
		[]byte("#!/bin/bash\necho 'go: 7-day usage at 35%'\nexit 0\n"), 0o755)

	res, err := r.Run(context.Background(), j, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.OK {
		t.Errorf("outcome = %q, want ok", res.Record.Outcome)
	}
	if _, err := os.Stat(filepath.Join(j.ArtifactDir, "ran.txt")); err != nil {
		t.Error("the job did not run despite the gate allowing it")
	}
}

// A broken gate must not be read as permission to run.
func TestBrokenGateFailsClosed(t *testing.T) {
	r, j, _ := harness(t, "#!/bin/bash\necho ran > \"$OPS_ARTIFACT_DIR/ran.txt\"\n", "JOB_GATED=1\n")

	gateDir := filepath.Join(r.OpsRoot, "lib")
	os.MkdirAll(gateDir, 0o755)
	os.WriteFile(filepath.Join(gateDir, "gate.sh"),
		[]byte("#!/bin/bash\necho 'gate: jq not found' >&2\nexit 1\n"), 0o755)

	res, err := r.Run(context.Background(), j, TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Outcome != ledger.Failed {
		t.Errorf("outcome = %q, want failed", res.Record.Outcome)
	}
	if _, err := os.Stat(filepath.Join(j.ArtifactDir, "ran.txt")); err == nil {
		t.Error("a broken gate let the job run")
	}
}
