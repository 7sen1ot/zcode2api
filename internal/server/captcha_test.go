package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func installCaptchaScene(t *testing.T, a *App, scene M) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/client/configs" || r.Header.Get("Authorization") != "" {
			t.Error("scene lookup used the wrong endpoint or sent an account credential")
		}
		writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": scene}}})
	})
	return &calls
}

func validCaptchaScene() M {
	return M{"enabled": true, "skip_model_request": false, "sceneId": "offline-scene", "region": "cn", "prefix": "offline-prefix"}
}

func TestCaptchaPoliciesSceneCacheAndFreshTickets(t *testing.T) {
	for _, item := range []struct {
		name, mode            string
		enabled, skip, verify bool
	}{
		{"off", "off", true, false, false},
		{"auto-disabled", "auto", false, false, false},
		{"auto-skip", "auto", true, true, false},
		{"auto-required", "auto", true, false, true},
		{"always-disabled", "always", false, false, false},
		{"always-ignores-skip", "always", true, true, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			a := networkTestApp(t)
			obj(obj(a.cfg["http"])["captcha"])["mode"] = item.mode
			scene := validCaptchaScene()
			scene["enabled"], scene["skip_model_request"] = item.enabled, item.skip
			calls := installCaptchaScene(t, a, scene)
			var tickets int
			a.captcha.run = func(ctx context.Context, parameters M) (M, error) {
				tickets++
				if parameters["scene_id"] != "offline-scene" || parameters["proxy_url"] != selectedProxy(ctx) {
					t.Error("runtime did not receive the selected scene and pinned route")
				}
				return M{"ok": true, "param": fmt.Sprintf("ticket-%d", tickets)}, nil
			}
			for i := 0; i < 2; i++ {
				headers, failure := a.captchaHeaders(context.Background(), nil, false)
				if failure != nil {
					t.Fatal(failure)
				}
				if item.verify {
					equal(t, headers.Get("X-Aliyun-Captcha-Verify-Param"), fmt.Sprintf("ticket-%d", i+1))
					equal(t, headers.Get("X-Aliyun-Captcha-Verify-Region"), "cn")
				} else {
					equal(t, len(headers), 0)
				}
			}
			if item.mode == "off" {
				equal(t, calls.Load(), 0)
			} else {
				equal(t, calls.Load(), 1)
			}
			if item.verify {
				equal(t, tickets, 2)
			} else {
				equal(t, tickets, 0)
			}
			if item.mode != "off" {
				a.invalidateCaptchaScene()
				_, failure := a.captchaHeaders(context.Background(), nil, false)
				if failure != nil {
					t.Fatal(failure)
				}
				equal(t, calls.Load(), 2)
			}
		})
	}
}

func TestCaptchaRuntimeFailuresAndTicketValidation(t *testing.T) {
	for _, item := range []struct {
		name   string
		result M
		err    error
		code   string
	}{
		{"missing-jsdom", M{"code": "captcha_jsdom_missing"}, nil, "captcha_jsdom_missing"},
		{"timeout", M{"code": "captcha_timeout"}, nil, "captcha_timeout"},
		{"authenticated-proxy", M{"code": "captcha_proxy_unsupported"}, nil, "captcha_proxy_unsupported"},
		{"sdk-error", M{"code": "captcha_sdk_error"}, nil, "captcha_runtime_error"},
		{"canceled", M{"code": "captcha_canceled"}, nil, "captcha_runtime_error"},
		{"interaction", M{"code": "captcha_interaction_required", "verify_code": "F015"}, nil, "captcha_interaction_required"},
		{"network", M{"code": "captcha_network_error"}, nil, "captcha_network_error"},
		{"unsafe-error", M{"verify_code": "<script>secret</script>"}, nil, "captcha_verification_failed"},
		{"process-error", nil, errors.New("private process output"), "captcha_runtime_error"},
		{"empty-ticket", M{"ok": true, "param": " "}, nil, "captcha_invalid_result"},
		{"header-injection", M{"ok": true, "param": "ticket\r\nInjected: yes"}, nil, "captcha_invalid_result"},
		{"oversized-ticket", M{"ok": true, "param": strings.Repeat("a", (32<<10)+1)}, nil, "captcha_invalid_result"},
	} {
		t.Run(item.name, func(t *testing.T) {
			a := networkTestApp(t)
			obj(obj(a.cfg["http"])["captcha"])["mode"] = "always"
			installCaptchaScene(t, a, validCaptchaScene())
			a.captcha.run = func(context.Context, M) (M, error) { return item.result, item.err }
			_, failure := a.captchaHeaders(context.Background(), nil, false)
			if failure == nil || failure.Code != item.code {
				t.Fatalf("unexpected failure: %+v", failure)
			}
			if strings.Contains(failure.Message, "private process output") || strings.Contains(failure.Message, "<script>") {
				t.Fatal("runtime internals leaked into public error")
			}
		})
	}
	a := networkTestApp(t)
	obj(obj(a.cfg["http"])["captcha"])["mode"] = "always"
	installCaptchaScene(t, a, validCaptchaScene())
	a.captcha.run = func(context.Context, M) (M, error) { return M{"ok": true, "param": "same-ticket"}, nil }
	_, failure := a.captchaHeaders(context.Background(), nil, false)
	if failure != nil {
		t.Fatal(failure)
	}
	_, failure = a.captchaHeaders(context.Background(), nil, false)
	if failure == nil || failure.Code != "captcha_duplicate_result" {
		t.Fatal("single-use ticket was reused")
	}
}

func TestCaptchaSceneValidationAndCancellation(t *testing.T) {
	for _, scene := range []M{
		{}, {"enabled": "true"}, {"enabled": true, "skip_model_request": "false"},
		{"enabled": true, "region": "invalid", "sceneId": "scene", "prefix": "prefix"},
		{"enabled": true, "region": "cn", "sceneId": "", "prefix": "prefix"},
	} {
		a := networkTestApp(t)
		obj(obj(a.cfg["http"])["captcha"])["mode"] = "auto"
		installCaptchaScene(t, a, scene)
		_, failure := a.captchaHeaders(context.Background(), nil, false)
		if failure == nil || failure.Code != "captcha_config_invalid" {
			t.Fatalf("invalid scene accepted: %v / %+v", scene, failure)
		}
	}
	a := networkTestApp(t)
	obj(obj(a.cfg["http"])["captcha"])["mode"] = "always"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, failure := a.captchaHeaders(ctx, nil, false)
	if failure == nil || failure.Code != "captcha_timeout" {
		t.Fatalf("canceled operation was not stopped: %+v", failure)
	}
	for _, options := range []any{
		"not-an-object", M{"mode": "invalid"}, M{"simulate_canvas_webgl": "true"},
		M{"node_path": ""}, M{"jsdom_path": "bad\x00path"}, M{"timeout_ms": "30000"},
		M{"timeout_ms": 4999}, M{"timeout_ms": 120001}, M{"timeout_ms": 5000.5}, M{"unknown": true},
	} {
		if validateCaptchaOptions(options) == nil {
			t.Fatalf("invalid captcha setting accepted: %v", options)
		}
	}
	for _, value := range []int{5000, 30000, 120000} {
		must(t, validateCaptchaOptions(M{"timeout_ms": value}))
	}
}

func TestCaptchaNodeProcessUsesOfflineSDK(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the child-process integration test")
	}
	jsdom, err := filepath.Abs(filepath.Join("..", "..", "captcha_node", "node_modules", "jsdom"))
	must(t, err)
	if _, err = os.Stat(jsdom); err != nil {
		t.Skip("install captcha_node dependencies to run the offline Node integration")
	}
	a := networkTestApp(t)
	modulePath := t.TempDir()
	directory := filepath.Join(modulePath, "node_modules", "jsdom")
	must(t, os.MkdirAll(directory, 0700))
	// Only the SDK download is replaced; JSDOM, its realm, the embedded helper,
	// Go/Node stdin/stdout and process lifecycle all execute normally.
	source := `const library = require(` + js(jsdom) + `);
class OfflineResources extends library.ResourceLoader {
  fetch(url) {
    if (url !== 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js') throw new Error('Unexpected network request');
    return Promise.resolve(Buffer.from('window.initAliyunCaptcha = options => options.getInstance({startTracelessVerification() { options.success("offline-process-ticket"); }});'));
  }
}
module.exports = {...library, ResourceLoader: OfflineResources};`
	must(t, os.WriteFile(filepath.Join(directory, "index.js"), []byte(source), 0600))
	parameters := M{"origin": "https://offline.example", "scene_id": "offline", "region": "cn", "prefix": "offline", "module_path": modulePath, "timeout_ms": 10000, "simulate_canvas_webgl": true}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := a.runCaptcha(ctx, M{"node_path": node}, parameters)
	must(t, err)
	equal(t, result["ok"], true)
	equal(t, result["param"], "offline-process-ticket")
	ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err = a.runCaptcha(ctx, M{"node_path": node}, parameters)
	if err == nil && result["code"] != "captcha_canceled" {
		t.Fatal("canceled Node child unexpectedly succeeded")
	}
	var output limitedCaptchaOutput
	if _, err = output.Write(make([]byte, (64<<10)+1)); err == nil {
		t.Fatal("unbounded helper output was accepted")
	}
}
