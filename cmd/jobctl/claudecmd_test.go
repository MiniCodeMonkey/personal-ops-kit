package main

import (
	"testing"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
)

func TestClaudeCommandStartsInProjectDir(t *testing.T) {
	plain := claudeCommand(&job.Job{}, "/ops/x/memo.md")
	if want := "claude 'Read /ops/x/memo.md and help me act on its suggestions.'"; plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
	inProject := claudeCommand(&job.Job{ProjectDir: "/Users/me/projects/notes"}, "/ops/x/memo.md")
	if want := "cd '/Users/me/projects/notes' && claude 'Read /ops/x/memo.md and help me act on its suggestions.'"; inProject != want {
		t.Errorf("inProject = %q, want %q", inProject, want)
	}
}

func TestClaudeCommandQuotesApostrophes(t *testing.T) {
	got := claudeCommand(&job.Job{}, "/ops/it's.md")
	if want := `claude 'Read /ops/it'\''s.md and help me act on its suggestions.'`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
