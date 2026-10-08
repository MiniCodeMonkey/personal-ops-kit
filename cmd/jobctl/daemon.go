package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MiniCodeMonkey/personal-ops-kit/internal/job"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/ledger"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/notify"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/runner"
	"github.com/MiniCodeMonkey/personal-ops-kit/internal/schedule"
)

// tickInterval is how often the scheduler wakes. Thirty seconds is comfortably
// inside the one-minute resolution of a cron spec, so a fire cannot be stepped over,
// and is cheap enough that an always-on daemon costs nothing measurable.
const tickInterval = 30 * time.Second

// sleepJump is how much later than expected a tick must arrive to be treated as the
// machine having been asleep or the daemon stopped. Generous, because a busy laptop
// can delay a timer by a second or two without having slept.
const sleepJump = 3 * tickInterval

// Daemon owns scheduling and the HTTP server.
type Daemon struct {
	cfg    Config
	runner *runner.Runner
	notif  *notify.Notifier
	policy notify.ReminderPolicy

	mu      sync.Mutex
	jobs    []*job.Job
	probs   []error
	running map[string]*activeRun
	state   *daemonState
}

type activeRun struct {
	RunID   string
	Started time.Time
	Cancel  context.CancelFunc
}

// daemonState is what must survive a restart: the last fire honoured per job, and the
// paused set. Without it a restart would either replay fires or forget them.
type daemonState struct {
	path      string
	LastFire  map[string]time.Time `json:"last_fire"`
	Paused    map[string]bool      `json:"paused"`
	Heartbeat time.Time            `json:"heartbeat"`
	mu        sync.Mutex
}

func loadState(path string) *daemonState {
	s := &daemonState{path: path, LastFire: map[string]time.Time{}, Paused: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, s); err != nil {
		log.Printf("state file unreadable, starting fresh: %v", err)
		return &daemonState{path: path, LastFire: map[string]time.Time{}, Paused: map[string]bool{}}
	}
	if s.LastFire == nil {
		s.LastFire = map[string]time.Time{}
	}
	if s.Paused == nil {
		s.Paused = map[string]bool{}
	}
	s.path = path
	return s
}

func (s *daemonState) save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Printf("could not create state directory: %v", err)
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("could not write state: %v", err)
		return
	}
	// Rename rather than write in place: a crash mid-write would otherwise leave a
	// truncated state file, and losing every job's last fire time means the next
	// start either replays or forgets a day of runs.
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("could not commit state: %v", err)
	}
}

func (s *daemonState) lastFire(job string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastFire[job]
}

func (s *daemonState) setLastFire(job string, t time.Time) {
	s.mu.Lock()
	s.LastFire[job] = t
	s.mu.Unlock()
	s.save()
}

func (s *daemonState) isPaused(job string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Paused[job]
}

func (s *daemonState) setPaused(job string, paused bool) {
	s.mu.Lock()
	if paused {
		s.Paused[job] = true
	} else {
		delete(s.Paused, job)
	}
	s.mu.Unlock()
	s.save()
}

func (s *daemonState) beat(t time.Time) {
	s.mu.Lock()
	s.Heartbeat = t
	s.mu.Unlock()
	s.save()
}

func (s *daemonState) lastBeat() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Heartbeat
}

// NewDaemon builds a daemon from config.
func NewDaemon(cfg Config) (*Daemon, error) {
	notifier, err := notify.New(notifierExecPath(), cfg.DashboardURL())
	if err != nil {
		// Not fatal. Losing notifications is bad; refusing to run any jobs because
		// notifications are unavailable is worse.
		log.Printf("notifications unavailable: %v", err)
	}

	d := &Daemon{
		cfg:     cfg,
		notif:   notifier,
		policy:  notify.DefaultReminderPolicy(),
		running: map[string]*activeRun{},
		state:   loadState(cfg.StatePath()),
	}
	d.runner = &runner.Runner{
		OpsRoot:       cfg.OpsRoot,
		ArtifactsRoot: cfg.ArtifactsRoot,
		Notifier:      d,
	}
	d.reload()
	return d, nil
}

// Post satisfies runner.Notifier. The runner reports every outcome; deciding which
// ones are worth interrupting someone over belongs here, not in the runner.
func (d *Daemon) Post(ctx context.Context, jobName, outcome, headline, runID string) error {
	if d.notif == nil || !notify.Worth(notify.Outcome(outcome)) {
		return nil
	}
	return d.notif.Post(ctx, notify.Notification{
		Job: jobName, Outcome: notify.Outcome(outcome), Headline: headline, RunID: runID,
	})
}

// clearNotification takes a job's notification out of Notification Centre once its
// result has been read.
//
// Fire-and-forget on purpose: this is called while rendering a page, and a memo that
// fails to load because Notification Centre was slow would be a worse outcome than a
// banner that lingers. The ledger is already the record of what has been seen, so the
// morning nudge stays correct either way.
func (d *Daemon) clearNotification(jobName string) {
	if d.notif == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := d.notif.Clear(ctx, jobName); err != nil {
			log.Printf("%s: clear notification: %v", jobName, err)
		}
	}()
}

func (d *Daemon) reload() {
	jobs, problems := job.LoadAll(d.cfg.JobsRoot(), d.cfg.ArtifactsRoot)
	d.mu.Lock()
	d.jobs, d.probs = jobs, problems
	d.mu.Unlock()
	for _, p := range problems {
		log.Printf("job not loaded: %v", p)
	}
}

// Run starts the scheduler loop and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) {
	log.Printf("scheduler started; %d job(s)", len(d.snapshotJobs()))

	// A start is itself a gap: whatever happened while the daemon was down is exactly
	// what catch-up is for, so evaluate immediately rather than waiting a tick.
	d.tick(ctx, time.Now())

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			log.Print("scheduler stopping")
			return
		case now := <-ticker.C:
			if now.Sub(last) > sleepJump {
				// Sleep does not announce itself; it shows up as a tick that arrives
				// far later than it should. Nothing special is needed beyond noticing,
				// because catch-up already asks "what fired while I was not looking".
				log.Printf("clock jumped %s -- treating as wake from sleep", now.Sub(last).Round(time.Second))
			}
			last = now
			d.tick(ctx, now)
		}
	}
}

func (d *Daemon) tick(ctx context.Context, now time.Time) {
	d.state.beat(now)

	for _, j := range d.snapshotJobs() {
		if d.state.isPaused(j.Name) || d.isRunning(j.Name) {
			continue
		}
		decision, fire := schedule.Evaluate(j.Schedule, d.state.lastFire(j.Name), now, j.CatchupWindow)
		switch decision {
		case schedule.Idle:
			continue
		case schedule.Expired:
			// Deliberately abandoned rather than run late. Recorded so the dashboard
			// can say why a night is missing instead of leaving a silent hole.
			log.Printf("%s: missed fire at %s is outside the catch-up window; skipping", j.Name, fire.Format(time.RFC3339))
			d.state.setLastFire(j.Name, fire)
			_ = ledger.AppendRun(j.LedgerPath(), ledger.Record{
				Job: j.Name, RunID: fire.Format("2006-01-02T15-04-05"),
				StartedAt: fire, FinishedAt: now, Outcome: ledger.Gated,
				Headline: "Missed while the daemon was not running",
				Detail:   fmt.Sprintf("Scheduled for %s, outside the %s catch-up window.", fire.Format("15:04 on 2 Jan"), j.CatchupWindow),
				Trigger:  string(runner.TriggerCatchup),
			})
		case schedule.Due, schedule.Catchup:
			trigger := runner.TriggerSchedule
			if decision == schedule.Catchup {
				trigger = runner.TriggerCatchup
				log.Printf("%s: catching up the fire from %s", j.Name, fire.Format("15:04"))
			}
			// Record the fire before starting, so a crash during the run cannot cause
			// it to be started again on restart.
			d.state.setLastFire(j.Name, fire)
			go d.start(ctx, j, trigger)
		}
	}

	d.maybeRemind(ctx, now)
}

// maybeRemind re-posts each job's latest result if it was delivered but never looked
// at. Only the latest: see ledger.Outstanding for why older ones must not queue up.
func (d *Daemon) maybeRemind(ctx context.Context, now time.Time) {
	if d.notif == nil || !d.policy.DueAt(now) {
		return
	}
	for _, j := range d.snapshotJobs() {
		runs, err := ledger.Load(j.LedgerPath())
		if err != nil {
			continue
		}
		var pending []notify.Pending
		if r, ok := ledger.Outstanding(runs); ok {
			pending = append(pending, notify.Pending{
				Job: j.Name, Outcome: notify.Outcome(r.Outcome), Headline: r.Headline,
				RunID: r.RunID, FinishedAt: r.FinishedAt, RemindedAt: r.RemindedAt,
			})
		}
		for _, p := range d.policy.Select(pending, now) {
			err := d.notif.Post(ctx, notify.Notification{
				Job: p.Job, Outcome: p.Outcome, Headline: p.Headline,
				RunID: p.RunID, Reminder: true,
			})
			if err != nil {
				log.Printf("%s: reminder failed: %v", j.Name, err)
				continue
			}
			_ = ledger.Remind(j.LedgerPath(), p.Job, p.RunID, now)
		}
	}
}

// start launches a run, tracking it so the dashboard can show it and stop it.
func (d *Daemon) start(ctx context.Context, j *job.Job, trigger runner.Trigger) {
	runCtx, cancel := context.WithCancel(ctx)

	d.mu.Lock()
	if _, busy := d.running[j.Name]; busy {
		d.mu.Unlock()
		cancel()
		return
	}
	d.running[j.Name] = &activeRun{Started: time.Now(), Cancel: cancel}
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.running, j.Name)
		d.mu.Unlock()
		cancel()
	}()

	log.Printf("%s: starting (%s)", j.Name, trigger)
	res, err := d.runner.Run(runCtx, j, trigger)
	if err != nil {
		log.Printf("%s: %v", j.Name, err)
		return
	}
	log.Printf("%s: %s -- %s", j.Name, res.Record.Outcome, res.Record.Headline)
}

func (d *Daemon) snapshotJobs() []*job.Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*job.Job(nil), d.jobs...)
}

func (d *Daemon) problems() []error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]error(nil), d.probs...)
}

func (d *Daemon) isRunning(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.running[name]
	return ok
}

func (d *Daemon) activeRun(name string) *activeRun {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[name]
}

func (d *Daemon) findJob(name string) *job.Job {
	for _, j := range d.snapshotJobs() {
		if j.Name == name {
			return j
		}
	}
	return nil
}

// gapNotice describes daemon downtime, so silence is distinguishable from an idle
// system. A crash-loop under KeepAlive is otherwise completely quiet.
func (d *Daemon) gapNotice(now time.Time) string {
	beat := d.state.lastBeat()
	if beat.IsZero() {
		return ""
	}
	gap := now.Sub(beat)
	if gap < 10*time.Minute {
		return ""
	}
	return fmt.Sprintf("The daemon was not running from %s to %s (%s). Any fire inside a job's catch-up window has been re-run.",
		beat.Format("15:04 on 2 Jan"), now.Format("15:04"), gap.Round(time.Minute))
}
