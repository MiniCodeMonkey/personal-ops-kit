// Package job reads a job's manifest.
//
// A job is a directory in the user's jobs folder containing a job.env manifest and a run.sh. The
// manifest is shell-sourceable on purpose: run.sh sources it directly and this package
// parses the same file, so there is one declaration of a job's schedule and settings
// rather than two that can drift.
package job

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/schedule"
)

// Job is one scheduled unit of work.
type Job struct {
	Name        string
	Description string

	Schedule *schedule.Spec
	// Gated jobs consult the weekly quota gate before running. Reserved for jobs
	// expensive enough to genuinely compete with the next day's work.
	Gated bool
	// GateThreshold (GATE_THRESHOLD) is the 7-day usage percentage at or above
	// which a gated job skips. Empty means gate.sh's default.
	GateThreshold string
	// CatchupWindow bounds how late a missed fire may still be run.
	CatchupWindow time.Duration
	Timeout       time.Duration

	Model string
	// ClaudeProfileDir pins CLAUDE_CONFIG_DIR, which decides the MCP servers,
	// settings and project memory a run sees. Unset means the default profile.
	ClaudeProfileDir string
	// Icon names the heroicon the dashboard shows beside the job (JOB_ICON).
	Icon string
	// ProjectDir, when set (CLAUDE_PROJECT_DIR), is where a follow-up session on the
	// job's output should start, so the copied command begins with a `cd` there.
	ProjectDir string

	// ArtifactGlob is relative to the job's artifact directory and selects the files
	// the dashboard lists.
	ArtifactGlob string

	// IssueLinks (ISSUE_LINKS="ENG=https://linear.app/acme/issue/,MAR=...") maps
	// issue-key prefixes to URL bases; bare identifiers like ENG-123 in this job's
	// artifacts render as links, with a hover card fed by state/issues.json.
	IssueLinks map[string]string

	// Dir is the job's directory in the repository; ArtifactDir is where its output
	// lives, under the artifacts root.
	Dir         string
	ArtifactDir string
}

// RunScript is the executable the runner spawns.
// About returns the job's longer description -- <name>/about.md, markdown --
// or "" when the job has none. Read on demand, not at load, so editing the file
// shows up without a daemon reload.
func (j *Job) About() string {
	b, err := os.ReadFile(filepath.Join(j.Dir, "about.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

func (j *Job) RunScript() string { return filepath.Join(j.Dir, "run.sh") }

// LedgerPath is the job's append-only run history.
func (j *Job) LedgerPath() string { return filepath.Join(j.ArtifactDir, "ledger.jsonl") }

// LogDir holds one log file per run.
func (j *Job) LogDir() string { return filepath.Join(j.ArtifactDir, "logs") }

// StateDir is scratch the job owns across runs, such as a seen-set.
func (j *Job) StateDir() string { return filepath.Join(j.ArtifactDir, "state") }

// ParseEnv reads KEY=value lines, the subset of shell syntax a manifest needs.
//
// Deliberately not a shell: sourcing a manifest to read it would execute whatever it
// contains, and a config file should not be able to run commands. Quotes are stripped
// and $HOME / ~ expanded, which is the entire vocabulary a manifest is allowed.
func ParseEnv(r *os.File) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")

		eq := strings.Index(text, "=")
		if eq < 1 {
			return nil, fmt.Errorf("line %d: expected KEY=value, got %q", line, text)
		}
		key := strings.TrimSpace(text[:eq])
		value := strings.TrimSpace(text[eq+1:])

		// Strip a trailing comment only when the value is not quoted; a quoted value
		// may legitimately contain a '#'.
		if !strings.HasPrefix(value, `"`) && !strings.HasPrefix(value, "'") {
			if hash := strings.Index(value, " #"); hash >= 0 {
				value = strings.TrimSpace(value[:hash])
			}
		}
		value = unquote(value)
		values[key] = expandHome(value)
	}
	return values, scanner.Err()
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func expandHome(v string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return v
	}
	v = strings.ReplaceAll(v, "${HOME}", home)
	v = strings.ReplaceAll(v, "$HOME", home)
	if strings.HasPrefix(v, "~/") {
		v = filepath.Join(home, v[2:])
	}
	return v
}

// Defaults applied when a manifest leaves a field out.
const (
	DefaultTimeout       = 30 * time.Minute
	DefaultCatchupWindow = 12 * time.Hour
	DefaultArtifactGlob  = "*"
)

// Load reads one job directory. artifactsRoot is where job output lives, normally
// ~/ops.
func Load(dir, artifactsRoot string) (*Job, error) {
	manifest := filepath.Join(dir, "job.env")
	f, err := os.Open(manifest)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()

	values, err := ParseEnv(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest, err)
	}

	name := values["JOB_NAME"]
	if name == "" {
		name = filepath.Base(dir)
	}
	// A job whose manifest name disagrees with its directory would produce artifacts
	// and ledger entries under one name and be scheduled under another.
	if base := filepath.Base(dir); name != base {
		return nil, fmt.Errorf("%s: JOB_NAME %q does not match directory %q", manifest, name, base)
	}

	spec, err := schedule.Parse(values["SCHEDULE"])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest, err)
	}

	j := &Job{
		Name:             name,
		Description:      values["JOB_DESCRIPTION"],
		Schedule:         spec,
		Gated:            values["JOB_GATED"] == "1",
		GateThreshold:    values["GATE_THRESHOLD"],
		Model:            values["JOB_MODEL"],
		ClaudeProfileDir: values["CLAUDE_PROFILE_DIR"],
		Icon:             values["JOB_ICON"],
		ProjectDir:       values["CLAUDE_PROJECT_DIR"],
		ArtifactGlob:     values["ARTIFACT_GLOB"],
		IssueLinks:       ParseIssueLinks(values["ISSUE_LINKS"]),
		Timeout:          DefaultTimeout,
		CatchupWindow:    DefaultCatchupWindow,
		Dir:              dir,
		ArtifactDir:      filepath.Join(artifactsRoot, name),
	}
	if j.ArtifactGlob == "" {
		j.ArtifactGlob = DefaultArtifactGlob
	}
	if raw := values["JOB_TIMEOUT"]; raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("%s: JOB_TIMEOUT %q is not a positive number of seconds", manifest, raw)
		}
		j.Timeout = time.Duration(seconds) * time.Second
	}
	if raw := values["CATCHUP_WINDOW"]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%s: CATCHUP_WINDOW %q is not a positive duration", manifest, raw)
		}
		j.CatchupWindow = d
	}

	if _, err := os.Stat(j.RunScript()); err != nil {
		return nil, fmt.Errorf("%s: no run.sh (%w)", dir, err)
	}
	return j, nil
}

// LoadAll reads every job directory under jobsRoot.
//
// One bad manifest does not hide the others: the error names the job and the rest
// still load, so a typo in a new job cannot silently stop the ones that work.
func LoadAll(jobsRoot, artifactsRoot string) ([]*Job, []error) {
	entries, err := os.ReadDir(jobsRoot)
	if err != nil {
		return nil, []error{fmt.Errorf("read jobs directory: %w", err)}
	}

	var jobs []*Job
	var problems []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(jobsRoot, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "job.env")); err != nil {
			continue // not a job directory
		}
		j, err := Load(dir, artifactsRoot)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Name < jobs[b].Name })
	return jobs, problems
}

// ParseIssueLinks parses ISSUE_LINKS: comma- or semicolon-separated KEY=URL
// pairs. Keys are upper-case letters/digits; URLs must be http(s). Anything
// else is dropped rather than failing the manifest -- a bad link config should
// cost a link, not a job.
func ParseIssueLinks(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		key, url, ok := strings.Cut(strings.TrimSpace(pair), "=")
		key, url = strings.TrimSpace(key), strings.TrimSpace(url)
		if !ok || key == "" || !issueKeyPattern.MatchString(key) ||
			!(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
			continue
		}
		out[key] = url
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
