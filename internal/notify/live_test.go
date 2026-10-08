package notify

import (
	"context"
	"os"
	"testing"
	"time"
)

// Delivery can only really be confirmed by a human looking at the screen, so this
// test posts real notifications and is skipped unless explicitly asked for:
//
//	PERSONAL_OPS_LIVE_NOTIFY=1 go test ./internal/notify -run Live -v
//
// Guarded rather than deleted, because "does a notification actually arrive" is the
// question that matters most here and the one least amenable to a unit test.
func TestLiveDelivery(t *testing.T) {
	if os.Getenv("PERSONAL_OPS_LIVE_NOTIFY") == "" {
		t.Skip("set PERSONAL_OPS_LIVE_NOTIFY=1 to post real notifications")
	}

	n, err := New("dev.personal-ops.notifier", "http://127.0.0.1:7777")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	t.Log("posting a fresh success notification")
	if err := n.Post(ctx, Notification{
		Job: "garden-log", Outcome: OutcomeOK,
		Headline: "Best find: ~25 projector screens, DKK 0-325 each",
		RunID:    "2026-08-11T10:28",
	}); err != nil {
		t.Fatalf("post success: %v", err)
	}

	time.Sleep(4 * time.Second)

	t.Log("posting a morning reminder for an unread overnight memo")
	if err := n.Post(ctx, Notification{
		Job: "nightly-research", Outcome: OutcomeOK,
		Headline: "CI critical path: the 7-minute ungate is one coupled change",
		RunID:    "2026-08-11T18:47", Reminder: true,
	}); err != nil {
		t.Fatalf("post reminder: %v", err)
	}

	time.Sleep(4 * time.Second)

	// Posting to the same group must replace, not stack: a daily job would otherwise
	// leave a week of banners behind.
	t.Log("re-posting to the garden-log group; it should replace, not stack")
	if err := n.Post(ctx, Notification{
		Job: "garden-log", Outcome: OutcomeFailed,
		Headline: "Found no auctions -- the site layout may have changed",
		RunID:    "2026-08-11T10:40",
	}); err != nil {
		t.Fatalf("post replacement: %v", err)
	}
}
