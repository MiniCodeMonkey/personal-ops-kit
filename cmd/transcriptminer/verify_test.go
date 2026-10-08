package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptedCorrectionMatchesMemoryIndexAndClaudeMd(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "projects", "-p", "memory", "MEMORY.md"),
		"- [Prefer uv over pip](prefer-uv.md) -- python installs use uv\n")
	if !adopted(home, "repeated-correction", "repeated-correction#prefer-uv") {
		t.Fatal("memory index match missed")
	}
	if adopted(home, "repeated-correction", "repeated-correction#never-force-push") {
		t.Fatal("absent slug reported adopted")
	}
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "Never force push. Ever.\n")
	if !adopted(home, "repeated-correction", "repeated-correction#never-force-push") {
		t.Fatal("CLAUDE.md match missed (case-insensitive, words split on hyphen)")
	}
}

func TestAdoptedSkillCandidateMatchesSkillsDir(t *testing.T) {
	home := t.TempDir()
	if adopted(home, "workflow-skill-candidate", "workflow-skill-candidate#release-cascade") {
		t.Fatal("absent skill reported adopted")
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "upload-release-cascade"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !adopted(home, "workflow-skill-candidate", "workflow-skill-candidate#release-cascade") {
		t.Fatal("skills dir glob match missed")
	}
}

func TestAdoptedUnknownKindNeverMatches(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "prefer uv over pip\n")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "prefer-uv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if adopted(home, "some-unknown-kind", "some-unknown-kind#prefer-uv") {
		t.Fatal("unknown kind reported adopted even though matching files exist")
	}
}

func TestAdoptedRejectsEmptyOrAllHyphenSlug(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "some unrelated content\n")
	if adopted(home, "repeated-correction", "repeated-correction") {
		t.Fatal("id with no '#' reported adopted")
	}
	if adopted(home, "repeated-correction", "repeated-correction#--") {
		t.Fatal("all-hyphen slug reported adopted (vacuous match)")
	}
}

func TestRunVerifyEmitsAdoptedOnly(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "use uv not pip\n")
	fold := `{"items":[
	  {"kind":"repeated-correction","id":"repeated-correction#use-uv","status":"open","title":"t1"},
	  {"kind":"repeated-correction","id":"repeated-correction#unrelated-thing","status":"open","title":"t2"},
	  {"kind":"repeated-correction","id":"repeated-correction#use-uv-closed","status":"closed","title":"t3"}
	]}`
	var out bytes.Buffer
	if err := runVerify(home, strings.NewReader(fold), &out); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	want := `{"kind":"repeated-correction","id":"repeated-correction#use-uv"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
