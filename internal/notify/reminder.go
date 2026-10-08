package notify

import "time"

// Pending is one unacknowledged result, as the daemon reads it back from the ledger.
type Pending struct {
	Job      string
	Outcome  Outcome
	Headline string
	RunID    string
	// FinishedAt is when the run completed, not when it was last notified about.
	FinishedAt time.Time
	// RemindedAt is the last time a reminder was posted, zero if never.
	RemindedAt time.Time
}

// ReminderPolicy decides when to re-post results that were delivered but never
// looked at.
type ReminderPolicy struct {
	// Hour and Minute are the local time of the morning nudge.
	Hour, Minute int
	// MinAge stops a result that arrived minutes ago from being re-announced by a
	// nudge that happens to fall just after it. A run at 08:29 should not be
	// "still unread" at 08:30.
	MinAge time.Duration
	// MaxAge abandons reminders for results old enough that nagging is not going to
	// help. They stay in the dashboard; they just stop interrupting.
	MaxAge time.Duration
}

// DefaultReminderPolicy nudges once each morning. Once, not repeatedly: a reminder
// that arrives every two hours becomes something you dismiss without reading, which
// costs exactly the attention the reminder existed to buy.
func DefaultReminderPolicy() ReminderPolicy {
	return ReminderPolicy{
		Hour: 8, Minute: 30,
		MinAge: 2 * time.Hour,
		MaxAge: 7 * 24 * time.Hour,
	}
}

// DueAt reports whether the nudge falls in the minute containing now.
func (p ReminderPolicy) DueAt(now time.Time) bool {
	return now.Hour() == p.Hour && now.Minute() == p.Minute
}

// Select returns the pending results that should be re-announced now.
//
// Only one reminder per result per day: RemindedAt is compared by calendar day, so a
// daemon restart during the nudge minute cannot re-post the same thing twice.
func (p ReminderPolicy) Select(pending []Pending, now time.Time) []Pending {
	var due []Pending
	for _, x := range pending {
		age := now.Sub(x.FinishedAt)
		if age < p.MinAge || age > p.MaxAge {
			continue
		}
		if !x.RemindedAt.IsZero() && sameDay(x.RemindedAt, now) {
			continue
		}
		if !Worth(x.Outcome) {
			continue
		}
		due = append(due, x)
	}
	return due
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
