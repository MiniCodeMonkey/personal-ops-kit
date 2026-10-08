package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func jl(lines ...string) *strings.Reader { return strings.NewReader(strings.Join(lines, "\n")) }

func userLine(text string) string {
	// content as plain string form
	return `{"type":"user","message":{"role":"user","content":` + jsonString(text) + `}}`
}

func userBlocks(texts ...string) string {
	var blocks []string
	for _, t := range texts {
		blocks = append(blocks, `{"type":"text","text":`+jsonString(t)+`}`)
	}
	return `{"type":"user","message":{"role":"user","content":[` + strings.Join(blocks, ",") + `]}}`
}

func TestParseTranscriptKeepsHumanTurnsOnly(t *testing.T) {
	got := parseTranscript(jl(
		`{"type":"mode","mode":"normal"}`,
		userLine("use uv not pip"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"big output"}]}}`,
		userBlocks("please add a test for that"),
		"{not json at all",
	))
	want := []string{"use uv not pip", "please add a test for that"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseTranscriptDropsMachinePrefixes(t *testing.T) {
	for _, prefix := range []string{
		"<command-name>/foo</command-name>",
		"<system-reminder>stuff",
		"Base directory for this skill: /x",
		"[SYSTEM NOTIFICATION - NOT USER INPUT]",
		"<task-notification>",
		"Caveat: the messages below",
	} {
		got := parseTranscript(jl(userLine(prefix + " trailing")))
		if len(got) != 0 {
			t.Fatalf("prefix %q not dropped: %q", prefix, got)
		}
	}
}

func TestParseTranscriptSizeBoundsAndShortFollowUp(t *testing.T) {
	long := strings.Repeat("x", 1501)
	got := parseTranscript(jl(
		userLine(long),  // >1500 bytes: dropped
		userLine("ok"),  // <3 chars, previous user turn was dropped -> dropped
		userLine("no."), // 3 chars: kept
		userLine("y"),   // short but follows a kept user turn: kept
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hm"}]}}`,
		userLine("k"), // short, follows assistant: dropped
	))
	want := []string{"no.", "y"}
	if len(got) != 2 || got[0] != "no." || got[1] != "y" {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDigestBuilderDedupesWithCount(t *testing.T) {
	b := newDigestBuilder(600000)
	b.add("notes-app", "2026-08-20", "use uv  not   pip")
	b.add("website", "2026-08-21", "Use uv not pip") // same first-80 lowercase key
	b.add("notes-app", "2026-08-21", "something else entirely")
	lines := b.lines()
	if len(lines) != 2 {
		t.Fatalf("want 2 deduped lines, got %q", lines)
	}
	if lines[0] != "[notes-app 2026-08-20] use uv not pip (×2)" {
		t.Fatalf("dedupe line wrong: %q", lines[0])
	}
	if b.turns() != 3 {
		t.Fatalf("turns() should count duplicates: got %d", b.turns())
	}
}

func TestDigestBuilderEnforcesCap(t *testing.T) {
	b := newDigestBuilder(200) // tiny cap for the test
	b.add("p", "2026-08-22", strings.Repeat("a", 150))
	b.add("p", "2026-08-21", strings.Repeat("b", 150)) // over cap: ignored
	if !b.capped() {
		t.Fatal("cap not reported")
	}
	if len(b.lines()) != 1 || !strings.Contains(b.lines()[0], "aaa") {
		t.Fatalf("newest-first retention wrong: %q", b.lines())
	}
}

func TestDigestBuilderTurnsExcludesCapRejected(t *testing.T) {
	b := newDigestBuilder(200) // tiny cap for the test
	b.add("p", "2026-08-22", strings.Repeat("a", 150))
	b.add("p", "2026-08-21", strings.Repeat("b", 150)) // over cap: rejected, distinct
	b.add("p", "2026-08-20", strings.Repeat("c", 150)) // over cap: rejected, distinct
	if !b.capped() {
		t.Fatal("cap not reported")
	}
	if b.turns() != 1 {
		t.Fatalf("turns() should not count cap-rejected lines: got %d, want 1", b.turns())
	}
	b.add("p", "2026-08-22", strings.Repeat("a", 150)) // duplicate of the accepted line
	if b.turns() != 2 {
		t.Fatalf("turns() should count a duplicate of an accepted line: got %d, want 2", b.turns())
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
