package server

import (
	"fmt"
	"strings"
)

var failureCodeMessages = map[string]string{
	"upstream_waf_blocked":                       "上游安全网关拦截",
	"coding_plan_security_verification_required": "需要 SDK 验证",
	"CAPTCHA_VERIFY_FAILED":                      "SDK 验证未通过",
	"captcha_expired":                            "SDK 票据失效",
	"captcha_invalid":                            "SDK 票据失效",
	"F019":                                       "SDK 票据失效",
	"F008":                                       "SDK 票据重复提交",
	"F018":                                       "SDK 票据重复提交",
	"F014":                                       "SDK 初始化记录缺失或失效",
	"F020":                                       "SDK 票据与场景或用户不匹配",
	"captcha_risk_blocked":                       "SDK 风控拒绝（F001）",
	"F001":                                       "SDK 风控拒绝（F001）",
	"captcha_ip_rate_limited":                    "SDK 出口访问频率受限（F010）",
	"F010":                                       "SDK 出口访问频率受限（F010）",
	"captcha_device_rate_limited":                "SDK 设备访问频率受限（F011）",
	"F011":                                       "SDK 设备访问频率受限（F011）",
	"captcha_duplicate_result":                   "SDK 票据重复",
	"captcha_interaction_required":               "SDK 需要人工验证",
	"captcha_scene_disabled":                     "SDK 验证场景未启用",
	"captcha_timeout":                            "SDK 验证超时",
	"captcha_network_error":                      "SDK 连接失败",
	"captcha_config_unavailable":                 "SDK 配置获取失败",
	"captcha_config_risk_blocked":                "SDK 配置请求被风控拦截",
	"captcha_config_invalid":                     "SDK 配置无效",
	"captcha_config_changed":                     "SDK 配置已更新，请重试",
	"captcha_session_invalid":                    "领取验证会话失效或不匹配，请重新验证",
	"captcha_jsdom_missing":                      "SDK 缺少 jsdom",
	"captcha_playwright_missing":                 "未找到 Playwright 驱动，请在设置中安装或配置已有运行时",
	"captcha_browser_missing":                    "未找到浏览器，请在设置中选择已有浏览器或安装 Chromium",
	"captcha_browser_launch_failed":              "浏览器启动失败，请检查浏览器路径、版本及系统运行依赖",
	"captcha_browser_timeout":                    "等待浏览器准备超时，请稍后重试",
	"captcha_runtime_error":                      "SDK 运行失败",
	"captcha_invalid_result":                     "SDK 票据无效",
	"captcha_capacity_exceeded":                  "SDK 票据缓存已满",
	"captcha_proxy_unsupported":                  "SDK 不支持当前代理",
	"account_busy":                               "本地排队超时",
	"queue_full":                                 "本地等待队列已满",
	"no_account":                                 "无可用账号",
	"proxy_auth_failed":                          "代理认证失败",
	"proxy_connection_failed":                    "代理连接失败",
	"proxy_pool_exhausted":                       "代理池暂不可用",
	"proxy_upstream_blocked":                     "代理出口暂被上游拦截",
	"proxy_config_invalid":                       "代理配置无效",
	"proxy_unavailable":                          "代理不可用",
	"proxy_runtime_missing":                      "未找到 Mihomo",
	"proxy_runtime_error":                        "代理内核运行失败",
	"proxy_subscription_invalid":                 "代理订阅无效",
	"proxy_subscription_response_too_large":      "代理订阅响应超过 2 MB",
	"proxy_subscription_empty":                   "代理订阅无可用节点",
	"proxy_subscription_too_large":               "代理节点数量超限",
	"proxy_canceled":                             "代理准备已取消",
	"upstream_stream_error":                      "上游响应中断",
	"truncated_stream":                           "上游响应不完整",
	"first_token_timeout":                        "上游首字超时",
	"stream_idle_timeout":                        "上游流空闲超时",
	"inference_timeout":                          "上游推理超时",
	"captcha_config_timeout":                     "SDK 配置获取超时",
	"throttle_window_exhausted":                  "限流重试超时",
	"retry_budget_exhausted":                     "故障重试超时",
	"canceled":                                   "请求已取消",
}

var failureKindMessages = map[string]string{
	"account_risk": "账号风控拒绝",
	"risk":         "上游风控拦截",
	"auth":         "账号凭据失效",
	"quota":        "账号额度不足",
	"permission":   "上游拒绝访问",
	"rate_limit":   "请求频率受限",
	"busy":         "账号并发已满",
	"overload":     "上游服务过载",
	"network":      "上游连接失败",
	"context":      "上下文超限",
	"server":       "上游服务异常",
	"canceled":     "请求已取消",
	"timeout":      "上游请求超时",
}

// Keep gateway errors and current/historical logs on the same bounded summary.
func conciseFailureMessage(f *Failure) string {
	copy := *f
	classify(&copy)
	if message := failureCodeMessages[copy.Code]; message != "" {
		return message
	}
	switch copy.Code {
	case "3007":
		if copy.Kind == "rate_limit" {
			return "请求频率受限（3007）"
		}
		if copy.Kind == "risk" || explicitCaptchaChallenge(copy.Message) {
			return "需要 SDK 验证（3007）"
		}
	case "captcha_resource_http_error":
		if copy.Status == 429 {
			return "SDK 资源请求受限"
		}
		return "SDK 资源请求失败"
	case "proxy_subscription_unavailable":
		if copy.Status > 0 {
			return fmt.Sprintf("代理订阅获取失败（HTTP %d）", copy.Status)
		}
		return "代理订阅获取失败"
	case "proxy_subscription_auth_failed":
		if copy.Status > 0 {
			return fmt.Sprintf("订阅令牌错误或已失效（HTTP %d）", copy.Status)
		}
		return "订阅令牌错误或已失效"
	case "captcha_verification_failed":
		code := copy.VerifyCode
		if code == "" {
			if index := strings.LastIndex(copy.Message, "（F"); index >= 0 {
				code = strings.TrimSuffix(strings.TrimPrefix(copy.Message[index:], "（"), "）")
			}
		}
		// Preserve safe SDK codes when summarizing both fresh and historical logs.
		if len(code) == 4 && code[0] == 'F' && code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9' && code[3] >= '0' && code[3] <= '9' {
			if code == "F001" {
				return "SDK 风控拒绝（F001）"
			}
			return "SDK 验证失败（" + code + "）"
		}
		return "SDK 验证失败"
	case "timeout":
		// Older records used one code for all timeout stages.
		switch copy.Message {
		case "HTTP 首字超时":
			return "上游首字超时"
		case "HTTP 上游流空闲超时":
			return "上游流空闲超时"
		case "HTTP 整轮推理超时":
			return "上游推理超时"
		case "读取安全验证配置超时":
			return "SDK 配置获取超时"
		case "排队或重试预算耗尽":
			return "故障重试超时"
		case "上游首字超时", "上游流空闲超时", "上游推理超时", "SDK 配置获取超时", "故障重试超时":
			return copy.Message
		}
		return "上游请求超时"
	}
	if message := failureKindMessages[copy.Kind]; message != "" {
		return message
	}
	message := strings.Join(strings.Fields(copy.Message), " ")
	if message == "" {
		return "上游请求失败"
	}
	runes := []rune(message)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return message
}

func riskBlockMessage(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "request has been blocked due to unusual") ||
		strings.Contains(message, "request blocked due to unusual")
}
