package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManualClaimVerificationConfigIgnoresModelSkip(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		equal(t, r.URL.Path, "/api/v1/client/configs")
		equal(t, r.URL.Query().Get("app_version"), a.config()["app_version"])
		equal(t, r.Header.Get("Authorization"), "")
		writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": M{
			"enabled": true, "skip_model_request": true, "region": "cn", "prefix": "offline", "sceneId": "claim-scene",
		}}}})
	})
	equal(t, obj(obj(a.config()["http"])["captcha"])["mode"], "off")
	config, err := a.claimVerification(context.Background(), str(accounts[0]["id"]))
	must(t, err)
	equal(t, config, M{"enabled": true, "scene_id": "claim-scene", "region": "cn", "prefix": "offline"})
	if _, err := a.claimVerification(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown account must not expose a verification flow")
	}
	equal(t, request(a.AdminHandler(), "GET", "/api/claims/verification?account_id="+str(accounts[0]["id"]), "", nil).Code, 401)
}

func TestClaimForwardsFreshBrowserTicketAndReportsVerificationRequired(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 2)
	var claims atomic.Int32
	const ticket = `{"certifyId":"synthetic-browser-ticket"}`
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/zcode-plan/billing/claim":
			claims.Add(1)
			if r.Header.Get("X-Aliyun-Captcha-Verify-Param") == "" {
				writeJSON(w, 200, M{"code": 3007, "msg": "captcha verify failed"})
				return
			}
			equal(t, r.Header.Get("X-Aliyun-Captcha-Verify-Param"), ticket)
			equal(t, r.Header.Get("X-Aliyun-Captcha-Verify-Region"), "cn")
			equal(t, r.Header.Get("X-Device-Mid"), accounts[0]["device_mid"])
			equal(t, r.Header.Get("Authorization"), "Bearer synthetic-0")
			body, err := readBody(w, r)
			must(t, err)
			equal(t, body, M{"plan_id": "activity-card"})
			writeJSON(w, 200, M{"code": 0, "data": M{"plan": M{"plan_id": "activity-card", "user_plan_id": "claimed"}}})
		case "/api/v1/zcode-plan/billing/balance":
			writeJSON(w, 200, M{"code": 0, "data": M{"plans": []any{}, "balances": []any{}}})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})
	ctx := context.Background()
	id := str(accounts[0]["id"])
	failed, err := a.claim(ctx, id, "activity-card", "manual", nil)
	must(t, err)
	equal(t, failed["ok"], false)
	equal(t, failed["requires_verification"], true)
	if !strings.Contains(str(failed["message"]), "安全验证") {
		t.Fatalf("failure must explain the required action: %v", failed)
	}
	login := request(a.AdminHandler(), "POST", "/api/auth/login", "", M{"password": a.config()["admin_password"]})
	adminToken := str(obj(parse(login.Body.String(), nil))["token"])
	body := M{"account_id": id, "plan_id": "activity-card", "captcha_verify_param": ticket, "captcha_region": "cn"}
	response := request(a.AdminHandler(), "POST", "/api/claims/claim", adminToken, body)
	equal(t, response.Code, 200)
	equal(t, obj(parse(response.Body.String(), nil))["ok"], true)
	if _, err := a.claim(ctx, str(accounts[1]["id"]), "activity-card", "manual", body); err == nil {
		t.Fatal("the same ticket must not be reused by a different account")
	}
	equal(t, claims.Load(), 2)
	for _, entry := range a.rows("SELECT message FROM claim_log UNION ALL SELECT detail AS message FROM audit_log") {
		if strings.Contains(str(obj(entry)["message"]), "synthetic-browser-ticket") {
			t.Fatal("verification ticket leaked into logs")
		}
	}
}

func TestClaimRejectsMalformedVerificationBeforeUpstream(t *testing.T) {
	for _, value := range []M{
		{"captcha_region": "cn"}, {"captcha_verify_param": ""},
		{"captcha_verify_param": 123, "captcha_region": "cn"},
		{"captcha_verify_param": "ticket\r\nInjected: header", "captcha_region": "cn"},
		{"captcha_verify_param": strings.Repeat("x", (32<<10)+1), "captcha_region": "cn"},
		{"captcha_verify_param": "ticket", "captcha_region": "unknown"},
	} {
		if err := validateClaimVerification(value); err == nil {
			t.Fatal("invalid verification was accepted")
		}
	}
	for _, value := range []M{nil, {}, {"captcha_verify_param": "ticket", "captcha_region": "sgp"}} {
		must(t, validateClaimVerification(value))
	}
}

func TestAutoClaimObtainsFreshTicketsDespiteModelSkip(t *testing.T) {
	for _, mode := range []string{"off", "auto", "always"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			_, _, accounts := testPool(t, a, 2)
			obj(obj(a.cfg["http"])["captcha"])["mode"] = mode
			var claims, tickets, configs atomic.Int32
			used := map[string]bool{}
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/marketing/touch":
					writeJSON(w, 200, M{"code": 0, "data": M{"deliveries": []any{}}})
				case "/api/v1/zcode-plan/billing/preview":
					writeJSON(w, 200, M{"code": 0, "data": M{"plans": []any{M{"plan_id": "card-a"}, M{"plan_id": "card-b"}}}})
				case "/api/v1/client/configs":
					configs.Add(1)
					scene := validCaptchaScene()
					scene["skip_model_request"] = true
					writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": scene}}})
				case "/api/v1/zcode-plan/billing/claim":
					claims.Add(1)
					param := r.Header.Get("X-Aliyun-Captcha-Verify-Param")
					if param == "" {
						writeJSON(w, 200, M{"code": 3007, "msg": "captcha verify failed"})
						return
					}
					if used[param] {
						t.Error("ticket reused between claims")
					}
					used[param] = true
					equal(t, r.Header.Get("X-Aliyun-Captcha-Verify-Region"), "cn")
					var account M
					for _, candidate := range accounts {
						if r.Header.Get("X-Device-Mid") == candidate["device_mid"] {
							account = candidate
						}
					}
					if account == nil {
						t.Error("claim lost account device identity")
					}
					equal(t, r.Header.Get("Authorization"), "Bearer "+str(a.tokens(str(account["id"]))["zcode_jwt"]))
					body, err := readBody(w, r)
					must(t, err)
					equal(t, len(body), 1)
					writeJSON(w, 200, M{"code": 0, "data": M{"plan": M{"plan_id": body["plan_id"], "user_plan_id": "claimed"}}})
				case "/api/v1/zcode-plan/billing/balance":
					writeJSON(w, 200, M{"code": 0, "data": M{"plans": []any{}, "balances": []any{}}})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			})
			a.captcha.run = func(context.Context, M) (M, error) {
				return M{"ok": true, "param": fmt.Sprint("fresh-auto-ticket-", tickets.Add(1))}, nil
			}
			dry, err := a.autoClaim(t.Context(), true)
			must(t, err)
			equal(t, len(arr(dry["results"])), 4)
			equal(t, claims.Load()+tickets.Load()+configs.Load(), 0)
			result, err := a.autoClaim(t.Context(), false)
			must(t, err)
			equal(t, result["checked"], 2)
			equal(t, result["claimed"], 4)
			equal(t, claims.Load(), 8)
			equal(t, tickets.Load(), 4)
			equal(t, configs.Load(), 4)
			equal(t, a.row("SELECT COUNT(*) AS n FROM claim_log WHERE trigger='auto' AND ok=1")["n"], 4)
			for _, entry := range a.rows("SELECT message FROM claim_log UNION ALL SELECT detail AS message FROM audit_log") {
				if strings.Contains(str(obj(entry)["message"]), "fresh-auto-ticket-") {
					t.Fatal("ticket leaked into logs")
				}
			}
		})
	}
}

func TestAutoClaimVerificationFailuresAreBoundedAndLogged(t *testing.T) {
	for _, tc := range []struct {
		name, runtimeCode, expected string
		claimCode                   any
		disabled                    bool
		claims, tickets             int32
	}{
		{"human", "captcha_interaction_required", "captcha_interaction_required", 3007, false, 1, 1},
		{"missing runtime", "captcha_jsdom_missing", "captcha_jsdom_missing", 3007, false, 1, 1},
		{"SDK rejection", "captcha_verification_failed", "captcha_verification_failed", 3007, false, 1, 1},
		{"disabled scene", "", "captcha_scene_disabled", 3007, true, 1, 0},
		{"rejected fresh ticket", "", "3007", 3007, false, 2, 1},
		{"expired ticket", "", "F019", "F019", false, 2, 1},
		{"ordinary rejection", "", "3001", 3001, false, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			_, _, accounts := testPool(t, a, 1)
			var claims, tickets atomic.Int32
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/client/configs":
					scene := validCaptchaScene()
					scene["enabled"] = !tc.disabled
					writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": scene}}})
				case "/api/v1/zcode-plan/billing/claim":
					claims.Add(1)
					writeJSON(w, 200, M{"code": tc.claimCode, "msg": "rejected"})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			})
			a.captcha.run = func(context.Context, M) (M, error) {
				tickets.Add(1)
				if tc.runtimeCode != "" {
					return M{"code": tc.runtimeCode, "verify_code": "F015"}, nil
				}
				return M{"ok": true, "param": "fresh-test-ticket"}, nil
			}
			result, err := a.claim(t.Context(), str(accounts[0]["id"]), "card", "auto", nil)
			must(t, err)
			equal(t, result["ok"], false)
			equal(t, result["code"], tc.expected)
			equal(t, result["requires_verification"], tc.claimCode != 3001)
			equal(t, claims.Load(), tc.claims)
			equal(t, tickets.Load(), tc.tickets)
			equal(t, a.rows("SELECT trigger,ok,code,message FROM claim_log"), []any{M{"trigger": "auto", "ok": 0, "code": tc.expected, "message": result["message"]}})
			if tc.runtimeCode == "captcha_interaction_required" && !strings.Contains(str(result["message"]), "人工") {
				t.Fatal("manual verification requirement was hidden")
			}
		})
	}
}

func TestAutoClaimVerificationKeepsOriginalProxy(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	var claims atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/client/configs":
			writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": validCaptchaScene()}}})
		case "/api/v1/zcode-plan/billing/claim":
			if claims.Add(1) == 1 {
				writeJSON(w, 200, M{"code": 3007})
				return
			}
			equal(t, r.Header.Get("X-Aliyun-Captcha-Verify-Param"), "pinned-ticket")
			writeJSON(w, 200, M{"code": 0, "data": M{"plan": M{"plan_id": "card"}}})
		case "/api/v1/zcode-plan/billing/balance":
			writeJSON(w, 200, M{"code": 0, "data": M{"plans": []any{}, "balances": []any{}}})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer proxy.Close()
	a.origin = "http://claim-upstream.invalid"
	setTestProxy(a, M{"enabled": true, "urls": []any{proxy.URL}})
	a.captcha.run = func(ctx context.Context, parameters M) (M, error) {
		equal(t, selectedProxy(ctx), proxy.URL)
		equal(t, parameters["proxy_url"], proxy.URL)
		setTestProxy(a, M{"enabled": false})
		return M{"ok": true, "param": "pinned-ticket"}, nil
	}
	result, err := a.claim(t.Context(), str(accounts[0]["id"]), "card", "auto", nil)
	must(t, err)
	equal(t, result["ok"], true)
	equal(t, claims.Load(), 2)
}
