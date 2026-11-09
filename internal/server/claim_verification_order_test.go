package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Failed pre-verification must not submit an unverified claim.
func TestClaimVerificationRunsBeforeOnlyClaimRequest(t *testing.T) {
	app := testApp(t)
	app.captcha.run = func(context.Context, M) (M, error) {
		return M{"ok": false, "code": "captcha_jsdom_missing"}, nil
	}

	var claimCalls atomic.Int32
	var captchaCalls atomic.Int32
	testInstallClient(app, "", func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/billing/claim"):
			claimCalls.Add(1)
			// The repaired path must reach upstream with a verified ticket.
			if r.Header.Get("X-Aliyun-Captcha-Verify-Param") == "" {
				t.Fatal("claim reached upstream without a verification ticket")
			}
			return testAPIResponse(r, 200, `{"code":0,"data":{"plan":{"plan_id":"p","ends_at":1}}}`), nil
		case strings.Contains(r.URL.Path, "/client/configs"):
			captchaCalls.Add(1)
			return testAPIResponse(r, 200, `{"code":0,"data":{"configs":{"captcha":{"enabled":true,"region":"sgp","sceneId":"scene","prefix":"verify","skip_model_request":false}}}}`), nil
		default:
			return testAPIResponse(r, 200, `{"code":0,"data":{}}`), nil
		}
	})

	// The captcha runtime is deliberately unavailable. Verification fails
	// before any claim request, proving that no unverified probe was sent.
	account := testAccount(t, app, "claim-order")
	result, err := app.claim(app.ctx, str(account["id"]), "plan-order", "auto", nil)
	testMust(t, err)
	testEqual(t, result["ok"], false)
	testEqual(t, claimCalls.Load(), int32(0))
	testEqual(t, captchaCalls.Load(), int32(1))
}
