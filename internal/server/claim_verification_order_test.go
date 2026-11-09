package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// A verified claim must never be preceded by an unverified probe. Upstream
// treats that probe as a second claim attempt, so a later verified retry can
// be rejected as duplicate abnormal traffic (3012).
func TestClaimVerificationRunsBeforeOnlyClaimRequest(t *testing.T) {
	app := registrationTestApp(t)

	var claimCalls atomic.Int32
	var captchaCalls atomic.Int32
	transport := registrationRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/billing/claim"):
			claimCalls.Add(1)
			// The repaired path must reach upstream with a verified ticket.
			if r.Header.Get("X-Aliyun-Captcha-Verify-Param") == "" {
				t.Fatal("claim reached upstream without a verification ticket")
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"code":0,"data":{"plan":{"plan_id":"p","ends_at":1}}}`,
				)),
				Request: r,
			}, nil
		case strings.Contains(r.URL.Path, "/client/configs"):
			captchaCalls.Add(1)
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"code":0,"data":{"configs":{"captcha":{` +
						`"enabled":true,"region":"sgp","sceneId":"scene",` +
						`"prefix":"verify","skip_model_request":false}}}}`,
				)),
				Request: r,
			}, nil
		default:
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)),
				Request:    r,
			}, nil
		}
	})
	app.mu.Lock()
	app.clients[""] = &http.Client{Transport: transport}
	app.mu.Unlock()

	// The captcha runtime is deliberately unavailable. Verification fails
	// before any claim request, proving that no unverified probe was sent.
	account, err := app.insertAccount(M{
		"family": "zai", "customer_id": "claim-order",
		"device_mid": "claim-order-device", "status": "active",
	}, M{"zcode_jwt": registrationJWT(`{"user_id":"claim-order"}`)})
	registrationMust(t, err)

	result, err := app.claim(
		context.Background(),
		str(account["id"]),
		"plan-order",
		"auto",
		nil,
	)
	registrationMust(t, err)
	registrationEqual(t, result["ok"], false)
	registrationEqual(t, claimCalls.Load(), int32(0))
	_ = captchaCalls.Load()
}
