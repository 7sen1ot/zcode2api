package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
)

const InferenceDriver = "http"

const DefaultBalancePollInterval = 30

const DefaultUsageLogRetentionDays = 30

const EditableSettings = "poll_interval_sec poll_jitter_sec poll_concurrency daily_claim_enabled daily_claim_time conversation_affinity_minutes usage_log_retention_days default_reasoning_level aliases retry http outbound_proxy github_update_mirror zcode_template_mode zcode_subagent_template zcode_subagent_insert_enabled zcode_subagent_harness_enabled zcode_subagent_files_enabled zcode_subagent_comments_enabled zcode_subagent_finishing_work_enabled zcode_subagent_memory_notes_enabled"

func defaults() shared.M {
	return shared.M{
		"zcode_template_mode": "main", "zcode_subagent_template": "",
		"zcode_subagent_insert_enabled": false, "zcode_subagent_harness_enabled": false,
		"zcode_subagent_files_enabled": false, "zcode_subagent_comments_enabled": false,
		"zcode_subagent_finishing_work_enabled": false, "zcode_subagent_memory_notes_enabled": false,
		"http":     shared.M{"first_token_timeout_ms": 60000, "idle_timeout_ms": 120000, "total_timeout_ms": 0, "captcha": shared.M{"mode": "off", "claim_runtime": "jsdom", "browser_backend": "auto", "browser_headless": true, "browser_display": "", "playwright_path": "", "browser_path": "", "node_path": "node", "jsdom_path": "", "simulate_canvas_webgl": true, "timeout_ms": 30000, "ticket_ttl_ms": 75000}},
		"web_port": 6789, "gateway_port": 6780, "host": "127.0.0.1", "admin_password": "", "poll_interval_sec": DefaultBalancePollInterval, "poll_jitter_sec": 0, "poll_concurrency": shared.AccountOperationConcurrency, "web_only_ping_min": 30, "conversation_affinity_minutes": 10, "default_reasoning_level": "low",
		"daily_claim_enabled":      true,
		"daily_claim_time":         DefaultDailyClaimTime,
		"usage_log_retention_days": DefaultUsageLogRetentionDays,
		"github_update_mirror":     "",
		"retry":                    shared.M{"rate_limit_retry_interval_ms": 1000, "max_attempts": 10, "base_delay_ms": 2000, "backoff_factor": 2, "max_delay_ms": 60000, "jitter": true, "busy_wait_sec": 60, "total_budget_ms": 120000, "min_request_window_sec": 60},
		"aliases":                  shared.M{"glm-flash": "GLM-5.3-Flash", "glm-5.3-flash": "GLM-5.3-Flash", "glm-5.3": "GLM-5.3", "glm-5.4": "GLM-5.4", "glm-5.5": "GLM-5.5"}, "bind_public": false, "proxy_url": "", "outbound_proxy": routing.ProxyDefaults(), "trust_proxy": false, "admin_bind_public": false,
	}
}

// Preserve the former custom mode as subagent with its four sections enabled.
func MigrateTemplateSettings(cfg shared.M) {
	if shared.Str(cfg["zcode_template_mode"]) != "subagent_custom" {
		return
	}
	cfg["zcode_template_mode"] = "subagent"
	for _, key := range []string{"zcode_subagent_insert_enabled", "zcode_subagent_harness_enabled", "zcode_subagent_files_enabled", "zcode_subagent_comments_enabled"} {
		if _, present := cfg[key]; !present {
			cfg[key] = true
		}
	}
}

func DeepMerge(base, over shared.M, unknown bool) shared.M {
	out := shared.Clone(base)
	for k, v := range over {
		b, ok := base[k]
		if !ok && !unknown {
			continue
		}
		if _, ok := b.(map[string]any); ok {
			if _, ok := v.(map[string]any); ok {
				out[k] = DeepMerge(shared.Obj(b), shared.Obj(v), k == "aliases")
				continue
			}
		}
		out[k] = v
	}
	return out
}

func MergeOverride(key string, current, override any) any {
	switch key {
	case "http", "retry", "outbound_proxy":
		return DeepMerge(shared.Obj(current), shared.Obj(override), false)
	default:
		return override
	}
}

// Drop retired persisted settings while preserving sparse overrides and aliases.
func KnownConfig(base, over shared.M) shared.M {
	out := shared.M{}
	for key, value := range over {
		field, exists := base[key]
		if !exists {
			continue
		}
		if nested, ok := field.(map[string]any); ok && key != "aliases" {
			value = KnownConfig(nested, shared.Obj(value))
		}
		out[key] = value
	}
	return out
}

func Load(root, data string) (shared.M, error) {
	cfg := defaults()
	b, e := os.ReadFile(filepath.Join(root, "config.json"))
	if e == nil {
		var c shared.M
		if e = json.Unmarshal(b, &c); e != nil {
			return nil, fmt.Errorf("config.json 解析失败: %w", e)
		}
		MigrateTemplateSettings(c)
		cfg = DeepMerge(cfg, c, false)
		// Preserve proxy_url deployments without overriding the new enable switch.
		if _, present := c["outbound_proxy"]; !present && shared.Str(cfg["proxy_url"]) != "" {
			cfg["outbound_proxy"] = shared.Merge(routing.ProxyDefaults(), shared.M{"enabled": true, "urls": []any{cfg["proxy_url"]}})
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e = os.MkdirAll(data, 0700); e != nil {
		return nil, e
	}
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
	if !shared.Truth(cfg["admin_password"]) {
		p := filepath.Join(data, "admin-password.txt")
		b, e := os.ReadFile(p)
		if e == nil {
			cfg["admin_password"] = strings.TrimSpace(string(b))
		} else if os.IsNotExist(e) {
			cfg["admin_password"] = shared.Random(9)
			if e = os.WriteFile(p, []byte(shared.Str(cfg["admin_password"])), 0600); e != nil {
				return nil, e
			}
		} else {
			return nil, e
		}
	}
	return cfg, nil
}
