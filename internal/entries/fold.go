package entries

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Folded is the current state of one subject plus derived lifecycle fields.
// FirstSeen is the start of the current open episode -- the first entry after
// the subject's most recent closed -- so a reopened loop ages from reopening.
type Folded struct {
	Entry
	FirstSeen time.Time `json:"first_seen"`
	AgeDays   int       `json:"age_days"`
}

type FoldOptions struct {
	Since    time.Time // zero = no filter; else keep keys whose latest ts >= Since
	Job      string    // "" = all jobs
	OpenOnly bool      // keep only latest-status "open"; ignores Since
}

type FoldResult struct {
	Items   []Folded `json:"items"`
	Skipped int      `json:"skipped"`
}

type key struct{ job, kind, id string }

// Fold reads entries files and folds latest-per-(job, kind, id). Lenient on
// read: malformed, oversized, or otherwise unreadable lines are counted in
// Skipped and never fatal -- one bad job must not kill the weekly memo. A
// bufio.Reader line-by-line read (rather than bufio.Scanner) is used
// deliberately: Scanner's fixed max-token-size makes one oversized/corrupt
// line abort the whole file with a hard error, discarding everything folded
// so far across every file; ReadBytes has no such limit and lets a bad line
// simply count against Skipped while reading continues.
func Fold(paths []string, opt FoldOptions, now time.Time) (FoldResult, error) {
	latest := map[key]Entry{}
	episode := map[key]time.Time{} // first ts of the current open episode
	var skipped int

	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return FoldResult{}, err
		}
		r := bufio.NewReader(f)
		for {
			raw, readErr := r.ReadBytes('\n')
			if len(raw) > 0 {
				line := bytes.TrimRight(raw, "\r\n")
				var e Entry
				if err := json.Unmarshal(line, &e); err != nil ||
					e.Job == "" || e.Kind == "" || e.ID == "" || e.TS.IsZero() {
					skipped++
				} else {
					k := key{e.Job, e.Kind, e.ID}
					// Later entry wins; identical ts resolved by file order (>=).
					if prev, ok := latest[k]; !ok || !e.TS.Before(prev.TS) {
						latest[k] = e
					}
					if _, ok := episode[k]; !ok {
						episode[k] = e.TS
					}
					if e.Status == "closed" {
						// The next entry (if any) starts a fresh episode.
						delete(episode, k)
					}
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					// Reader failed mid-file (not a clean end-of-file): the
					// rest of this file can't be trusted, but the file
					// itself must not fail the whole fold -- count what's
					// lost and move on to the next file.
					skipped++
				}
				break
			}
		}
		f.Close()
	}

	items := []Folded{}
	for k, e := range latest {
		if opt.Job != "" && e.Job != opt.Job {
			continue
		}
		if opt.OpenOnly {
			if e.Status != "open" {
				continue
			}
		} else if !opt.Since.IsZero() && e.TS.Before(opt.Since) {
			continue
		}
		first, ok := episode[k]
		if !ok {
			first = e.TS // latest is a closed entry; episode ended with it
		}
		items = append(items, Folded{
			Entry:     e,
			FirstSeen: first,
			AgeDays:   int(now.Sub(first).Hours() / 24),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return FoldResult{Items: items, Skipped: skipped}, nil
}

// StatePaths lists every job's entries file under artifactsRoot (~/ops):
// <root>/<job>/state/entries.jsonl. Jobs without one simply do not appear.
func StatePaths(artifactsRoot string) ([]string, error) {
	return filepath.Glob(filepath.Join(artifactsRoot, "*", "state", "entries.jsonl"))
}
