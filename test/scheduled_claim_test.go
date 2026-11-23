//go:build browser

package test

import (
	"fmt"
	"testing"
	"time"
)

// The worker starts a browser even when the fixture offers a card without verification.
func TestScheduledDailyCardClaim(t *testing.T) {
	root := t.TempDir()
	p := &claimProvider{}
	c := p.start(t, root)
	// Leave time to save the schedule even if setup finishes just before a minute boundary.
	due := time.Now().In(shanghai).Add(10 * time.Second).Truncate(time.Minute).Add(time.Minute)
	settings := fmt.Sprintf(`{"daily_claim_enabled":true,"daily_claim_time":%q,
		"http":{"captcha":{"browser_backend":"playwright","browser_headless":true}}}`, due.Format("15:04"))
	request(t, "PUT", c.admin+"/api/settings", c.token, settings, 200)
	time.Sleep(time.Until(due))
	got := c.waitForClaimSchedule(t, c.token, due.Add(90*time.Second))
	if !got.Enabled || got.Last.ScheduledAt != due.UnixMilli() || got.Last.StartedAt < due.UnixMilli() ||
		got.Last.FinishedAt < got.Last.StartedAt || got.Last.Checked != 1 || got.Last.Claimed != 1 ||
		got.NextRunAt != due.AddDate(0, 0, 1).UnixMilli() {
		t.Fatalf("unexpected daily occurrence for %v: %+v", due, got)
	}
	want := claimRecord{OK: 1, Code: "0", Trigger: "scheduled", AccountID: c.accountID,
		PlanID: "daily-card", UserPlanID: "issued-card", EndsAt: p.expires * 1000}
	history := c.history(t)
	if len(history) != 1 || history[0] != want || p.posts.Load() != 1 {
		t.Fatalf("scheduled history = %+v, POSTs = %d; want %+v and one POST", history, p.posts.Load(), want)
	}
	if submitted := time.UnixMilli(p.firstPostAt.Load()); submitted.Before(due) {
		t.Fatalf("upstream received the claim at %v, before the scheduled time %v", submitted, due)
	}

	c.stop()
	c.service = startService(t, root)
	c.token = c.login(t)
	restored := decode[claimSchedule](t, request(t, "GET", c.admin+"/api/claims/schedule", c.token, "", 200))
	if restored != got || p.posts.Load() != 1 {
		t.Fatalf("completed schedule after restart = %+v, POSTs = %d; want %+v and one POST", restored, p.posts.Load(), got)
	}
}
