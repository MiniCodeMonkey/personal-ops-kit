// Package entries implements the shared cross-job state format: an append-only
// JSONL file per job that every collector writes and the weekly compiler folds.
// See "Shared entries" in templates/CLAUDE.md.
package entries

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	maxTitleRunes = 140
	maxDataBytes  = 1024
)

// Entry is one line of a job's entries.jsonl. The current state of a subject
// is the latest entry per (job, kind, id) -- latest-wins.
type Entry struct {
	TS     time.Time       `json:"ts"`
	Job    string          `json:"job"`
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Status string          `json:"status"` // "open" | "closed" | "event"
	Title  string          `json:"title"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Validate is the write-side gate: strict on write, lenient on read.
func (e *Entry) Validate() error {
	if e.TS.IsZero() {
		return fmt.Errorf("entry ts is unset")
	}
	if e.Job == "" {
		return fmt.Errorf("entry job is empty")
	}
	if e.Kind == "" {
		return fmt.Errorf("entry kind is empty")
	}
	if e.ID == "" {
		return fmt.Errorf("entry id is empty")
	}
	switch e.Status {
	case "open", "closed", "event":
	default:
		return fmt.Errorf("entry status %q is not open, closed, or event", e.Status)
	}
	if e.Title == "" {
		return fmt.Errorf("entry title is empty")
	}
	if n := utf8.RuneCountInString(e.Title); n > maxTitleRunes {
		return fmt.Errorf("entry title is %d runes, max %d", n, maxTitleRunes)
	}
	if len(e.Data) > 0 {
		if len(e.Data) > maxDataBytes {
			return fmt.Errorf("entry data is %d bytes, max %d", len(e.Data), maxDataBytes)
		}
		if !json.Valid(e.Data) {
			return fmt.Errorf("entry data is not valid JSON")
		}
	}
	return nil
}
