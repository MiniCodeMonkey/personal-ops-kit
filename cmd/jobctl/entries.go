// jobctl entries -- the shared cross-job state format.
//
// Unlike the other subcommands these are NOT thin clients over the daemon API:
// they operate directly on the files, so a job can append with the daemon down
// and the compiler can fold without a round-trip.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/entries"
)

func cmdEntries(cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: jobctl entries <append|fold> [flags]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "append":
		stateDir := os.Getenv("OPS_STATE_DIR")
		// --job (debugging only) may override; peeked at inside.
		if dir, remaining, ok := extractJobOverride(cfg, rest); ok {
			stateDir, rest = dir, remaining
		}
		return entriesAppendEntry(stateDir, rest)
	case "fold":
		return entriesFold(cfg, rest, os.Stdout)
	default:
		return fmt.Errorf("unknown entries subcommand %q", sub)
	}
}

// extractJobOverride pulls a --job NAME pair out of args and resolves it to
// that job's state dir. Debugging convenience only; run.sh relies on
// OPS_STATE_DIR, which the runner always sets.
func extractJobOverride(cfg Config, args []string) (stateDir string, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--job" && i+1 < len(args) {
			name := args[i+1]
			rest = append(append([]string{}, args[:i]...), args[i+2:]...)
			return filepath.Join(cfg.ArtifactsRoot, name, "state"), rest, true
		}
	}
	return "", args, false
}

func entriesAppendEntry(stateDir string, args []string) error {
	if stateDir == "" {
		return fmt.Errorf("no job context: OPS_STATE_DIR is unset (jobs get it from the runner; use --job <name> when debugging)")
	}
	fs := flag.NewFlagSet("entries append", flag.ContinueOnError)
	kind := fs.String("kind", "", "job-specific kind, e.g. stranded-branch")
	id := fs.String("id", "", "stable subject key, unique within the job")
	status := fs.String("status", "", "open | closed | event")
	title := fs.String("title", "", "one human sentence, <= 140 runes")
	data := fs.String("data", "", "small JSON object for drill-down (optional)")
	dryRun := fs.Bool("dry-run", false, "validate and print, do not write")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e := entries.Entry{
		Job:    filepath.Base(filepath.Dir(stateDir)), // state dir is ~/ops/<job>/state
		Kind:   *kind,
		ID:     *id,
		Status: *status,
		Title:  *title,
	}
	if *data != "" {
		e.Data = json.RawMessage(*data)
	}

	if *dryRun {
		_, line, err := entries.Prepare(e, time.Now())
		if err != nil {
			return err
		}
		fmt.Println(string(line))
		return nil
	}
	return entries.Append(filepath.Join(stateDir, "entries.jsonl"), e)
}

func entriesFold(cfg Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("entries fold", flag.ContinueOnError)
	sinceFlag := fs.String("since", "", "duration (7d, 36h) or RFC 3339 timestamp")
	jobFlag := fs.String("job", "", "limit to one job")
	openOnly := fs.Bool("open-only", false, "only loops whose latest status is open")
	if err := fs.Parse(args); err != nil {
		return err
	}

	opt := entries.FoldOptions{Job: *jobFlag, OpenOnly: *openOnly}
	if *sinceFlag != "" {
		since, err := parseSince(*sinceFlag)
		if err != nil {
			return err
		}
		opt.Since = since
	}

	paths, err := entries.StatePaths(cfg.ArtifactsRoot)
	if err != nil {
		return err
	}
	res, err := entries.Fold(paths, opt, time.Now())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// parseSince accepts "7d", any Go duration, or an RFC 3339 timestamp.
func parseSince(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse --since %q: want 7d, 36h, or RFC 3339", s)
}
