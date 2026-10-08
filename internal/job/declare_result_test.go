package job

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A job declares its verdict by writing OPS_RESULT_FILE, but the runner also reads
// run.sh's exit status, and treats "declared ok, exited non-zero" as a failed run.
// That makes declare_result's own return status load-bearing: it is the last command
// on every success path, so run.sh's implicit exit status is whatever it returns.
//
// The original idiom -- [ -n "${4:-}" ] && echo "detail=$4" -- returns 1 whenever the
// optional argument is absent, which silently turned every successful meeting-prep run
// into a failed one. These tests pin the fix across every job at once.

var declareResultFn = regexp.MustCompile(`(?ms)^declare_result\(\) \{.*?^\}`)

// jobScripts returns each templates/*/run.sh that defines declare_result, keyed by job name.
func jobScripts(t *testing.T) map[string]string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "templates", "*", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no templates/*/run.sh found")
	}
	out := map[string]string{}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if fn := declareResultFn.FindString(string(b)); fn != "" {
			out[filepath.Base(filepath.Dir(m))] = fn
		}
	}
	if len(out) == 0 {
		t.Fatal("no run.sh defines declare_result")
	}
	return out
}

// TestDeclareResultReturnsZero calls each job's own declare_result at every arity it
// supports and asserts it returns 0. An arity that skips an optional argument must not
// leave a non-zero status behind for the runner to read as a failure.
func TestDeclareResultReturnsZero(t *testing.T) {
	for name, fn := range jobScripts(t) {
		for _, args := range [][]string{
			{"ok", "just a headline"},
			{"ok", "headline", "artifact-or-detail.md"},
			{"ok", "headline", "artifact.md", "and a detail"},
			{"no-change", "headline", "", ""},
		} {
			t.Run(name+"/"+strings.Join(args, "|"), func(t *testing.T) {
				resultFile := filepath.Join(t.TempDir(), "result.env")
				script := "set -uo pipefail\nOPS_RESULT_FILE=\"$1\"\nshift\n" + fn + "\ndeclare_result \"$@\"\n"

				cmd := exec.Command("/bin/bash", append([]string{"-c", script, "test", resultFile}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("declare_result returned non-zero: %v\n%s", err, out)
				}

				b, err := os.ReadFile(resultFile)
				if err != nil {
					t.Fatalf("no result file written: %v", err)
				}
				if !strings.Contains(string(b), "outcome="+args[0]) {
					t.Errorf("result file missing outcome=%s:\n%s", args[0], b)
				}
				if !strings.Contains(string(b), "headline="+args[1]) {
					t.Errorf("result file missing headline:\n%s", b)
				}
			})
		}
	}
}

// TestDeclareResultAvoidsAndGuard is the cheap, direct check on the idiom itself: a
// future edit that reintroduces [ -n ... ] && echo would reintroduce the bug even if it
// happened to pass the arity test above with all arguments supplied.
func TestDeclareResultAvoidsAndGuard(t *testing.T) {
	bad := regexp.MustCompile(`\[ -n "\$\{\d:-\}" \] &&`)
	for name, fn := range jobScripts(t) {
		if bad.MatchString(fn) {
			t.Errorf("%s/run.sh: declare_result uses [ -n ... ] && echo, which returns 1 when the optional argument is absent; use [ -z ... ] || echo", name)
		}
	}
}
