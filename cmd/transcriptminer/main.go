package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	digestCapBytes   = 600000 // ~150k tokens at 4 bytes/token
	activeSkipWindow = 10 * time.Minute
	cursorFile       = "last_mined"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: transcriptminer <distill|verify> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "distill":
		fs := flag.NewFlagSet("distill", flag.ExitOnError)
		home := fs.String("home", os.Getenv("HOME"), "home directory holding .claude* profiles")
		state := fs.String("state", "", "job state dir (cursor lives here)")
		out := fs.String("out", "", "digest output path")
		activity := fs.String("activity", "", "per-project activity JSON output path")
		nowFlag := fs.String("now", "", "override clock, RFC 3339 (tests)")
		fs.Parse(os.Args[2:])
		now := time.Now()
		if *nowFlag != "" {
			if now, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(2)
			}
		}
		if *state == "" || *out == "" || *activity == "" {
			fmt.Fprintln(os.Stderr, "distill: -state, -out, and -activity are required")
			os.Exit(2)
		}
		err = runDistill(*home, *state, *out, *activity, now)
	case "verify":
		fs := flag.NewFlagSet("verify", flag.ExitOnError)
		home := fs.String("home", os.Getenv("HOME"), "home directory holding .claude* profiles")
		fs.Parse(os.Args[2:])
		err = runVerify(*home, os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type sessionFile struct {
	path    string
	project string
	mtime   time.Time
}

type projectActivity struct {
	Sessions int `json:"sessions"`
	Turns    int `json:"turns"`
}

// profileProjectDirs returns each real profile's projects dir, deduplicated
// by resolved symlink target (~/.claude-personal aliases ~/.claude). A
// profile without projects/ is normal and skipped.
func profileProjectDirs(home string) []string {
	matches, _ := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
	seen := map[string]bool{}
	var dirs []string
	for _, m := range matches {
		resolved, err := filepath.EvalSymlinks(m)
		if err != nil || seen[resolved] {
			continue
		}
		seen[resolved] = true
		dirs = append(dirs, resolved)
	}
	sort.Strings(dirs)
	return dirs
}

func runDistill(home, stateDir, outPath, activityPath string, now time.Time) error {
	cursor := time.Time{}
	if raw, err := os.ReadFile(filepath.Join(stateDir, cursorFile)); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw))); err == nil {
			cursor = t
		}
	}
	cutoff := now.Add(-activeSkipWindow)

	var sessions []sessionFile
	for _, projRoot := range profileProjectDirs(home) {
		projDirs, _ := os.ReadDir(projRoot)
		for _, pd := range projDirs {
			if !pd.IsDir() {
				continue
			}
			files, _ := filepath.Glob(filepath.Join(projRoot, pd.Name(), "*.jsonl"))
			for _, f := range files {
				info, err := os.Stat(f)
				if err != nil {
					continue
				}
				m := info.ModTime()
				if m.After(cursor) && !m.After(cutoff) {
					sessions = append(sessions, sessionFile{path: f, project: pd.Name(), mtime: m})
				}
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].mtime.After(sessions[j].mtime) })

	b := newDigestBuilder(digestCapBytes)
	activity := map[string]*projectActivity{}
	for _, s := range sessions {
		f, err := os.Open(s.path)
		if err != nil {
			continue
		}
		turns := parseTranscript(f)
		f.Close()
		a := activity[s.project]
		if a == nil {
			a = &projectActivity{}
			activity[s.project] = a
		}
		a.Sessions++
		a.Turns += len(turns)
		date := s.mtime.UTC().Format("2006-01-02")
		for _, turn := range turns {
			b.add(s.project, date, turn)
		}
	}

	actOut := map[string]projectActivity{}
	for p, a := range activity {
		actOut[p] = *a
	}
	actJSON, err := json.MarshalIndent(actOut, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(activityPath, actJSON, 0o644); err != nil {
		return err
	}

	var digest strings.Builder
	if lines := b.lines(); len(lines) > 0 {
		capNote := "cap not reached"
		if b.capped() {
			capNote = "CAP HIT -- oldest sessions dropped"
		}
		fmt.Fprintf(&digest, "# distilled %d sessions → %d turns, ~%d tokens (cap 150000; %s)\n",
			len(sessions), b.turns(), b.bytesUsed()/4, capNote)
		digest.WriteString(strings.Join(lines, "\n"))
		digest.WriteString("\n")
	}
	if err := os.WriteFile(outPath, []byte(digest.String()), 0o644); err != nil {
		return err
	}

	newCursor := cutoff.Format(time.RFC3339)
	return os.WriteFile(filepath.Join(stateDir, cursorFile), []byte(newCursor), 0o644)
}
