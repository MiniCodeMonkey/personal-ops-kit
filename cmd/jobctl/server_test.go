package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/marks"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/schedule"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ui"
)

// The favicon route has to win over the "/" catch-all, which otherwise 404s every
// path but the root. Exercised through Handler so the routing is what is tested,
// not just the handler function.
func TestFaviconRoute(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Header().Get("Content-Type"), "image/svg+xml"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if rec.Body.Len() == 0 {
		t.Error("body is empty")
	}
}

// --------------------------------------------------------------- notification click

// notifiedJob builds a job whose ledger holds one finished run, and writes the
// artifact file so the redirect target is a file that would really be served.
// finishedAt is explicit because which result a click resolves to is decided by it.
func notifiedJob(t *testing.T, name, artifact string, outcome ledger.Outcome, finishedAt time.Time) *job.Job {
	t.Helper()
	dir := t.TempDir()
	j := &job.Job{Name: name, ArtifactDir: dir, ArtifactGlob: "*.html"}
	if artifact != "" {
		if err := os.WriteFile(filepath.Join(dir, artifact), []byte("<html></html>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := ledger.Record{
		Type: "run", Job: name, RunID: name + "-run-1", Outcome: outcome,
		Headline: "something happened", Artifact: artifact,
		StartedAt: finishedAt, FinishedAt: finishedAt,
	}
	if err := ledger.Append(j.LedgerPath(), rec); err != nil {
		t.Fatal(err)
	}
	return j
}

func hoursAgo(n int) time.Time { return time.Now().Add(-time.Duration(n) * time.Hour) }

func openTarget(t *testing.T, jobs ...*job.Job) string {
	t.Helper()
	s := &Server{daemon: &Daemon{jobs: jobs}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	return rec.Header().Get("Location")
}

func openTargetAt(t *testing.T, path string, jobs ...*job.Job) string {
	t.Helper()
	s := &Server{daemon: &Daemon{jobs: jobs}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	return rec.Header().Get("Location")
}

// A notification names the run it was posted for, and the click goes there -- even
// when a newer unread result exists and even after the run has been read, because the
// notification on screen is the one the click means.
func TestOpenHonoursTheRunNamedByTheNotification(t *testing.T) {
	old := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(5))
	recent := notifiedJob(t, "auctions", "digest.html", ledger.Warning, hoursAgo(1))

	got := openTargetAt(t, "/open?job=research&run=research-run-1", old, recent)
	if want := "/artifact/research/memo.html"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	s := &Server{daemon: &Daemon{jobs: []*job.Job{old, recent}}}
	s.acknowledgeSeen(old, func(run ledger.Run) bool { return run.Artifact == "memo.html" })
	got = openTargetAt(t, "/open?job=research&run=research-run-1", old, recent)
	if want := "/artifact/research/memo.html"; got != want {
		t.Errorf("after ack, Location = %q, want %q", got, want)
	}
}

// A run without an artifact still has somewhere to land: its entry on the job page.
func TestOpenNamedRunWithoutArtifactGoesToTheJobPage(t *testing.T) {
	j := notifiedJob(t, "research", "", ledger.Failed, hoursAgo(1))
	got := openTargetAt(t, "/open?job=research&run=research-run-1", j)
	if want := "/jobs/research?run=research-run-1"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// Parameters that do not name a real notified run cannot be trusted; fall back to the
// newest-unread rule rather than 404 a click.
func TestOpenIgnoresUnknownOrQuietNamedRuns(t *testing.T) {
	loud := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(2))
	quiet := notifiedJob(t, "auctions", "digest.html", ledger.NoChange, hoursAgo(1))

	for _, path := range []string{
		"/open?job=research&run=does-not-exist",
		"/open?job=nope&run=research-run-1",
		"/open?job=auctions&run=auctions-run-1", // no-change never notifies
		"/open?job=research",                    // run missing
	} {
		if got, want := openTargetAt(t, path, loud, quiet), "/artifact/research/memo.html"; got != want {
			t.Errorf("%s: Location = %q, want %q", path, got, want)
		}
	}
}

// The case that matters at breakfast: one unread result, so the click lands on the
// memo itself rather than making someone find it.
func TestOpenGoesStraightToTheOnlyUnreadResult(t *testing.T) {
	j := notifiedJob(t, "research", "2026-08-12-memo.html", ledger.OK, hoursAgo(1))
	if got, want := openTarget(t, j), "/artifact/research/2026-08-12-memo.html"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// A failed run has no memo but is exactly what a click needs to reach, so it falls
// back to the run's own entry rather than the index.
func TestOpenFallsBackToTheRunWhenThereIsNoArtifact(t *testing.T) {
	j := notifiedJob(t, "research", "", ledger.Failed, hoursAgo(1))
	if got, want := openTarget(t, j), "/jobs/research?run=research-run-1"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// The click carries nothing saying which notification it came from, so with several
// unread it goes to the newest -- a notification is clicked while it is the one on
// screen, and yesterday's unopened result must not outrank tonight's.
func TestOpenGoesToTheNewestUnreadResult(t *testing.T) {
	old := notifiedJob(t, "auctions", "digest.html", ledger.Warning, hoursAgo(48))
	recent := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))

	// Both orderings, so the answer comes from the timestamps and not from which job
	// happened to be loaded first.
	if got, want := openTarget(t, old, recent), "/artifact/research/memo.html"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if got, want := openTarget(t, recent, old), "/artifact/research/memo.html"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// Nothing unread means the click cannot be resolved to a result at all.
func TestOpenGoesToTheIndexWhenNothingIsUnread(t *testing.T) {
	j := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j}}}
	s.acknowledgeSeen(j, func(run ledger.Run) bool { return run.Artifact == "memo.html" })

	if got, want := openTarget(t, j), "/"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// Outcomes that never notify cannot be behind a click, so they must not be treated
// as the one unread result and hijack it.
func TestOpenIgnoresResultsThatNeverNotify(t *testing.T) {
	quiet := notifiedJob(t, "auctions", "digest.html", ledger.NoChange, hoursAgo(1))
	if got, want := openTarget(t, quiet), "/"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	loud := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(2))
	if got, want := openTarget(t, quiet, loud), "/artifact/research/memo.html"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// Reading the result is the acknowledgement. Without this the morning nudge re-posts
// something already read every day until it ages out.
func TestAcknowledgeSeenMarksTheRunOnlyOnce(t *testing.T) {
	j := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j}}}
	byArtifact := func(run ledger.Run) bool { return run.Artifact == "memo.html" }

	s.acknowledgeSeen(j, byArtifact)
	runs, err := ledger.Load(j.LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].Acknowledged() {
		t.Fatalf("run not acknowledged: %+v", runs)
	}
	if n := len(ledger.Unacknowledged(runs)); n != 0 {
		t.Errorf("unacknowledged = %d, want 0", n)
	}

	// A second read must not append another ack record; the ledger is a history, and
	// re-reading a memo is not a new event.
	before := ledgerLines(t, j.LedgerPath())
	s.acknowledgeSeen(j, byArtifact)
	if after := ledgerLines(t, j.LedgerPath()); after != before {
		t.Errorf("ledger grew from %d to %d lines on a second read", before, after)
	}
}

func ledgerLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(b)), "\n"))
}

// --------------------------------------------------------------- artifacts hub

func TestBookmarkToggleRoundTrips(t *testing.T) {
	j := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j}}, marks: marks.New(filepath.Join(t.TempDir(), "bookmarks.json"))}
	h := s.Handler()

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/bookmark/research/memo.html", strings.NewReader("back=/artifacts"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := post()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/artifacts" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if !s.marks.Has("research", "memo.html") {
		t.Error("not bookmarked after POST")
	}
	post()
	if s.marks.Has("research", "memo.html") {
		t.Error("still bookmarked after second POST")
	}

	// A GET must not toggle anything.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/bookmark/research/memo.html", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", rec.Code)
	}
}

func TestBookmarkRefusesUnknownArtifacts(t *testing.T) {
	j := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j}}, marks: marks.New(filepath.Join(t.TempDir(), "bookmarks.json"))}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/bookmark/research/..%2Fledger.jsonl", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestArtifactRowsCarryReadAndMarkState(t *testing.T) {
	unread := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	read := notifiedJob(t, "auction", "digest.html", ledger.OK, hoursAgo(2))
	s := &Server{daemon: &Daemon{jobs: []*job.Job{unread, read}}, marks: marks.New(filepath.Join(t.TempDir(), "bookmarks.json"))}
	s.acknowledgeSeen(read, func(run ledger.Run) bool { return true })
	if _, err := s.marks.Toggle("auction", "digest.html"); err != nil {
		t.Fatal(err)
	}

	rows := s.allArtifacts()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	byFile := map[string]artifactRow{}
	for _, r := range rows {
		byFile[r.File] = r
	}
	if r := byFile["memo.html"]; !r.Unread || r.Marked {
		t.Errorf("memo: %+v", r)
	}
	if r := byFile["digest.html"]; r.Unread || !r.Marked {
		t.Errorf("digest: %+v", r)
	}

	if got := filterArtifacts(rows, "unread"); len(got) != 1 || got[0].File != "memo.html" {
		t.Errorf("unread filter = %+v", got)
	}
	if got := filterArtifacts(rows, "marked"); len(got) != 1 || got[0].File != "digest.html" {
		t.Errorf("marked filter = %+v", got)
	}
	if got := filterArtifacts(rows, "all"); len(got) != 2 {
		t.Errorf("all filter = %+v", got)
	}
}

func TestArtifactsPageRenders(t *testing.T) {
	j := notifiedJob(t, "research", "memo.html", ledger.OK, hoursAgo(1))
	tpl, err := ui.Templates()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := schedule.Parse("0 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	j.Schedule = spec
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j}, state: &daemonState{}}, tpl: tpl}
	for _, path := range []string{"/", "/artifacts?show=all", "/artifacts?show=marked"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "/artifact/research/memo.html") && path != "/artifacts?show=marked" {
			t.Errorf("%s: artifact link missing", path)
		}
	}
}

// ------------------------------------------------------------------ issue cards

// A job that sets ISSUE_LINKS and writes state/issues.json gets hover cards; the
// endpoint serves that file only -- never a job without links, never an id that
// isn't in the index, never a malformed id.
func TestIssueEndpointServesTheJobsOwnIndex(t *testing.T) {
	dir := t.TempDir()
	j := &job.Job{Name: "triage", ArtifactDir: dir, ArtifactGlob: "*.md",
		IssueLinks: map[string]string{"ENG": "https://linear.app/acme/issue/"}}
	if err := os.MkdirAll(j.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	index := `{"date":"2026-08-26","cycle":{"id":"c20","number":20},
	  "issues":[{"id":"ENG-7","title":"Fix it","status":"Todo","assignee":"Sylvester","labels":["Bug"],"cycleId":"c20"},
	            {"id":"ENG-8","title":"Other","status":"Backlog","cycleId":null}]}`
	if err := os.WriteFile(filepath.Join(j.StateDir(), "issues.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := &job.Job{Name: "plain", ArtifactDir: t.TempDir(), ArtifactGlob: "*.md"}
	s := &Server{daemon: &Daemon{jobs: []*job.Job{j, plain}}}

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get("/api/issue/triage/ENG-7")
	if rec.Code != http.StatusOK {
		t.Fatalf("ENG-7: status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"title":"Fix it"`, `"url":"https://linear.app/acme/issue/ENG-7"`, `"number":20`, `"asOf":"2026-08-26"`} {
		if !strings.Contains(body, want) {
			t.Errorf("ENG-7 body missing %s: %s", want, body)
		}
	}
	if rec := get("/api/issue/triage/ENG-8"); strings.Contains(rec.Body.String(), `"cycle"`) {
		t.Errorf("ENG-8 is not in the current cycle and must not carry one: %s", rec.Body.String())
	}
	for _, path := range []string{"/api/issue/triage/ENG-9", "/api/issue/plain/ENG-7", "/api/issue/triage/eng-7"} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

// Artifacts of a job with ISSUE_LINKS render bare identifiers as links; other
// jobs' artifacts are untouched.
func TestArtifactLinksIssuesOnlyWhenConfigured(t *testing.T) {
	linked := &job.Job{Name: "triage", ArtifactDir: t.TempDir(), ArtifactGlob: "*.md",
		IssueLinks: map[string]string{"ENG": "https://linear.app/acme/issue/"}}
	plain := &job.Job{Name: "plain", ArtifactDir: t.TempDir(), ArtifactGlob: "*.md"}
	for _, j := range []*job.Job{linked, plain} {
		if err := os.WriteFile(filepath.Join(j.ArtifactDir, "m.md"), []byte("| Issue |\n| :-- |\n| ENG-7 |\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tpl, err := ui.Templates()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := schedule.Parse("0 7 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	linked.Schedule, plain.Schedule = spec, spec
	s := &Server{daemon: &Daemon{jobs: []*job.Job{linked, plain}, state: &daemonState{}}, tpl: tpl,
		marks: marks.New(filepath.Join(t.TempDir(), "b.json"))}
	get := func(path string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	if got := get("/artifact/triage/m.md"); !strings.Contains(got, `data-issue="ENG-7"`) || !strings.Contains(got, `id="issue-card"`) || !strings.Contains(got, "<table>") {
		t.Errorf("linked job's artifact lacks issue link, card container or table")
	}
	if got := get("/artifact/plain/m.md"); strings.Contains(got, `data-issue=`) || strings.Contains(got, `id="issue-card"`) {
		t.Errorf("plain job's artifact must not get issue links or a card")
	}
}
