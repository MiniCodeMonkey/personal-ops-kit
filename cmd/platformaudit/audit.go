// Command platformaudit reads every job's run ledger, buckets runs into a
// trailing 30-day window and the 30 days before that, and turns the
// comparison into three things: a monthly digest event, flags for jobs
// worth slowing down (always-quiet) or worth worrying about (failing), and
// a human-readable memo. This file implements the stats/flags/memo/
// candidates logic; main.go wraps it in a CLI.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
)

const (
	windowDays         = 30
	alwaysQuietMinRuns = 20
	failingMinRuns     = 5
	failingThreshold   = 0.20 // strict >
	maxTitleRunes      = 140
	skippedDirDaemon   = "daemon"
	ledgerFileName     = "ledger.jsonl"
)

// jobStats is one job's tally over a window, plus the previous window's
// median for convenient side-by-side reporting.
type jobStats struct {
	Job                                        string
	Runs, OK, NoChange, Warning, Gated, Failed int
	MedianSecs, PrevMedianSecs                 float64 // 0 when no data
	NotifyPerWeek                              float64
}

// candidate is one entry candidate this job proposes for the run. Shape is
// identical to cmd/repoactivity's; redeclared locally since these are
// separate main packages.
type candidate struct {
	Kind   string         `json:"kind"`
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Title  string         `json:"title"`
	Data   map[string]any `json:"data,omitempty"`
}

// openItem is the subset of a fold item this job needs.
type openItem struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// platformFlag is a job worth calling out: either it has gone quiet for
// long enough that it might not be worth running, or it is failing often
// enough to be worth worrying about.
type platformFlag struct {
	Job  string
	Kind string // "always-quiet" | "failing"
	Runs int
	Pct  int // 100 for always-quiet; the rounded failure percentage for failing
}

func (f platformFlag) id() string {
	return fmt.Sprintf("platform-flag#%s#%s", f.Job, f.Kind)
}

func (f platformFlag) title() string {
	switch f.Kind {
	case "always-quiet":
		return fmt.Sprintf("%s: 100%% quiet over %d runs -- slow it down or kill it", f.Job, f.Runs)
	case "failing":
		return fmt.Sprintf("%s: %d%% failed over %d runs", f.Job, f.Pct, f.Runs)
	default:
		return ""
	}
}

// collectStats reads <opsDir>/*/ledger.jsonl, skipping the daemon dir and
// any dir without a ledger.jsonl, and buckets every run record into the
// current window [now-30d, now) and the previous window [now-60d, now-30d),
// both by StartedAt. Malformed lines are skipped and counted, not fatal;
// ack/remind records are ignored. opsDir itself must be readable.
func collectStats(opsDir string, now time.Time) (cur, prev map[string]jobStats, skippedLines int, err error) {
	entries, err := os.ReadDir(opsDir)
	if err != nil {
		return nil, nil, 0, err
	}

	curStart := now.AddDate(0, 0, -windowDays)
	prevStart := now.AddDate(0, 0, -2*windowDays)

	cur = map[string]jobStats{}
	prev = map[string]jobStats{}
	curDurations := map[string][]float64{}
	prevDurations := map[string][]float64{}

	for _, e := range entries {
		if !e.IsDir() || e.Name() == skippedDirDaemon {
			continue
		}
		job := e.Name()
		data, rerr := os.ReadFile(filepath.Join(opsDir, job, ledgerFileName))
		if rerr != nil {
			continue // no ledger.jsonl (or unreadable) -- skip silently
		}

		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var rec ledger.Record
			if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
				skippedLines++
				continue
			}
			if rec.Type != "" && rec.Type != "run" {
				continue // ack/remind
			}

			outcome := rec.Outcome
			if outcome == "" {
				outcome = ledger.OK
			}

			// Legacy pre-runner-package lines carry a date but no started_at.
			// Mirror ledger.Load's own fallback exactly (internal/ledger/ledger.go
			// lines 168-192, the StartedAt derivation at lines 186-190): Load
			// treats every "" or "run" record as a run regardless of StartedAt,
			// and when StartedAt is zero but Date is set, parses Date
			// ("2006-01-02", Local) into StartedAt. Without this, these lines
			// silently fall out of both windows and out of the memo's skipped
			// tally -- present in the ledger, invisible everywhere.
			if rec.StartedAt.IsZero() && rec.Date != "" {
				if t, terr := time.ParseInLocation("2006-01-02", rec.Date, time.Local); terr == nil {
					rec.StartedAt = t
				}
			}

			var bucket map[string]jobStats
			var durations map[string][]float64
			switch {
			case !rec.StartedAt.Before(curStart) && rec.StartedAt.Before(now):
				bucket, durations = cur, curDurations
			case !rec.StartedAt.Before(prevStart) && rec.StartedAt.Before(curStart):
				bucket, durations = prev, prevDurations
			default:
				// Outside both windows -- including a record with no timestamp
				// at all (no StartedAt, no Date to fall back to), which Load
				// still keeps as a run but which we have nothing to bucket it
				// by; that is a record property, not something this loop drops.
				continue
			}

			js := bucket[job]
			js.Job = job
			js.Runs++
			switch outcome {
			case ledger.OK:
				js.OK++
			case ledger.NoChange:
				js.NoChange++
			case ledger.Warning:
				js.Warning++
			case ledger.Gated:
				js.Gated++
			case ledger.Failed:
				js.Failed++
			}
			bucket[job] = js

			if !rec.FinishedAt.IsZero() {
				if d := rec.FinishedAt.Sub(rec.StartedAt).Seconds(); d >= 0 {
					durations[job] = append(durations[job], d)
				}
			}
		}
	}

	weeksInWindow := float64(windowDays) / 7.0
	for job, js := range cur {
		js.MedianSecs = median(curDurations[job])
		js.PrevMedianSecs = median(prevDurations[job])
		js.NotifyPerWeek = float64(js.OK+js.Warning+js.Failed) / weeksInWindow
		cur[job] = js
	}
	for job, js := range prev {
		js.MedianSecs = median(prevDurations[job])
		prev[job] = js
	}

	return cur, prev, skippedLines, nil
}

// median returns the median of vals, or 0 for an empty slice.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// computeFlags evaluates the always-quiet and failing thresholds against
// the current window only, in alphabetical job order.
func computeFlags(cur map[string]jobStats) []platformFlag {
	var flags []platformFlag
	for _, job := range sortedJobs(cur) {
		js := cur[job]
		if js.Runs >= alwaysQuietMinRuns && js.NoChange == js.Runs {
			flags = append(flags, platformFlag{Job: job, Kind: "always-quiet", Runs: js.Runs, Pct: 100})
			continue
		}
		if js.Runs >= failingMinRuns {
			ratio := float64(js.Failed) / float64(js.Runs)
			if ratio > failingThreshold {
				flags = append(flags, platformFlag{
					Job:  job,
					Kind: "failing",
					Runs: js.Runs,
					Pct:  int(math.Round(ratio * 100)),
				})
			}
		}
	}
	return flags
}

// monthLabel is the "YYYY-MM" of the month containing now-1day, so a run on
// the 1st reports the month that just ended.
func monthLabel(now time.Time) string {
	return now.AddDate(0, 0, -1).Format("2006-01")
}

// buildCandidates turns this run's flags and stats into the monthly digest
// event, an open candidate per flag, and a close for every open platform-flag
// id whose condition no longer holds. Foreign kinds in the open set are left
// untouched.
func buildCandidates(flags []platformFlag, open []openItem, cur map[string]jobStats, month string) []candidate {
	cands := []candidate{monthlyEvent(flags, cur, month)}

	stillFlagged := map[string]bool{}
	for _, f := range flags {
		stillFlagged[f.id()] = true
		data := map[string]any{"job": f.Job, "runs": f.Runs}
		if f.Kind == "failing" {
			data["pct"] = f.Pct
		}
		cands = append(cands, candidate{
			Kind:   "platform-flag",
			ID:     f.id(),
			Status: "open",
			Title:  f.title(),
			Data:   data,
		})
	}

	for _, o := range open {
		if o.Kind != "platform-flag" {
			continue // foreign kind, untouched
		}
		if stillFlagged[o.ID] {
			continue
		}
		cands = append(cands, candidate{
			Kind:   "platform-flag",
			ID:     o.ID,
			Status: "closed",
			Title:  "resolved",
			Data:   map[string]any{"reason": "gone"},
		})
	}

	return cands
}

// monthlyEvent summarizes the current window's aggregate stats as the
// reported month's digest event.
func monthlyEvent(flags []platformFlag, cur map[string]jobStats, month string) candidate {
	var runs, noChange, failed, gated int
	for _, job := range sortedJobs(cur) {
		js := cur[job]
		runs += js.Runs
		noChange += js.NoChange
		failed += js.Failed
		gated += js.Gated
	}

	quietPct := 0
	if runs > 0 {
		quietPct = int(math.Round(100 * float64(noChange) / float64(runs)))
	}

	monTime, _ := time.Parse("2006-01", month)
	monName := monTime.Format("Jan")

	summary := "no flags"
	var ids []string
	if len(flags) > 0 {
		parts := make([]string, 0, len(flags))
		for _, f := range flags {
			label := "quiet"
			if f.Kind == "failing" {
				label = "failing"
			}
			parts = append(parts, fmt.Sprintf("%s %s", f.Job, label))
			ids = append(ids, f.id())
		}
		summary = strings.Join(parts, ", ")
	}

	title := truncateTitle(fmt.Sprintf("%s: %d runs, %d%% quiet, %s", monName, runs, quietPct, summary))

	return candidate{
		Kind:   "platform-audit",
		ID:     fmt.Sprintf("platform-audit#%s", month),
		Status: "event",
		Title:  title,
		Data: map[string]any{
			"runs":      runs,
			"quiet_pct": quietPct,
			"failed":    failed,
			"gated":     gated,
			"flags":     ids,
		},
	}
}

// buildMemo renders the human-readable audit memo: flags first, then
// outcomes/notification-volume/duration tables, jobs in alphabetical order
// throughout, deterministic given the same inputs.
func buildMemo(cur, prev map[string]jobStats, flags []platformFlag, month string, skipped int) string {
	var b strings.Builder

	monTime, _ := time.Parse("2006-01", month)
	fmt.Fprintf(&b, "Platform Audit -- %s\n\n", monTime.Format("January 2006"))

	b.WriteString("Flags:\n")
	if len(flags) == 0 {
		b.WriteString("No flags.\n")
	} else {
		for _, f := range flags {
			fmt.Fprintf(&b, "- %s\n", f.title())
		}
	}
	b.WriteString("\n")

	jobs := unionJobs(cur, prev)

	b.WriteString("Outcomes per job:\n")
	for _, job := range jobs {
		js := cur[job]
		fmt.Fprintf(&b, "%s: runs=%d ok=%d no-change=%d warning=%d gated=%d failed=%d\n",
			job, js.Runs, js.OK, js.NoChange, js.Warning, js.Gated, js.Failed)
	}
	b.WriteString("\n")

	b.WriteString("Notification volume (per week):\n")
	for _, job := range jobs {
		js := cur[job]
		fmt.Fprintf(&b, "%s: %.2f/week\n", job, js.NotifyPerWeek)
	}
	b.WriteString("\n")

	b.WriteString("Run duration (median seconds):\n")
	for _, job := range jobs {
		js := cur[job]
		drift := "–"
		if js.PrevMedianSecs != 0 {
			drift = fmt.Sprintf("%+.1f", js.MedianSecs-js.PrevMedianSecs)
		}
		fmt.Fprintf(&b, "%s: current=%.1f previous=%.1f drift=%s\n", job, js.MedianSecs, js.PrevMedianSecs, drift)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Malformed lines skipped: %d\n", skipped)

	return b.String()
}

func sortedJobs(m map[string]jobStats) []string {
	jobs := make([]string, 0, len(m))
	for j := range m {
		jobs = append(jobs, j)
	}
	sort.Strings(jobs)
	return jobs
}

func unionJobs(cur, prev map[string]jobStats) []string {
	set := map[string]bool{}
	for j := range cur {
		set[j] = true
	}
	for j := range prev {
		set[j] = true
	}
	jobs := make([]string, 0, len(set))
	for j := range set {
		jobs = append(jobs, j)
	}
	sort.Strings(jobs)
	return jobs
}

// truncateTitle clamps s to maxTitleRunes.
func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) <= maxTitleRunes {
		return s
	}
	return string(r[:maxTitleRunes])
}
