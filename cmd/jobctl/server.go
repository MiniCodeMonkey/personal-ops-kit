package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/marks"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/md"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/notify"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/runner"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ui"
)

const (
	maxRunsShown = 12
	maxLogBytes  = 200 << 10
	stripLength  = 14
)

// Server renders the dashboard and exposes the API the CLI and the page both use.
type Server struct {
	daemon *Daemon
	tpl    *template.Template
	origin string
	marks  *marks.Store
}

func NewServer(d *Daemon) (*Server, error) {
	tpl, err := ui.Templates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		daemon: d, tpl: tpl, origin: d.cfg.DashboardURL(),
		marks: marks.New(filepath.Join(d.cfg.ArtifactsRoot, "bookmarks.json")),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	// More specific than the "/" catch-all, so ServeMux routes it here without
	// handleIndex needing to know the path exists.
	mux.HandleFunc("/favicon.svg", s.handleFavicon)
	mux.HandleFunc("/jobs/", s.handleJob)
	mux.HandleFunc("/artifacts", s.handleArtifacts)
	mux.HandleFunc("/artifact/", s.handleArtifact)
	mux.HandleFunc("/api/bookmark/", s.mutate(s.handleBookmark))
	mux.HandleFunc("/open", s.handleOpen)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/issue/", s.handleIssue)
	mux.HandleFunc("/api/jobs", s.handleAPIJobs)
	mux.HandleFunc("/api/run/", s.mutate(s.handleRun))
	mux.HandleFunc("/api/stop/", s.mutate(s.handleStop))
	mux.HandleFunc("/api/pause/", s.mutate(s.handlePause(true)))
	mux.HandleFunc("/api/resume/", s.mutate(s.handlePause(false)))
	return s.loopbackHost(mux)
}

// loopbackHost refuses requests whose Host is not the dashboard's own address.
// Binding to 127.0.0.1 keeps other machines out, but a website can point its own
// hostname at 127.0.0.1 (DNS rebinding) and then read the dashboard as same-origin.
// Such requests still carry the attacker's hostname, so checking Host stops them.
func (s *Server) loopbackHost(next http.Handler) http.Handler {
	if s.origin == "" {
		return next
	}
	host := strings.TrimPrefix(s.origin, "http://")
	_, port, _ := strings.Cut(host, ":")
	allowed := map[string]bool{host: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mutate guards every state-changing endpoint.
//
// There is no login: the server binds loopback only, which is a deliberate choice for
// a single-user laptop. What that does not defend against is a website you happen to
// be visiting POSTing to 127.0.0.1 while the daemon is up, so anything that changes
// state must be a POST and must carry same-origin proof. Modern browsers send both
// headers on cross-origin form posts, which is exactly the case being blocked.
func (s *Server) mutate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "ok %d", time.Now().Unix())
}

// handleIssue serves one issue record from a job's state/issues.json -- the
// file a job writes when it wants hover cards on its issue links (see README,
// "Issue links"). Local data only: nothing is fetched from the tracker here.
func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/issue/")
	jobName, id, found := strings.Cut(rest, "/")
	if !found || !issueIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	j := s.daemon.findJob(jobName)
	if j == nil || len(j.IssueLinks) == 0 {
		http.NotFound(w, r)
		return
	}
	body, err := os.ReadFile(filepath.Join(j.StateDir(), "issues.json"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var index struct {
		Date   string           `json:"date"`
		Cycle  map[string]any   `json:"cycle"`
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		http.Error(w, "issues.json unreadable", http.StatusInternalServerError)
		return
	}
	for _, is := range index.Issues {
		if is["id"] == id {
			out := map[string]any{"issue": is, "asOf": index.Date}
			if cid, _ := is["cycleId"].(string); cid != "" && index.Cycle != nil && index.Cycle["id"] == cid {
				out["cycle"] = index.Cycle
			}
			if base, ok := j.IssueLinks[strings.SplitN(id, "-", 2)[0]]; ok {
				out["url"] = base + id
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
	}
	http.NotFound(w, r)
}

var issueIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[0-9]{1,7}$`)

func (s *Server) handleAPIJobs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, j := range s.daemon.snapshotJobs() {
		state := "scheduled"
		switch {
		case s.daemon.isRunning(j.Name):
			state = "running"
		case s.daemon.state.isPaused(j.Name):
			state = "paused"
		}
		next := "-"
		if t, ok := j.Schedule.Next(time.Now()); ok {
			next = t.Format("Mon 15:04")
		}
		last := "never"
		if runs, _ := ledger.Load(j.LedgerPath()); len(runs) > 0 {
			last = fmt.Sprintf("%s %s", runs[0].StartedAt.Format("2 Jan 15:04"), runs[0].Outcome)
		}
		fmt.Fprintf(w, "%-18s %-10s %-14s next %-12s last %s\n", j.Name, state, j.Schedule.String(), next, last)
	}
	return
}

func (s *Server) jobFromPath(prefix string, r *http.Request) *job.Job {
	name := strings.TrimPrefix(r.URL.Path, prefix)
	name = strings.Trim(name, "/")
	if name == "" {
		return nil
	}
	return s.daemon.findJob(name)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	j := s.jobFromPath("/api/run/", r)
	if j == nil {
		http.Error(w, "no such job", http.StatusNotFound)
		return
	}
	if s.daemon.isRunning(j.Name) {
		http.Error(w, "already running", http.StatusConflict)
		return
	}
	// "Run anyway" bypasses the gate by running a copy of the job with gating off,
	// rather than by threading a flag through the runner. Same code path, one
	// setting different.
	target := *j
	if r.URL.Query().Get("force") == "1" {
		target.Gated = false
	}
	go s.daemon.start(context.Background(), &target, runner.TriggerManual)
	s.redirectToJob(w, r, j.Name)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	j := s.jobFromPath("/api/stop/", r)
	if j == nil {
		http.Error(w, "no such job", http.StatusNotFound)
		return
	}
	if active := s.daemon.activeRun(j.Name); active != nil {
		active.Cancel()
	}
	s.redirectToJob(w, r, j.Name)
}

func (s *Server) handlePause(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := "/api/resume/"
		if paused {
			prefix = "/api/pause/"
		}
		j := s.jobFromPath(prefix, r)
		if j == nil {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		s.daemon.state.setPaused(j.Name, paused)
		s.redirectToJob(w, r, j.Name)
	}
}

func (s *Server) redirectToJob(w http.ResponseWriter, r *http.Request, name string) {
	if r.Header.Get("Accept") == "text/plain" {
		fmt.Fprintln(w, "ok")
		return
	}
	http.Redirect(w, r, "/jobs/"+url.PathEscape(name), http.StatusSeeOther)
}

// handleFavicon serves the compiled-in site icon. Given a long max-age because the
// asset only changes when the binary is rebuilt.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(ui.Favicon())
}

// handleArtifacts is the reading list: every artifact across every job, newest
// first, with what is unread up front. It exists so a memo can be found without first
// remembering which job wrote it.
func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	show := r.URL.Query().Get("show")
	if show == "" {
		show = "unread"
	}
	s.render(w, r, nil, "", show)
}

// handleBookmark toggles an artifact's star and sends the browser back where it came
// from. The artifact must pass the same gates handleArtifact applies, so a mark can
// only ever name something the dashboard would serve.
func (s *Server) handleBookmark(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/bookmark/")
	jobName, file, found := strings.Cut(rest, "/")
	j := s.daemon.findJob(jobName)
	if !found || file == "" || j == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := safeArtifactPath(j.ArtifactDir, file); !ok || strings.Contains(file, "/") || !matchesGlob(j.ArtifactGlob, file) {
		http.NotFound(w, r)
		return
	}
	if _, err := s.marks.Toggle(j.Name, file); err != nil {
		log.Printf("%s: bookmark %s: %v", j.Name, file, err)
		http.Error(w, "could not save bookmark", http.StatusInternalServerError)
		return
	}
	back := r.FormValue("back")
	// Only a local path is followed: the value comes from the page, but a redirect to
	// wherever a form says is still not something to hand out.
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/artifact/" + url.PathEscape(j.Name) + "/" + url.PathEscape(file)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, nil, "", "unread")
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j := s.jobFromPath("/jobs/", r)
	if j == nil {
		http.NotFound(w, r)
		return
	}
	runID := r.URL.Query().Get("run")
	// Asking for a specific run is looking at it. The job page on its own is not:
	// arriving at the index and glancing down the list is not the same as reading
	// the result, and acknowledging it there would quietly cancel the morning nudge
	// for something never actually read.
	if runID != "" {
		s.acknowledgeSeen(j, func(run ledger.Run) bool { return run.RunID == runID })
	}
	s.render(w, r, j, runID, "")
}

// handleOpen is where a clicked notification lands.
//
// The notification carries ?job=&run= naming the result it was posted for, and that is
// where the click goes: the artifact the run produced, or its entry on the job page.
// Going through here rather than straight to the artifact is because only the daemon
// knows what the run left behind and what it is called.
//
// Without those parameters -- the app was launched some other way, or macOS delivered
// the click without its notification -- the destination is the newest unread result.
// With several unread the click cannot prove which one it came from, but the newest is
// very nearly always right. Being wrong is cheap and self-correcting: the result
// actually wanted stays unread, so it keeps its place in the dashboard and its morning
// nudge. With nothing unread the index is the honest answer.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if target, ok := s.openTargetFor(r.URL.Query().Get("job"), r.URL.Query().Get("run")); ok {
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	target := "/"
	var newest time.Time
	for _, j := range s.daemon.snapshotJobs() {
		runs, err := ledger.Load(j.LedgerPath())
		if err != nil {
			continue
		}
		for _, run := range ledger.Unacknowledged(runs) {
			// Only results that were worth a notification can be behind this click.
			if !notify.Worth(notify.Outcome(run.Outcome)) {
				continue
			}
			if !run.FinishedAt.After(newest) {
				continue
			}
			newest, target = run.FinishedAt, s.runURL(j, run)
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// openTargetFor resolves the run a notification named. A run that is unknown, or one
// that would never have been notified about, is not something a click can have come
// from, so it falls through to the newest-unread rule rather than trusting the URL.
func (s *Server) openTargetFor(jobName, runID string) (string, bool) {
	if jobName == "" || runID == "" {
		return "", false
	}
	j := s.daemon.findJob(jobName)
	if j == nil {
		return "", false
	}
	runs, err := ledger.Load(j.LedgerPath())
	if err != nil {
		return "", false
	}
	for _, run := range runs {
		if run.RunID == runID && notify.Worth(notify.Outcome(run.Outcome)) {
			return s.runURL(j, run), true
		}
	}
	return "", false
}

// runURL is where a run is best read: the artifact it produced if it left one, and the
// run's own entry on the job page otherwise. A failed run has no memo but is exactly
// the kind of thing a click needs to reach.
func (s *Server) runURL(j *job.Job, run ledger.Run) string {
	if run.Artifact != "" {
		file := filepath.Base(run.Artifact)
		// Same two gates handleArtifact applies, so a click can never be redirected
		// somewhere handleArtifact will refuse to serve.
		if _, ok := safeArtifactPath(j.ArtifactDir, file); ok && matchesGlob(j.ArtifactGlob, file) {
			return "/artifact/" + url.PathEscape(j.Name) + "/" + url.PathEscape(file)
		}
	}
	return "/jobs/" + url.PathEscape(j.Name) + "?run=" + url.QueryEscape(run.RunID)
}

// acknowledgeSeen marks the first run matching sel as seen and takes its notification
// out of Notification Centre.
//
// Opening the result is the acknowledgement; there is no button to press, because a
// button that has to be pressed is one that will not be, and the morning nudge would
// then repeat for a week over something already read.
//
// This mutates on a GET, which every other state change here refuses to do. The reason
// the mutate guard exists is that a website you are visiting could POST to loopback and
// start a job; the worst a forged read here can do is clear a banner you were going to
// see in the dashboard anyway, which is not worth making reading a memo a two-step act.
func (s *Server) acknowledgeSeen(j *job.Job, sel func(ledger.Run) bool) {
	runs, err := ledger.Load(j.LedgerPath())
	if err != nil {
		return
	}
	for _, run := range runs {
		if !sel(run) {
			continue
		}
		if run.Acknowledged() {
			return
		}
		if err := ledger.Acknowledge(j.LedgerPath(), j.Name, run.RunID, time.Now()); err != nil {
			log.Printf("%s: acknowledge %s: %v", j.Name, run.RunID, err)
			return
		}
		s.daemon.clearNotification(j.Name)
		return
	}
}

// handleArtifact serves a job's output.
//
// The path is resolved and confirmed to sit inside the job's own artifact directory
// before anything is read, so a crafted path cannot walk out of it.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/artifact/")
	jobName, file, found := strings.Cut(rest, "/")
	if !found || file == "" {
		http.NotFound(w, r)
		return
	}
	j := s.daemon.findJob(jobName)
	if j == nil {
		http.NotFound(w, r)
		return
	}
	full, ok := safeArtifactPath(j.ArtifactDir, file)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Confinement to the job's directory is necessary but not sufficient: that
	// directory also holds the ledger, the run logs and the job's own state, none of
	// which are artifacts. Serve only what the job declares as output, and only from
	// the top level, so state/ and logs/ are not reachable at all.
	if strings.Contains(file, "/") || !matchesGlob(j.ArtifactGlob, filepath.Base(file)) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// raw=1 is what the reader's iframe fetches for an HTML memo. Served with a
	// restrictive CSP: memos are generated by an unattended agent from web sources,
	// and are meant to be self-contained anyway.
	if r.URL.Query().Get("raw") == "1" {
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, full)
		return
	}

	// Reading the memo is the acknowledgement. Below the raw=1 branch, so it is the
	// reader's page request that counts and not the iframe fetch inside it.
	s.acknowledgeSeen(j, func(run ledger.Run) bool {
		return run.Artifact != "" && filepath.Base(run.Artifact) == file
	})

	s.renderArtifact(w, r, j, file, full)
}

func safeArtifactPath(dir, file string) (string, bool) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	full, err := filepath.Abs(filepath.Join(base, filepath.Clean("/"+file)))
	if err != nil {
		return "", false
	}
	// Compare against the directory with a separator appended, so /ops/jobfoo cannot
	// masquerade as being inside /ops/job.
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

// ---------------------------------------------------------------- view models

type pageData struct {
	CSS      template.CSS
	Jobs     []jobRow
	Selected *jobView
	// Hub is the reading list shown on the index, already filtered by Show.
	Hub    []artifactRow
	Groups []artifactGroup
	// Upcoming is what the empty inbox shows instead of nothing: the next few runs.
	Upcoming        []upcomingRow
	Show            string
	UnreadCount     int
	MarkedCount     int
	TotalCount      int
	Artifact        *artifactView
	Reading         bool
	Daemon          daemonView
	Problems        []string
	SelectedRunning bool
}

type daemonView struct {
	Status    string
	Degraded  bool
	GapNotice string
}

type jobRow struct {
	Name, Icon string
	Subtitle   string
	DotClass   string
	Strip      []string
	StripLabel string
	Selected   bool
}

type jobView struct {
	Name, Description, Model string
	Icon                     string
	Gated, Paused, Running   bool
	ScheduleText             string
	ScheduleHuman            string
	About                    template.HTML
	NextRun, LastRun         string
	LastArtifact             string
	Notice                   string
	Runs                     []runRow
	Log                      string
	LogTitle                 string
}

type runRow struct {
	ID, When, Duration, OutcomeText, PillClass, Summary string
	Selected                                            bool
}

type artifactRow struct {
	Job, Icon, File, Title, Date, Size string
	OutcomeText, PillClass             string
	Unread, Marked                     bool
	// sortKey orders the hub: the run's start time, newest first.
	sortKey time.Time
}

type upcomingRow struct {
	Name, Icon, When, Until string
}

type artifactView struct {
	Job, Icon, File, Title, Path string
	IsHTML                       bool
	HTML                         template.HTML
	// IssueCards is set when the job configured ISSUE_LINKS, so the reader wires
	// hover cards on issue links (data from /api/issue/<job>/<id>).
	IssueCards bool
	Marked     bool
	// Command is the shell line "Open in Claude Code" copies.
	Command string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, selected *job.Job, runID, show string) {
	now := time.Now()
	data := pageData{
		CSS:    ui.CSS(),
		Daemon: s.daemonView(now),
		Show:   show,
	}
	for _, e := range s.daemon.problems() {
		data.Problems = append(data.Problems, e.Error())
	}

	var all []artifactRow
	for _, j := range s.daemon.snapshotJobs() {
		runs, _ := ledger.Load(j.LedgerPath())
		data.Jobs = append(data.Jobs, s.jobRow(j, runs, selected))
		all = append(all, s.artifactRows(j, runs)...)
	}
	sortArtifacts(all)
	data.TotalCount = len(all)
	for _, a := range all {
		if a.Unread {
			data.UnreadCount++
		}
		if a.Marked {
			data.MarkedCount++
		}
	}
	if show != "" {
		data.Hub = filterArtifacts(all, show)
		data.Groups = groupArtifacts(data.Hub, now)
		if len(data.Hub) == 0 {
			data.Upcoming = s.upcoming(now, 3)
		}
	}

	if selected != nil {
		view := s.jobView(selected, runID, now)
		data.Selected = view
		data.SelectedRunning = view.Running
	}
	s.write(w, data)
}

func (s *Server) write(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tpl.ExecuteTemplate(w, "page", data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (s *Server) daemonView(now time.Time) daemonView {
	notice := s.daemon.gapNotice(now)
	return daemonView{
		Status:    "Running",
		Degraded:  notice != "",
		GapNotice: notice,
	}
}

func (s *Server) jobRow(j *job.Job, runs []ledger.Run, selected *job.Job) jobRow {
	row := jobRow{Name: j.Name, Icon: j.Icon, Selected: selected != nil && selected.Name == j.Name}

	switch {
	case s.daemon.isRunning(j.Name):
		row.DotClass, row.Subtitle = "bg-accent", "running now"
	case s.daemon.state.isPaused(j.Name):
		row.DotClass, row.Subtitle = "bg-rule-strong", "paused"
	default:
		row.DotClass = "bg-good"
		if next, ok := j.Schedule.Next(time.Now()); ok {
			row.Subtitle = fmt.Sprintf("%s · next %s", next.Format("15:04"), humanUntil(time.Until(next)))
		}
	}

	// Oldest first, so the strip reads left to right like a timeline.
	counts := map[string]int{}
	for i := len(runs) - 1; i >= 0 && len(row.Strip) < stripLength; i-- {
		tick := stripTick(runs[i].Outcome)
		row.Strip = append(row.Strip, tick)
		counts[tick]++
	}
	row.StripLabel = stripLabel(counts, len(row.Strip))
	return row
}

func stripTick(o ledger.Outcome) string {
	switch o {
	case ledger.OK:
		return "ok"
	case ledger.Warning:
		return "warn"
	case ledger.Failed:
		return "fail"
	default:
		return "skip"
	}
}

func stripLabel(counts map[string]int, total int) string {
	if total == 0 {
		return "No runs yet"
	}
	var parts []string
	for _, k := range []string{"ok", "warn", "fail", "skip"} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], map[string]string{
				"ok": "succeeded", "warn": "with warnings", "fail": "failed", "skip": "skipped",
			}[k]))
		}
	}
	return fmt.Sprintf("Last %d runs: %s", total, strings.Join(parts, ", "))
}

func (s *Server) jobView(j *job.Job, runID string, now time.Time) *jobView {
	runs, _ := ledger.Load(j.LedgerPath())
	v := &jobView{
		Name: j.Name, Description: j.Description, Model: j.Model, Icon: j.Icon,
		Gated:         j.Gated,
		Paused:        s.daemon.state.isPaused(j.Name),
		Running:       s.daemon.isRunning(j.Name),
		ScheduleText:  j.Schedule.String(),
		ScheduleHuman: j.Schedule.Human(),
		NextRun:       "Not scheduled while paused",
		LastRun:       "Never",
	}

	if about := j.About(); about != "" {
		v.About = template.HTML(md.ToHTML(about))
	}

	if !v.Paused {
		if next, ok := j.Schedule.Next(now); ok {
			v.NextRun = fmt.Sprintf("%s · in %s", next.Format("Mon 2 Jan 15:04"), humanUntil(time.Until(next)))
		}
	}

	// A job that keeps being skipped has stopped working without failing, which is
	// the failure this dashboard exists to make visible.
	if skipped := countLeadingSkips(runs); skipped >= 3 {
		v.Notice = fmt.Sprintf("Skipped %d runs in a row. This job has not produced anything since then -- check the reason below, or run it anyway.", skipped)
	}

	var selected *ledger.Run
	for i := range runs {
		if runs[i].RunID == runID {
			selected = &runs[i]
		}
	}
	if selected == nil && len(runs) > 0 {
		selected = &runs[0]
	}

	for i, r := range runs {
		if i >= maxRunsShown {
			break
		}
		v.Runs = append(v.Runs, runRow{
			ID: r.RunID, When: humanWhen(r.StartedAt), Duration: humanDuration(r),
			OutcomeText: outcomeText(r.Outcome), PillClass: pillClass(r.Outcome),
			Summary:  firstNonEmpty(r.Headline, r.Detail),
			Selected: selected != nil && r.RunID == selected.RunID,
		})
	}

	if selected != nil {
		v.LastRun = fmt.Sprintf("%s · %s", humanWhen(selected.StartedAt), humanDuration(*selected))
		v.LastArtifact = filepath.Base(selected.Artifact)
		if selected.Artifact == "" {
			v.LastArtifact = ""
		}
		v.LogTitle = humanWhen(selected.StartedAt)
		v.Log = readLogTail(filepath.Join(j.LogDir(), selected.LogFile))
	}
	if v.Running {
		if active := s.daemon.activeRun(j.Name); active != nil {
			v.LogTitle = "running · " + humanDuration(ledger.Run{Record: ledger.Record{StartedAt: active.Started, FinishedAt: now}})
		}
	}
	return v
}

func countLeadingSkips(runs []ledger.Run) int {
	n := 0
	for _, r := range runs {
		if r.Outcome != ledger.Gated {
			break
		}
		n++
	}
	return n
}

// upcoming lists the next n scheduled runs across unpaused jobs, soonest first.
func (s *Server) upcoming(now time.Time, n int) []upcomingRow {
	type due struct {
		name, icon string
		at         time.Time
	}
	var dues []due
	for _, j := range s.daemon.snapshotJobs() {
		if j.Schedule == nil || s.daemon.state.isPaused(j.Name) {
			continue
		}
		if next, ok := j.Schedule.Next(now); ok {
			dues = append(dues, due{j.Name, j.Icon, next})
		}
	}
	sort.Slice(dues, func(a, b int) bool { return dues[a].at.Before(dues[b].at) })
	var rows []upcomingRow
	for i, d := range dues {
		if i >= n {
			break
		}
		when := d.at.Format("Mon 15:04")
		if d.at.YearDay() == now.YearDay() && d.at.Year() == now.Year() {
			when = "Today " + d.at.Format("15:04")
		} else if d.at.Sub(now) < 36*time.Hour {
			when = "Tomorrow " + d.at.Format("15:04")
		}
		rows = append(rows, upcomingRow{Name: d.name, Icon: d.icon, When: when, Until: "in " + humanUntil(d.at.Sub(now))})
	}
	return rows
}

// allArtifacts is every artifact the dashboard would serve, across jobs, newest first.
func (s *Server) allArtifacts() []artifactRow {
	var all []artifactRow
	for _, j := range s.daemon.snapshotJobs() {
		runs, _ := ledger.Load(j.LedgerPath())
		all = append(all, s.artifactRows(j, runs)...)
	}
	sortArtifacts(all)
	return all
}

func sortArtifacts(rows []artifactRow) {
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].sortKey.After(rows[b].sortKey) })
}

// filterArtifacts narrows rows to one of the hub's views: unread, marked, read, or all.
func filterArtifacts(rows []artifactRow, show string) []artifactRow {
	var out []artifactRow
	for _, r := range rows {
		switch show {
		case "unread":
			if !r.Unread {
				continue
			}
		case "read":
			if r.Unread {
				continue
			}
		case "marked":
			if !r.Marked {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func (s *Server) artifactRows(j *job.Job, runs []ledger.Run) []artifactRow {
	var rows []artifactRow
	seen := map[string]bool{}
	for _, r := range runs {
		if r.Artifact == "" {
			continue
		}
		file := filepath.Base(r.Artifact)
		if seen[file] {
			continue
		}
		seen[file] = true
		full, ok := safeArtifactPath(j.ArtifactDir, file)
		if !ok || !matchesGlob(j.ArtifactGlob, file) {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		rows = append(rows, artifactRow{
			Job: j.Name, Icon: j.Icon, File: file,
			Title:       firstNonEmpty(r.Headline, file),
			Date:        r.StartedAt.Format("2006-01-02"),
			Size:        humanSize(info.Size()),
			OutcomeText: outcomeText(r.Outcome),
			PillClass:   pillClass(r.Outcome),
			Unread:      !r.Acknowledged(),
			Marked:      s.marks.Has(j.Name, file),
			sortKey:     r.StartedAt,
		})
	}
	return rows
}

func (s *Server) renderArtifact(w http.ResponseWriter, r *http.Request, j *job.Job, file, full string) {
	body, err := os.ReadFile(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := &artifactView{
		Job: j.Name, Icon: j.Icon, File: file, Path: full,
		Title:   strings.TrimSuffix(file, filepath.Ext(file)),
		IsHTML:  strings.EqualFold(filepath.Ext(file), ".html"),
		Marked:  s.marks.Has(j.Name, file),
		Command: claudeCommand(j, full),
	}
	if !view.IsHTML {
		view.HTML = template.HTML(md.LinkIssues(md.ToHTML(string(body)), j.IssueLinks))
		view.IssueCards = len(j.IssueLinks) > 0
	}

	data := pageData{CSS: ui.CSS(), Reading: true, Artifact: view, Daemon: s.daemonView(time.Now())}
	for _, a := range s.allArtifacts() {
		if a.Unread {
			data.UnreadCount++
		}
		if a.Marked {
			data.MarkedCount++
		}
	}
	s.write(w, data)
}

// ---------------------------------------------------------------- formatting

func readLogTail(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	size := info.Size()
	offset := int64(0)
	if size > maxLogBytes {
		offset = size - maxLogBytes
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return ""
	}
	out := string(buf)
	if offset > 0 {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		out = "… earlier output trimmed …\n" + out
	}
	return out
}

func outcomeText(o ledger.Outcome) string {
	if o == ledger.NoChange {
		return "no change"
	}
	if o == ledger.Gated {
		return "skipped"
	}
	return string(o)
}

func pillClass(o ledger.Outcome) string {
	switch o {
	case ledger.OK:
		return "pill-ok"
	case ledger.Warning:
		return "pill-warn"
	case ledger.Failed:
		return "pill-fail"
	default:
		return "pill-skip"
	}
}

func humanWhen(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	now := time.Now()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return "Today " + t.Format("15:04")
	}
	if t.YearDay() == now.YearDay()-1 && t.Year() == now.Year() {
		return "Yesterday " + t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

func humanDuration(r ledger.Run) string {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return "—"
	}
	d := r.FinishedAt.Sub(r.StartedAt)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func humanUntil(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// matchesGlob reports whether a filename is one the job declares as output. An
// unparseable pattern matches nothing rather than everything: a typo in a manifest
// should hide files, not expose them.
func matchesGlob(pattern, name string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}
