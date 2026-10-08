package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// foldFile is the shape of the -open file: a jobctl fold JSON payload
// ({"items":[...]}). Only Kind/ID are consumed from each item.
type foldFile struct {
	Items []openItem `json:"items"`
}

// loadOpen reads the -open file (path must be non-empty; runMain guards
// that). A missing file or an empty file yields an empty set, not an error.
func loadOpen(path string) ([]openItem, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var ff foldFile
	if err := json.Unmarshal(raw, &ff); err != nil {
		return nil, fmt.Errorf("parsing -open file %q: %w", path, err)
	}
	return ff.Items, nil
}

// runMain does the CLI's work against injected streams so it's directly
// testable: read every job's ledger under opsDir, fold in the current open
// set, write the memo to outPath, and emit one JSONL candidate per line to
// stdout.
func runMain(opsDir, openPath, outPath string, now time.Time, stdout, stderr io.Writer) error {
	if opsDir == "" {
		return fmt.Errorf("-ops is required")
	}
	if outPath == "" {
		return fmt.Errorf("-out is required")
	}
	if _, err := os.ReadDir(opsDir); err != nil {
		return fmt.Errorf("-ops %q: %w", opsDir, err)
	}
	if openPath == "" {
		return fmt.Errorf("-open is required")
	}

	open, err := loadOpen(openPath)
	if err != nil {
		return err
	}

	cur, prev, skipped, err := collectStats(opsDir, now)
	if err != nil {
		return fmt.Errorf("collecting stats: %w", err)
	}

	flags := computeFlags(cur)
	month := monthLabel(now)
	cands := buildCandidates(flags, open, cur, month)
	memo := buildMemo(cur, prev, flags, month, skipped)

	if err := os.WriteFile(outPath, []byte(memo), 0o644); err != nil {
		return fmt.Errorf("writing memo to %q: %w", outPath, err)
	}

	enc := json.NewEncoder(stdout)
	for _, c := range cands {
		if err := enc.Encode(c); err != nil {
			return fmt.Errorf("encoding candidate: %w", err)
		}
	}

	if skipped > 0 {
		fmt.Fprintf(stderr, "platformaudit: skipped %d malformed ledger line(s)\n", skipped)
	}

	return nil
}

func main() {
	ops := flag.String("ops", "", "ops artifacts root (one dir per job, each with ledger.jsonl)")
	openPath := flag.String("open", "", "path to jobctl fold JSON of currently-open items")
	out := flag.String("out", "", "path to write the audit memo")
	nowFlag := flag.String("now", "", "override the current time (RFC3339); defaults to wall clock")
	flag.Parse()

	now := time.Now()
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "platformaudit: -now: %v\n", err)
			os.Exit(1)
		}
		now = t
	}

	if err := runMain(*ops, *openPath, *out, now, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "platformaudit: %v\n", err)
		os.Exit(1)
	}
}
