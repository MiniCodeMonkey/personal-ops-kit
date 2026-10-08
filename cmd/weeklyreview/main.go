// Command weeklyreview compiles the weekly review digest from two jobctl
// entry folds -- the current open items and a recent window of events and
// closures -- and prints it to stdout. Digest assembly lives in digest.go;
// this file is the CLI shell around it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var openPath, recentPath, jobsDir, sinceStr, nowStr string
	flag.StringVar(&openPath, "open", "", "path to jobctl entries fold output (currently-open items)")
	flag.StringVar(&recentPath, "recent", "", "path to jobctl entries fold output (recent window)")
	flag.StringVar(&jobsDir, "jobs", "", "path to the jobs directory (for job.env SCHEDULE lookups)")
	flag.StringVar(&sinceStr, "since", "", "RFC3339 timestamp: the covering-since date shown in the header")
	flag.StringVar(&nowStr, "now", "", "RFC3339 timestamp override for the current time (defaults to wall clock)")
	flag.Parse()

	if openPath == "" || recentPath == "" || jobsDir == "" || sinceStr == "" {
		fmt.Fprintln(os.Stderr, "weeklyreview: -open, -recent, -jobs, and -since are all required")
		os.Exit(1)
	}

	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weeklyreview: -since: %v\n", err)
		os.Exit(1)
	}

	now := time.Now()
	if nowStr != "" {
		now, err = time.Parse(time.RFC3339, nowStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "weeklyreview: -now: %v\n", err)
			os.Exit(1)
		}
	}

	open, err := readFoldFile(openPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weeklyreview: -open: %v\n", err)
		os.Exit(1)
	}
	recent, err := readFoldFile(recentPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weeklyreview: -recent: %v\n", err)
		os.Exit(1)
	}

	digest, empty := buildDigest(open, recent, jobsDir, now, since)
	if empty {
		os.Exit(3)
	}
	fmt.Print(digest)
}

// readFoldFile reads and parses a jobctl "entries fold" JSON output file.
// Deliberately lenient like the fold's own read discipline (see
// internal/entries.Fold's comment on skipping bad lines rather than
// failing the whole read): valid-but-wrong-shape JSON such as "{}" or "[]"
// unmarshals into a zero-value foldFile (no items, no skipped) rather than
// erroring, so an empty/degenerate fold file reads as "nothing to report"
// instead of aborting the whole digest.
func readFoldFile(path string) (foldFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return foldFile{}, err
	}
	var ff foldFile
	if err := json.Unmarshal(data, &ff); err != nil {
		return foldFile{}, err
	}
	return ff, nil
}
