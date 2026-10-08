// Command transcriptminer distills Claude session transcripts for the
// transcript-miner job. See templates/transcript-miner/about.md.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	maxTurnBytes = 1500
	minTurnChars = 3
	dedupeKeyLen = 80
)

// machinePrefixes is machine-generated text that arrives dressed as user
// turns: skill expansions, harness notifications, resume caveats.
var machinePrefixes = []string{
	"<command-name>",
	"<system-reminder>",
	"Base directory for this skill:",
	"[SYSTEM NOTIFICATION",
	"<task-notification>",
	"Caveat:",
}

// transcriptLine is the subset of a session JSONL line the miner reads.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// parseTranscript extracts the human-typed turns of one transcript in file
// order, applying the per-turn filter rules. Lenient: malformed lines skip.
func parseTranscript(r io.Reader) []string {
	var kept []string
	br := bufio.NewReader(r)
	prevKeptUser := false
	for {
		raw, readErr := br.ReadBytes('\n')
		if len(raw) > 0 {
			text, isUser := userText(raw)
			switch {
			case !isUser:
				prevKeptUser = false
			case text == "":
				prevKeptUser = false
			case hasMachinePrefix(text):
				prevKeptUser = false
			case len(text) > maxTurnBytes:
				prevKeptUser = false
			case len([]rune(text)) < minTurnChars && !prevKeptUser:
				// bare ack with no kept user turn right before it
			default:
				kept = append(kept, text)
				prevKeptUser = true
			}
		}
		if readErr != nil {
			return kept
		}
	}
}

// userText returns the concatenated typed text of a line when it is a user
// message (string content or text blocks; tool results excluded).
func userText(raw []byte) (string, bool) {
	var l transcriptLine
	if err := json.Unmarshal(raw, &l); err != nil || l.Type != "user" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(l.Message.Content, &s); err == nil {
		return strings.TrimSpace(s), true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n"), true
}

func hasMachinePrefix(text string) bool {
	for _, p := range machinePrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// digestBuilder accumulates turns newest-session-first, dedupes near-repeats,
// and enforces the byte cap. Callers add sessions in newest-first order, so
// hitting the cap drops the oldest material.
type digestBuilder struct {
	capBytes int
	used     int
	full     bool
	total    int
	order    []string
	byKey    map[string]*digestLine
}

type digestLine struct {
	project, date, text string
	count               int
}

func newDigestBuilder(capBytes int) *digestBuilder {
	return &digestBuilder{capBytes: capBytes, byKey: map[string]*digestLine{}}
}

func normalize(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func dedupeKey(norm string) string {
	r := []rune(strings.ToLower(norm))
	if len(r) > dedupeKeyLen {
		r = r[:dedupeKeyLen]
	}
	return string(r)
}

func (b *digestBuilder) add(project, date, text string) {
	norm := normalize(text)
	if norm == "" {
		return
	}
	key := dedupeKey(norm)
	if dl, ok := b.byKey[key]; ok {
		dl.count++
		b.total++
		return
	}
	cost := len(norm) + len(project) + len(date) + 16
	if b.full || b.used+cost > b.capBytes {
		b.full = true
		return
	}
	b.used += cost
	b.byKey[key] = &digestLine{project: project, date: date, text: norm, count: 1}
	b.order = append(b.order, key)
	b.total++
}

func (b *digestBuilder) lines() []string {
	out := make([]string, 0, len(b.order))
	for _, key := range b.order {
		dl := b.byKey[key]
		line := fmt.Sprintf("[%s %s] %s", dl.project, dl.date, dl.text)
		if dl.count > 1 {
			line += fmt.Sprintf(" (×%d)", dl.count)
		}
		out = append(out, line)
	}
	return out
}

func (b *digestBuilder) capped() bool   { return b.full }
func (b *digestBuilder) turns() int     { return b.total }
func (b *digestBuilder) bytesUsed() int { return b.used }
