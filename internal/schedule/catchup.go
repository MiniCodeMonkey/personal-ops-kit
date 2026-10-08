package schedule

import "time"

// Decision is what the scheduler concluded about one job at one tick.
type Decision int

const (
	// Idle means no fire is due.
	Idle Decision = iota
	// Due means a fire time has arrived normally.
	Due
	// Catchup means a fire was missed while the daemon was down or the machine was
	// asleep, and it is recent enough to still be worth running.
	Catchup
	// Expired means a fire was missed but is older than the catch-up window, so it
	// is deliberately abandoned rather than run late.
	Expired
)

func (d Decision) String() string {
	switch d {
	case Due:
		return "due"
	case Catchup:
		return "catchup"
	case Expired:
		return "expired"
	default:
		return "idle"
	}
}

// Evaluate decides whether a job should run now.
//
//   - lastFire is the last fire time the daemon acted on, zero if it has never run.
//   - window bounds how late a missed fire may be run. A laptop closed for a week
//     should not wake into a stampede of backlogged jobs, so anything older than the
//     window is reported as Expired and skipped.
//
// The returned time is the fire being honoured, which is the scheduled minute rather
// than "now" -- so a catch-up run records the slot it belongs to.
//
// Sleep does not announce itself. It shows up as a tick that arrives much later than
// the previous one, which is indistinguishable from the daemon having been stopped,
// and both are handled the same way: look for fires between the last one honoured and
// now. That is why this takes lastFire rather than trying to detect sleep directly.
func Evaluate(spec *Spec, lastFire, now time.Time, window time.Duration) (Decision, time.Time) {
	// Never run before: adopt the current minute if it matches, but do not reach back
	// and invent history for a job that has only just been added.
	if lastFire.IsZero() {
		if spec.Matches(now) {
			return Due, now.Truncate(time.Minute)
		}
		return Idle, time.Time{}
	}

	fire, found := spec.LastBetween(lastFire, now)
	if !found {
		return Idle, time.Time{}
	}

	// The fire landed on this very minute: an ordinary on-time run.
	if !fire.Before(now.Truncate(time.Minute)) {
		return Due, fire
	}

	if now.Sub(fire) > window {
		return Expired, fire
	}
	return Catchup, fire
}
