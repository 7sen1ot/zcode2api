package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func defaults() M {
	return M{
		"inference_driver": "http", "http": M{"client_version": "0.17.0", "first_token_timeout_ms": 60000, "idle_timeout_ms": 120000, "total_timeout_ms": 0, "captcha": M{"mode": "off", "node_path": "node", "jsdom_path": "", "simulate_canvas_webgl": true, "timeout_ms": 30000}},
		"web_port": 6789, "gateway_port": 6780, "host": "127.0.0.1", "admin_password": "", "poll_interval_sec": 60, "poll_jitter_sec": 10, "poll_concurrency": 4, "web_only_ping_min": 30, "conversation_affinity_minutes": 10, "claim_auto_min": 240, "claim_auto_enabled": true, "default_reasoning_level": "low", "app_version": "3.17.2",
		"retry":   M{"rate_limit_retry_interval_ms": 1000, "max_attempts": 10, "base_delay_ms": 2000, "backoff_factor": 2, "max_delay_ms": 60000, "jitter": true, "busy_wait_sec": 60, "total_budget_ms": 120000, "min_request_window_sec": 60},
		"aliases": M{"glm-flash": "GLM-5.3-Flash", "glm-5.3-flash": "GLM-5.3-Flash", "glm-5.3": "GLM-5.3"}, "bind_public": false, "proxy_url": "", "outbound_proxy": proxyDefaults(), "trust_proxy": false, "admin_bind_public": false,
	}
}
func deepMerge(base, over M, unknown bool) M {
	out := clone(base)
	for k, v := range over {
		b, ok := base[k]
		if !ok && !unknown {
			continue
		}
		if _, ok := b.(map[string]any); ok {
			if _, ok := v.(map[string]any); ok {
				out[k] = deepMerge(obj(b), obj(v), k == "aliases")
				continue
			}
		}
		out[k] = v
	}
	return out
}
func loadConfig(root, data string) (M, error) {
	cfg := defaults()
	b, e := os.ReadFile(filepath.Join(root, "config.json"))
	if e == nil {
		var c M
		if e = json.Unmarshal(b, &c); e != nil {
			return nil, fmt.Errorf("config.json 解析失败: %w", e)
		}
		cfg = deepMerge(cfg, c, false)
		// Preserve proxy_url deployments without overriding the new enable switch.
		if _, present := c["outbound_proxy"]; !present && str(cfg["proxy_url"]) != "" {
			cfg["outbound_proxy"] = merge(proxyDefaults(), M{"enabled": true, "urls": []any{cfg["proxy_url"]}})
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e = os.MkdirAll(data, 0700); e != nil {
		return nil, e
	}
	cfg["inference_driver"] = "http"
	for env, k := range map[string]string{"ZCODE2API_WEB_PORT": "web_port", "ZCODE2API_GATEWAY_PORT": "gateway_port"} {
		if v, ok := os.LookupEnv(env); ok && v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 || n > 65535 {
				return nil, fmt.Errorf("%s 端口无效", env)
			}
			cfg[k] = n
		}
	}
	for env, k := range map[string]string{"ZCODE2API_HOST": "host", "ZCODE2API_ADMIN_PASSWORD": "admin_password"} {
		if v := os.Getenv(env); v != "" {
			cfg[k] = v
		}
	}
	if !truth(cfg["admin_password"]) {
		p := filepath.Join(data, "admin-password.txt")
		b, e := os.ReadFile(p)
		if e == nil {
			cfg["admin_password"] = strings.TrimSpace(string(b))
		} else if os.IsNotExist(e) {
			cfg["admin_password"] = random(9)
			if e = os.WriteFile(p, []byte(str(cfg["admin_password"])), 0600); e != nil {
				return nil, e
			}
		} else {
			return nil, e
		}
	}
	return cfg, nil
}
