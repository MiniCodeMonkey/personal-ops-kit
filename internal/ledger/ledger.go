// Package ledger is the append-only record of what each job did.
//
// Two properties matter. It records *every* run, not just successful ones -- the old
// ledger only got a line when the research agent chose to write one, so a gated
// night, a crash and a night that produced nothing all left no trace, and the run
// history in the dashboard was literally unrenderable. And it is never rewritten:
// acknowledgements and reminders are appended as their own records and folded in on
// read, so a crash mid-write can lose at most the last line rather than corrupt the
// history.
package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Outcome is how a run ended. Five, because the dashboard has to tell them apart:
// a quiet day and a broken scraper look identical if both are just "no output".
type Outcome string

const (
	// OK: ran and produced an artifact.
	OK Outcome = "ok"
	// Warning: ran and produced an artifact with a defect worth surfacing.
	Warning Outcome = "warning"
	// NoChange: ran correctly and found nothing worth reporting.
	NoChange Outcome = "no-change"
	// Gated: never ran, because the quota gate said no.
	Gated Outcome = "gated"
	// Failed: crashed, timed out, or produced nothing it should have.
	Failed Outcome = "failed"
)

// Record types. A missing type means a run, so ledgers written before this package
// existed still read correctly.
const (
	typeRun    = "run"
	typeAck    = "ack"
	typeRemind = "remind"
)

// Record is one line of the ledger.
type Record struct {
	Type string `json:"type,omitempty"`

	Job   string `json:"job,omitempty"`
	RunID string `json:"run_id,omitempty"`

	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Outcome    Outcome   `json:"outcome,omitempty"`

	// Headline is the one line shown in the dashboard and the notification.
	Headline string `json:"headline,omitempty"`
	// Detail explains a gate or a failure: "7-day usage at 71% (threshold 60%)".
	Detail string `json:"detail,omitempty"`
	// Artifact is the file the run produced, relative to the job's artifact dir.
	Artifact string `json:"artifact,omitempty"`
	// LogFile is the captured output for this run.
	LogFile string `json:"log_file,omitempty"`
	// Trigger records why the run happened: schedule, catchup or manual.
	Trigger string `json:"trigger,omitempty"`

	At time.Time `json:"at,omitempty"` // for ack and remind records

	// Fields written by the previous ledger format, kept so old lines survive a
	// round trip and the research memos stay linked.
	Lane    string `json:"lane,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Summary string `json:"summary,omitempty"`
	Memo    string `json:"memo,omitempty"`
	Date    string `json:"date,omitempty"`
}

// Run is a folded view: a run plus whatever happened to it afterwards.
type Run struct {
	Record
	AcknowledgedAt time.Time
	RemindedAt     time.Time
}

// Acknowledged reports whether the result has been seen.
func (r Run) Acknowledged() bool { return !r.AcknowledgedAt.IsZero() }

// Append adds one record, creating the ledger if needed.
//
// O_APPEND on a file opened per call is atomic for writes below the pipe buffer on
// macOS, which every record here is by a wide margin. That is what makes it safe for
// the daemon and a manual `jobctl run` to write concurrently.
func Append(path string, rec Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create ledger directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode ledger record: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	return nil
}

// AppendRun records a completed (or skipped) run.
func AppendRun(path string, rec Record) error {
	rec.Type = typeRun
	if rec.Date == "" && !rec.StartedAt.IsZero() {
		rec.Date = rec.StartedAt.Format("2006-01-02")
	}
	return Append(path, rec)
}

// Acknowledge marks a run as seen.
func Acknowledge(path, job, runID string, at time.Time) error {
	return Append(path, Record{Type: typeAck, Job: job, RunID: runID, At: at})
}

// Remind records that a reminder was posted, so the morning nudge cannot double-post
// after a daemon restart.
func Remind(path, job, runID string, at time.Time) error {
	return Append(path, Record{Type: typeRemind, Job: job, RunID: runID, At: at})
}

// Load reads a ledger and folds acknowledgements and reminders into their runs.
//
// A malformed line is skipped rather than fatal. A ledger is a history, and one bad
// line -- a partial write from a hard kill, or something hand-edited -- should not
// make the rest unreadable.
func Load(path string) ([]Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ledger: %w", err)
	}

	var runs []Run
	index := map[string]int{}
	acks := map[string]time.Time{}
	reminders := map[string]time.Time{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		switch rec.Type {
		case typeAck:
			acks[rec.RunID] = rec.At
		case typeRemind:
			reminders[rec.RunID] = rec.At
		default: // "" or "run"
			// Lines from the previous format carry a memo path and a date but no run
			// id. Synthesize one so they still fold and display.
			if rec.RunID == "" {
				rec.RunID = rec.Date + "/" + rec.Lane
			}
			if rec.Outcome == "" {
				rec.Outcome = OK
			}
			if rec.Headline == "" {
				rec.Headline = firstNonEmpty(rec.Topic, rec.Summary)
			}
			if rec.Artifact == "" {
				rec.Artifact = rec.Memo
			}
			// Old lines carry a date but no timestamp. Without this every consumer
			// has to remember to fall back, and the ones that forget render a
			// legacy run as 1 Jan year zero.
			if rec.StartedAt.IsZero() && rec.Date != "" {
				if t, err := time.ParseInLocation("2006-01-02", rec.Date, time.Local); err == nil {
					rec.StartedAt = t
				}
			}
			index[rec.RunID] = len(runs)
			runs = append(runs, Run{Record: rec})
		}
	}

	for id, at := range acks {
		if i, ok := index[id]; ok {
			runs[i].AcknowledgedAt = at
		}
	}
	for id, at := range reminders {
		if i, ok := index[id]; ok {
			runs[i].RemindedAt = at
		}
	}

	sort.SliceStable(runs, func(a, b int) bool {
		return runStart(runs[a]).After(runStart(runs[b]))
	})
	return runs, nil
}

func runStart(r Run) time.Time {
	if !r.StartedAt.IsZero() {
		return r.StartedAt
	}
	if r.Date != "" {
		if t, err := time.ParseInLocation("2006-01-02", r.Date, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Last returns the most recent run, if any.
func Last(runs []Run) (Run, bool) {
	if len(runs) == 0 {
		return Run{}, false
	}
	return runs[0], true
}

// Outstanding returns the run a reminder should be about: the most recent one, and
// only while it is still unacknowledged.
//
// Older unacknowledged runs are dropped rather than queued behind it. Once a job has
// run again, the earlier result no longer describes the job's state -- a failure
// followed by another run has already been answered, either by a retry that worked or
// by a newer failure, and the newer record is the one worth interrupting for. Worse,
// nothing will ever acknowledge the superseded run: it is not what a notification
// click resolves to, so it stays unread and nags every morning until MaxAge retires
// it. The dashboard still lists it; it just stops asking about a settled question.
func Outstanding(runs []Run) (Run, bool) {
	last, ok := Last(runs)
	if !ok || last.Acknowledged() {
		return Run{}, false
	}
	return last, true
}

// Unacknowledged returns runs still waiting to be seen, newest first.
func Unacknowledged(runs []Run) []Run {
	var out []Run
	for _, r := range runs {
		if !r.Acknowledged() {
			out = append(out, r)
		}
	}
	return out
}
