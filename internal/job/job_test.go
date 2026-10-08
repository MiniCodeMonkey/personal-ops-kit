package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJob(t *testing.T, root, name, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.env"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	dir := writeJob(t, root, "garden-log", `
# Daily auction watcher.
JOB_NAME="garden-log"
JOB_DESCRIPTION="Garden notes, matched against your plan"
SCHEDULE="0 8 * * *"
JOB_GATED=0                  # cheap, so it always runs
JOB_MODEL="claude-sonnet-5"
JOB_TIMEOUT=900
CATCHUP_WINDOW="12h"
CLAUDE_PROFILE_DIR="$HOME/.claude"
ARTIFACT_GLOB="digests/*.md"
CLAUDE_PROJECT_DIR="$HOME/projects/garden"
`)

	j, err := Load(dir, filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "garden-log" || j.Gated || j.Model != "claude-sonnet-5" {
		t.Errorf("unexpected job: %+v", j)
	}
	if j.Timeout != 15*time.Minute {
		t.Errorf("Timeout = %v, want 15m", j.Timeout)
	}
	if j.CatchupWindow != 12*time.Hour {
		t.Errorf("CatchupWindow = %v, want 12h", j.CatchupWindow)
	}
	if !j.Schedule.Matches(time.Date(2026, 8, 12, 8, 0, 0, 0, time.Local)) {
		t.Error("schedule did not parse to 08:00 daily")
	}
	// A quoted value containing '#' must survive; a trailing comment must not.
	if strings.Contains(j.Description, "#") {
		t.Errorf("description picked up a comment: %q", j.Description)
	}
	if !strings.HasPrefix(j.ClaudeProfileDir, "/") {
		t.Errorf("$HOME was not expanded: %q", j.ClaudeProfileDir)
	}
	if !strings.HasSuffix(j.ProjectDir, "/projects/garden") || strings.Contains(j.ProjectDir, "$") {
		t.Errorf("ProjectDir = %q", j.ProjectDir)
	}
}

func TestDefaultsWhenOmitted(t *testing.T) {
	root := t.TempDir()
	dir := writeJob(t, root, "minimal", "JOB_NAME=\"minimal\"\nSCHEDULE=\"0 9 * * 1\"\n")
	j, err := Load(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if j.Timeout != DefaultTimeout || j.CatchupWindow != DefaultCatchupWindow {
		t.Errorf("defaults not applied: timeout=%v window=%v", j.Timeout, j.CatchupWindow)
	}
	if j.ArtifactGlob != DefaultArtifactGlob {
		t.Errorf("ArtifactGlob = %q, want %q", j.ArtifactGlob, DefaultArtifactGlob)
	}
	if j.Gated {
		t.Error("a job should not be gated unless it asks to be")
	}
}

// A name that disagrees with its directory would schedule under one identity and
// write artifacts under another, which is confusing in exactly the way that takes an
// hour to spot.
func TestNameMustMatchDirectory(t *testing.T) {
	root := t.TempDir()
	dir := writeJob(t, root, "actual", "JOB_NAME=\"different\"\nSCHEDULE=\"0 8 * * *\"\n")
	if _, err := Load(dir, root); err == nil {
		t.Fatal("mismatched JOB_NAME was accepted")
	}
}

func TestBadManifestsAreRejected(t *testing.T) {
	root := t.TempDir()
	cases := map[string]string{
		"missing schedule": "JOB_NAME=\"a\"\n",
		"bad cron":         "JOB_NAME=\"a\"\nSCHEDULE=\"0 99 * * *\"\n",
		"bad timeout":      "JOB_NAME=\"a\"\nSCHEDULE=\"0 8 * * *\"\nJOB_TIMEOUT=\"soon\"\n",
		"bad window":       "JOB_NAME=\"a\"\nSCHEDULE=\"0 8 * * *\"\nCATCHUP_WINDOW=\"ages\"\n",
		"not KEY=value":    "JOB_NAME=\"a\"\nSCHEDULE=\"0 8 * * *\"\nthis is not a setting\n",
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeJob(t, t.TempDir(), "a", manifest)
			if _, err := Load(dir, root); err == nil {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

func TestMissingRunScriptIsRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "norun")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "job.env"), []byte("JOB_NAME=\"norun\"\nSCHEDULE=\"0 8 * * *\"\n"), 0o644)
	if _, err := Load(dir, root); err == nil {
		t.Fatal("a job with no run.sh was accepted")
	}
}

// One broken manifest must not take the working jobs down with it.
func TestLoadAllIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	writeJob(t, root, "good-a", "JOB_NAME=\"good-a\"\nSCHEDULE=\"0 8 * * *\"\n")
	writeJob(t, root, "broken", "JOB_NAME=\"broken\"\nSCHEDULE=\"nonsense\"\n")
	writeJob(t, root, "good-b", "JOB_NAME=\"good-b\"\nSCHEDULE=\"47 18 * * *\"\n")

	jobs, problems := LoadAll(root, filepath.Join(root, "artifacts"))
	if len(jobs) != 2 {
		t.Fatalf("loaded %d jobs, want the 2 good ones", len(jobs))
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(problems))
	}
	if !strings.Contains(problems[0].Error(), "broken") {
		t.Errorf("the error does not name the offending job: %v", problems[0])
	}
	if jobs[0].Name != "good-a" || jobs[1].Name != "good-b" {
		t.Errorf("jobs not sorted by name: %v, %v", jobs[0].Name, jobs[1].Name)
	}
}

func TestPathsHangOffTheArtifactRoot(t *testing.T) {
	root := t.TempDir()
	dir := writeJob(t, root, "j", "JOB_NAME=\"j\"\nSCHEDULE=\"0 8 * * *\"\n")
	j, err := Load(dir, "/tmp/ops")
	if err != nil {
		t.Fatal(err)
	}
	if j.LedgerPath() != "/tmp/ops/j/ledger.jsonl" {
		t.Errorf("LedgerPath = %q", j.LedgerPath())
	}
	if j.StateDir() != "/tmp/ops/j/state" {
		t.Errorf("StateDir = %q", j.StateDir())
	}
	if j.RunScript() != filepath.Join(dir, "run.sh") {
		t.Errorf("RunScript = %q", j.RunScript())
	}
}

// The built-in templates are what install copies into a new user's jobs folder,
// so every one of them must load cleanly.
func TestTemplatesLoad(t *testing.T) {
	jobs, problems := LoadAll("../../templates", t.TempDir())
	for _, p := range problems {
		t.Error(p)
	}
	if len(jobs) != 4 {
		t.Errorf("loaded %d templates, want 4", len(jobs))
	}
	for _, j := range jobs {
		if j.Description == "" || j.Schedule == nil {
			t.Errorf("%s: missing description or schedule", j.Name)
		}
	}
}
