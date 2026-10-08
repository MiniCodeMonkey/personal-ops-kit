package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/entries"
)

func TestEntriesAppendInfersJobFromStateDir(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "repo-activity", "state")
	err := entriesAppendEntry(stateDir, []string{
		"--kind", "stranded-branch",
		"--id", "x#main",
		"--status", "open",
		"--title", "x main: stuff",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "entries.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var e entries.Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Job != "repo-activity" {
		t.Fatalf("job not inferred from state dir: %q", e.Job)
	}
	if e.TS.IsZero() {
		t.Fatal("ts not stamped")
	}
}

func TestEntriesAppendRefusesWithoutStateDir(t *testing.T) {
	err := entriesAppendEntry("", []string{
		"--kind", "k", "--id", "i", "--status", "open", "--title", "t",
	})
	if err == nil || !strings.Contains(err.Error(), "OPS_STATE_DIR") {
		t.Fatalf("want refusal mentioning OPS_STATE_DIR, got %v", err)
	}
}

func TestEntriesAppendDryRunWritesNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "j", "state")
	err := entriesAppendEntry(stateDir, []string{
		"--kind", "k", "--id", "i", "--status", "event", "--title", "t",
		"--dry-run",
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "entries.jsonl")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote a file")
	}
}

func TestEntriesFoldEndToEnd(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "some-job", "state")
	for _, a := range [][]string{
		{"--kind", "k", "--id", "a", "--status", "open", "--title", "loop a"},
		{"--kind", "k", "--id", "b", "--status", "event", "--title", "event b"},
	} {
		if err := entriesAppendEntry(stateDir, a); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cfg := Config{ArtifactsRoot: root}
	if err := entriesFold(cfg, []string{"--open-only"}, &out); err != nil {
		t.Fatalf("fold: %v", err)
	}
	var res entries.FoldResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("fold output is not JSON: %v\n%s", err, out.String())
	}
	if len(res.Items) != 1 || res.Items[0].ID != "a" {
		t.Fatalf("open-only fold: %+v", res.Items)
	}
}

func TestParseSince(t *testing.T) {
	if _, err := parseSince("7d"); err != nil {
		t.Fatalf("day suffix: %v", err)
	}
	if _, err := parseSince("36h"); err != nil {
		t.Fatalf("go duration: %v", err)
	}
	if _, err := parseSince("2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("rfc3339: %v", err)
	}
	if _, err := parseSince("whenever"); err == nil {
		t.Fatal("garbage accepted")
	}
}
