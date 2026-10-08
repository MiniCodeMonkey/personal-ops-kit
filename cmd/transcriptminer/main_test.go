package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSession creates a transcript with the given user turns and mtime.
func writeSession(t *testing.T, home, profile, project, name string, mtime time.Time, turns ...string) string {
	t.Helper()
	dir := filepath.Join(home, profile, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, turn := range turns {
		lines = append(lines, userLine(turn))
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func distillInto(t *testing.T, home string, now time.Time) (digest string, activity map[string]struct {
	Sessions int `json:"sessions"`
	Turns    int `json:"turns"`
}) {
	t.Helper()
	state := filepath.Join(home, "state")
	out := filepath.Join(state, "digest.txt")
	act := filepath.Join(state, "activity.json")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runDistill(home, state, out, act, now); err != nil {
		t.Fatalf("runDistill: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	actRaw, err := os.ReadFile(act)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actRaw, &activity); err != nil {
		t.Fatalf("activity not JSON: %v\n%s", err, actRaw)
	}
	return string(raw), activity
}

func TestDistillWalksProfilesAndWritesDigestAndActivity(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	writeSession(t, home, ".claude", "-proj-a", "s1", now.Add(-2*time.Hour),
		"use uv not pip", "also add a changelog entry")
	writeSession(t, home, ".claude-work", "-proj-b", "s2", now.Add(-3*time.Hour),
		"never push without asking")

	digest, activity := distillInto(t, home, now)
	if !strings.HasPrefix(digest, "# distilled 2 sessions") {
		t.Fatalf("header wrong: %q", strings.SplitN(digest, "\n", 2)[0])
	}
	// newest session first
	if !strings.Contains(digest, "[-proj-a 2026-08-23] use uv not pip") {
		t.Fatalf("missing turn: %s", digest)
	}
	if strings.Index(digest, "-proj-a") > strings.Index(digest, "-proj-b") {
		t.Fatalf("not newest-first: %s", digest)
	}
	if activity["-proj-a"].Sessions != 1 || activity["-proj-a"].Turns != 2 {
		t.Fatalf("activity wrong: %+v", activity)
	}
}

func TestDistillDedupesSymlinkedProfiles(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	writeSession(t, home, ".claude", "-p", "s1", now.Add(-time.Hour), "only once please")
	if err := os.Symlink(filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")); err != nil {
		t.Fatal(err)
	}
	digest, activity := distillInto(t, home, now)
	if strings.Count(digest, "only once please") != 1 {
		t.Fatalf("symlinked profile double-counted: %s", digest)
	}
	if activity["-p"].Sessions != 1 {
		t.Fatalf("activity double-counted: %+v", activity)
	}
}

func TestDistillCursorAndActiveSessionSkip(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	writeSession(t, home, ".claude", "-p", "old", now.Add(-48*time.Hour), "old news")
	writeSession(t, home, ".claude", "-p", "fresh", now.Add(-time.Hour), "fresh insight")
	writeSession(t, home, ".claude", "-p", "live", now.Add(-5*time.Minute), "still typing")

	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	// cursor set after "old" was written
	cursor := now.Add(-24 * time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(state, "last_mined"), []byte(cursor), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, _ := distillInto(t, home, now)
	if strings.Contains(digest, "old news") {
		t.Fatal("cursor not honored")
	}
	if strings.Contains(digest, "still typing") {
		t.Fatal("active session not skipped")
	}
	if !strings.Contains(digest, "fresh insight") {
		t.Fatalf("in-window session missing: %s", digest)
	}
	newCursor, err := os.ReadFile(filepath.Join(state, "last_mined"))
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(-10 * time.Minute).Format(time.RFC3339)
	if strings.TrimSpace(string(newCursor)) != want {
		t.Fatalf("cursor: got %q want %q", newCursor, want)
	}
}

func TestDistillEmptyWeekWritesEmptyDigest(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	digest, activity := distillInto(t, home, now)
	if digest != "" {
		t.Fatalf("want empty digest, got %q", digest)
	}
	if len(activity) != 0 {
		t.Fatalf("want empty activity, got %+v", activity)
	}
}
