package entries

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validEntry() Entry {
	return Entry{
		TS:     time.Date(2026, 8, 23, 8, 2, 11, 0, time.UTC),
		Job:    "repo-activity",
		Kind:   "stranded-branch",
		ID:     "notes-site#fix-parser",
		Status: "open",
		Title:  "notes-site fix-parser: 14 uncommitted files, untouched 12 days",
		Data:   json.RawMessage(`{"age_days":12}`),
	}
}

func TestValidateAcceptsGoodEntry(t *testing.T) {
	e := validEntry()
	if err := e.Validate(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Entry)
		want   string // substring of the error
	}{
		{"missing job", func(e *Entry) { e.Job = "" }, "job"},
		{"missing kind", func(e *Entry) { e.Kind = "" }, "kind"},
		{"missing id", func(e *Entry) { e.ID = "" }, "id"},
		{"missing title", func(e *Entry) { e.Title = "" }, "title"},
		{"bad status", func(e *Entry) { e.Status = "done" }, "status"},
		{"title too long", func(e *Entry) { e.Title = strings.Repeat("x", 141) }, "title"},
		{"oversized data", func(e *Entry) {
			e.Data = json.RawMessage(`"` + strings.Repeat("x", 1030) + `"`)
		}, "data"},
		{"invalid data json", func(e *Entry) { e.Data = json.RawMessage(`{oops`) }, "data"},
		{"zero ts", func(e *Entry) { e.TS = time.Time{} }, "ts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validEntry()
			tc.mutate(&e)
			err := e.Validate()
			if err == nil {
				t.Fatalf("expected error mentioning %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestTitleLimitIsRunesNotBytes(t *testing.T) {
	e := validEntry()
	e.Title = strings.Repeat("æ", 140) // 280 bytes, 140 runes: fine
	if err := e.Validate(); err != nil {
		t.Fatalf("140-rune title rejected: %v", err)
	}
}
