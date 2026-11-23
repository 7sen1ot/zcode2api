package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type claimProvider struct {
	requiresVerification bool
	uncertainResponse    bool
	expires              int64
	posts                atomic.Int32
	firstPostAt          atomic.Int64
	owned                atomic.Bool
}

type claimClient struct {
	service
	token, accountID string
}

func (p *claimProvider) start(t *testing.T, root string) claimClient {
	t.Helper()
	p.expires = time.Now().Add(2 * time.Hour).Unix()
	plan := fmt.Sprintf(`{"plan_id":"daily-card","name":"ZCODE GLOBAL BUILD","ends_at":%d}`, p.expires)
	owned := fmt.Sprintf(`{"plan_id":"daily-card","user_plan_id":"issued-card","status":"active","ends_at":%d}`, p.expires)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/marketing/touch", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"deliveries":[{"resource_position":"banner","banner":{
			"background":{"args":{"zcode_plan":%s}},
			"buttons":[{"action":{"type":"claim_zcode_plan","args":{"plan_id":"daily-card"}}}]}}]}}`, plan)
	})
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/preview", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"plans":[%s]}}`, plan)
	})
	mux.HandleFunc("GET /api/v1/client/configs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"configs":{"captcha":{"enabled":%t,"region":"cn","sceneId":"fixture","prefix":"fixture"}}}}`, p.requiresVerification)
	})
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/balance", func(w http.ResponseWriter, _ *http.Request) {
		switch {
		case p.owned.Load():
			fmt.Fprintf(w, `{"code":0,"data":{"plans":[%s],"balances":[{"user_plan_id":"issued-card","total_units":50,"remaining_units":50}]}}`, owned)
		case p.uncertainResponse && p.posts.Load() > 0:
			// An incomplete ownership snapshot cannot establish whether the POST took effect.
			io.WriteString(w, `{"code":0,"data":{"balances":[]}}`)
		default:
			io.WriteString(w, `{"code":0,"data":{"plans":[],"balances":[]}}`)
		}
	})
	mux.HandleFunc("POST /api/v1/zcode-plan/billing/claim", func(w http.ResponseWriter, r *http.Request) {
		p.firstPostAt.CompareAndSwap(0, time.Now().UnixMilli())
		p.posts.Add(1)
		var body struct {
			PlanID string `json:"plan_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("invalid upstream claim body: %v", err)
			w.WriteHeader(400)
			return
		}
		switch {
		case body.PlanID != "daily-card":
			w.WriteHeader(400)
			io.WriteString(w, `{"code":"plan_closed","data":{"message":"Campaign ended"}}`)
		case p.uncertainResponse:
			w.WriteHeader(503)
			io.WriteString(w, `{"code":503}`)
		case p.owned.Swap(true):
			w.WriteHeader(409)
			io.WriteString(w, `{"code":1003}`)
		default:
			fmt.Fprintf(w, `{"code":0,"data":{"plan":%s}}`, owned)
		}
	})
	token := fixtureAccountToken()
	serveLocalUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/client/configs" && r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("claim fixture received a request without its account credential")
			w.WriteHeader(401)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	s := startService(t, root)
	admin := s.login(t)
	return claimClient{s, admin, s.importActiveAccount(t, admin, token)}
}

type claimResult struct {
	OK     bool
	Code   string
	EndsAt int64
	Plan   struct {
		ID string `json:"user_plan_id"`
	}
	AlreadyClaimed       bool `json:"already_claimed"`
	RequiresVerification bool `json:"requires_verification"`
	RequiresConfirmation bool `json:"requires_confirmation"`
	Reconciled           bool
}

func (c claimClient) claim(t *testing.T, plan string) claimResult {
	t.Helper()
	body := fmt.Sprintf(`{"account_id":%q,"plan_id":%q}`, c.accountID, plan)
	return decode[claimResult](t, request(t, "POST", c.admin+"/api/claims/claim", c.token, body, 200))
}

type claimRecord struct {
	OK         int
	Code       string
	Trigger    string
	AccountID  string `json:"account_id"`
	PlanID     string `json:"plan_id"`
	UserPlanID string `json:"user_plan_id"`
	EndsAt     int64  `json:"ends_at"`
}

func (c claimClient) history(t *testing.T) []claimRecord {
	t.Helper()
	return decode[[]claimRecord](t, request(t, "GET", c.admin+"/api/claims/history", c.token, "", 200))
}

type offeredPlan struct {
	ID string `json:"plan_id"`
}

func (c claimClient) dailyPlans(t *testing.T) []offeredPlan {
	t.Helper()
	raw := request(t, "GET", c.admin+"/api/claims/discover?scope=daily&account_id="+c.accountID, c.token, "", 200)
	discovery := decode[struct {
		Complete bool
		Previews []struct {
			AccountID string `json:"account_id"`
			Plans     []offeredPlan
		}
	}](t, raw)
	if !discovery.Complete || len(discovery.Previews) != 1 || discovery.Previews[0].AccountID != c.accountID {
		t.Fatalf("incomplete daily discovery: %s", raw)
	}
	return discovery.Previews[0].Plans
}

func TestDailyCardClaim(t *testing.T) {
	p := &claimProvider{}
	c := p.start(t, t.TempDir())
	if plans := c.dailyPlans(t); len(plans) != 1 || plans[0].ID != "daily-card" {
		t.Fatalf("unexpected daily cards: %+v", plans)
	}
	raw := request(t, "POST", c.admin+"/api/claims/auto-run", c.token, `{"dry_run":true}`, 200)
	preview := decode[struct {
		Checked, Claimed int
		Results          []struct {
			Account    string
			PlanID     string `json:"plan_id"`
			WouldClaim bool   `json:"would_claim"`
		}
	}](t, raw)
	if preview.Checked != 1 || preview.Claimed != 0 || len(preview.Results) != 1 || !preview.Results[0].WouldClaim ||
		preview.Results[0].Account != c.accountID || preview.Results[0].PlanID != "daily-card" {
		t.Fatalf("unexpected dry run for the same card advertised by two sources: %s", raw)
	}
	if p.posts.Load() != 0 || len(c.history(t)) != 0 {
		t.Fatal("discovery or dry run submitted a claim or wrote claim history")
	}
	result := c.claim(t, "daily-card")
	if !result.OK || result.AlreadyClaimed || result.Plan.ID != "issued-card" || result.EndsAt != p.expires*1000 || p.posts.Load() != 1 {
		t.Fatalf("unexpected claim result: %+v (POSTs %d)", result, p.posts.Load())
	}
	history := c.history(t)
	want := claimRecord{OK: 1, Code: "0", Trigger: "manual", AccountID: c.accountID, PlanID: "daily-card", UserPlanID: "issued-card", EndsAt: p.expires * 1000}
	if len(history) != 1 || history[0] != want {
		t.Fatalf("claim history = %+v, want %+v", history, want)
	}
	if plans := c.dailyPlans(t); len(plans) != 0 {
		t.Fatalf("owned card still offered by discovery: %+v", plans)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw := request(t, "GET", c.admin+"/api/dashboard?view=overview", c.token, "", 200)
		dashboard := decode[struct{ Summary struct{ TotalRemaining int } }](t, raw)
		if dashboard.Summary.TotalRemaining == 50 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("balance after claiming = %d, want 50", dashboard.Summary.TotalRemaining)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if again := c.claim(t, "daily-card"); !again.OK || !again.AlreadyClaimed || again.Code != "1003" {
		t.Fatalf("already-owned card reported incorrectly: %+v", again)
	}
}

func TestClaimRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, plan, code  string
		needsVerification bool
		posts             int32
	}{
		{"closed campaign", "closed-card", "plan_closed", false, 1},
		{"manual verification required", "daily-card", "coding_plan_security_verification_required", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &claimProvider{requiresVerification: tc.needsVerification}
			c := p.start(t, t.TempDir())
			got := c.claim(t, tc.plan)
			if got.OK || got.Code != tc.code || got.RequiresVerification != tc.needsVerification || p.posts.Load() != tc.posts {
				t.Fatalf("unexpected refusal: %+v (POSTs %d)", got, p.posts.Load())
			}
			history := c.history(t)
			if len(history) != 1 || history[0].OK != 0 || history[0].PlanID != tc.plan || history[0].Code != tc.code || history[0].UserPlanID != "" {
				t.Fatalf("unexpected refusal history: %+v", history)
			}
		})
	}
}

func TestBatchClaim(t *testing.T) {
	p := &claimProvider{}
	c := p.start(t, t.TempDir())
	raw := request(t, "POST", c.admin+"/api/claims/auto-run", c.token, `{"dry_run":false}`, 200)
	run := decode[struct {
		Checked, Claimed int
		Results          []struct {
			OK      bool
			Account string
			PlanID  string `json:"plan_id"`
		}
	}](t, raw)
	if run.Checked != 1 || run.Claimed != 1 || len(run.Results) != 1 || !run.Results[0].OK ||
		run.Results[0].Account != c.accountID || run.Results[0].PlanID != "daily-card" || p.posts.Load() != 1 {
		t.Fatalf("unexpected batch result: %s (POSTs %d)", raw, p.posts.Load())
	}
	history := c.history(t)
	if len(history) != 1 || history[0].OK != 1 || history[0].Trigger != "auto" || history[0].UserPlanID != "issued-card" {
		t.Fatalf("unexpected batch claim history: %+v", history)
	}
}

func TestUnconfirmedClaimAfterRestart(t *testing.T) {
	root := t.TempDir()
	p := &claimProvider{uncertainResponse: true}
	c := p.start(t, root)
	first := c.claim(t, "daily-card")
	if first.OK || first.Code != "claim_result_unknown" || !first.RequiresConfirmation || p.posts.Load() != 1 {
		t.Fatalf("unexpected unconfirmed claim: %+v (POSTs %d)", first, p.posts.Load())
	}
	c.stop()
	p.owned.Store(true) // The next balance snapshot now includes the issued card.
	c.service = startService(t, root)
	c.token = c.login(t)
	next := c.claim(t, "daily-card")
	if !next.OK || !next.Reconciled || next.Code != "claim_result_confirmed" || next.Plan.ID != "issued-card" || p.posts.Load() != 1 {
		t.Fatalf("retry did not confirm the original submission: %+v (total POSTs %d)", next, p.posts.Load())
	}
	history := c.history(t)
	successes, pending := 0, 0
	for _, row := range history {
		if row.OK == 1 && row.Code == "claim_result_confirmed" && row.UserPlanID == "issued-card" {
			successes++
		} else if row.OK == 0 && row.Code == "claim_result_unknown" {
			pending++
		}
	}
	if len(history) != 2 || successes != 1 || pending != 1 {
		t.Fatalf("unexpected history after restart: %+v", history)
	}
}

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type claimSchedule struct {
	Enabled   bool
	NextRunAt int64 `json:"next_run_at"`
	Last      struct {
		Status           string
		Checked, Claimed int
		ScheduledAt      int64 `json:"scheduled_at"`
		StartedAt        int64 `json:"started_at"`
		FinishedAt       int64 `json:"finished_at"`
	} `json:"last_run"`
}

func (s service) waitForClaimSchedule(t *testing.T, admin string, deadline time.Time) claimSchedule {
	t.Helper()
	for {
		raw := request(t, "GET", s.admin+"/api/claims/schedule", admin, "", 200)
		got := decode[claimSchedule](t, raw)
		switch got.Last.Status {
		case "completed":
			return got
		case "failed", "partial", "needs_verification":
			t.Fatalf("daily worker finished with an unexpected outcome: %s", raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the daily worker: %s", raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDailyClaimScheduleOnEmptyInstallation(t *testing.T) {
	root := t.TempDir()
	s := startService(t, root)
	admin := s.login(t)
	// A just-due schedule runs inside its recovery window, including across midnight.
	due := time.Now().In(shanghai).Truncate(time.Minute).Add(-time.Minute)
	request(t, "PUT", s.admin+"/api/settings", admin,
		fmt.Sprintf(`{"daily_claim_enabled":true,"daily_claim_time":%q}`, due.Format("15:04")), 200)
	got := s.waitForClaimSchedule(t, admin, time.Now().Add(10*time.Second))
	if !got.Enabled || got.Last.ScheduledAt != due.UnixMilli() || got.Last.FinishedAt < due.UnixMilli() ||
		got.Last.Checked != 0 || got.Last.Claimed != 0 || got.NextRunAt != due.AddDate(0, 0, 1).UnixMilli() {
		t.Fatalf("unexpected empty-pool schedule: %+v", got)
	}
	s.stop()
	s = startService(t, root)
	admin = s.login(t)
	restored := decode[claimSchedule](t, request(t, "GET", s.admin+"/api/claims/schedule", admin, "", 200))
	if restored != got {
		t.Fatalf("completed occurrence changed after restart: before %+v, after %+v", got, restored)
	}
}
