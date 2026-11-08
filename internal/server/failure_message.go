package server

import (
	"fmt"
	"strings"
)

// Keep gateway errors and current/historical logs on the same bounded summary.
func conciseFailureMessage(f *Failure) string {
	copy := *f
	classify(&copy)
	switch copy.Code {
	case "3007":
		if copy.Kind == "rate_limit" {
			return "请求频率受限（3007）"
		}
		if copy.Kind == "risk" || explicitCaptchaChallenge(copy.Message) {
			return "需要 SDK 验证（3007）"
		}
	case "upstream_waf_blocked":
		return "上游安全网关拦截"
	case "coding_plan_security_verification_required":
		return "需要 SDK 验证"
	case "CAPTCHA_VERIFY_FAILED":
		return "SDK 验证未通过"
	case "captcha_expired", "captcha_invalid", "F019":
		return "SDK 票据失效"
	case "F008", "F018":
		return "SDK 票据重复提交"
	case "F014":
		return "SDK 初始化记录缺失或失效"
	case "F020":
		return "SDK 票据与场景或用户不匹配"
	case "captcha_risk_blocked", "F001":
		return "SDK 风控拒绝（F001）"
	case "captcha_ip_rate_limited", "F010":
		return "SDK 出口访问频率受限（F010）"
	case "captcha_device_rate_limited", "F011":
		return "SDK 设备访问频率受限（F011）"
	case "captcha_duplicate_result":
		return "SDK 票据重复"
	case "captcha_interaction_required":
		return "SDK 需要人工验证"
	case "captcha_scene_disabled":
		return "SDK 验证场景未启用"
	case "captcha_timeout":
		return "SDK 验证超时"
	case "captcha_network_error":
		return "SDK 连接失败"
	case "captcha_resource_http_error":
		if copy.Status == 429 {
			return "SDK 资源请求受限"
		}
		return "SDK 资源请求失败"
	case "captcha_config_unavailable":
		return "SDK 配置获取失败"
	case "captcha_config_risk_blocked":
		return "SDK 配置请求被风控拦截"
	case "captcha_config_invalid":
		return "SDK 配置无效"
	case "captcha_config_changed":
		return "SDK 配置已更新，请重试"
	case "captcha_session_invalid":
		return "领取验证会话失效或不匹配，请重新验证"
	case "captcha_jsdom_missing":
		return "SDK 缺少 jsdom"
	case "captcha_runtime_error":
		return "SDK 运行失败"
	case "captcha_invalid_result":
		return "SDK 票据无效"
	case "captcha_capacity_exceeded":
		return "SDK 票据缓存已满"
	case "captcha_proxy_unsupported":
		return "SDK 不支持当前代理"
	case "account_busy":
		return "本地排队超时"
	case "queue_full":
		return "本地等待队列已满"
	case "no_account":
		return "无可用账号"
	case "proxy_auth_failed":
		return "代理认证失败"
	case "proxy_connection_failed":
		return "代理连接失败"
	case "proxy_pool_exhausted":
		return "代理池暂不可用"
	case "proxy_upstream_blocked":
		return "代理出口暂被上游拦截"
	case "proxy_config_invalid":
		return "代理配置无效"
	case "proxy_unavailable":
		return "代理不可用"
	case "proxy_runtime_missing":
		return "未找到 Mihomo"
	case "proxy_runtime_error":
		return "代理内核运行失败"
	case "proxy_subscription_invalid":
		return "代理订阅无效"
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
	case "proxy_subscription_response_too_large":
		return "代理订阅响应超过 2 MB"
	case "proxy_subscription_empty":
		return "代理订阅无可用节点"
	case "proxy_subscription_too_large":
		return "代理节点数量超限"
	case "proxy_canceled":
		return "代理准备已取消"
	case "upstream_stream_error":
		return "上游响应中断"
	case "truncated_stream":
		return "上游响应不完整"
	case "first_token_timeout":
		return "上游首字超时"
	case "stream_idle_timeout":
		return "上游流空闲超时"
	case "inference_timeout":
		return "上游推理超时"
	case "captcha_config_timeout":
		return "SDK 配置获取超时"
	case "throttle_window_exhausted":
		return "限流重试超时"
	case "retry_budget_exhausted":
		return "故障重试超时"
	case "canceled":
		return "请求已取消"
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
	switch copy.Kind {
	case "account_risk":
		return "账号风控拒绝"
	case "risk":
		return "上游风控拦截"
	case "auth":
		return "账号凭据失效"
	case "quota":
		return "账号额度不足"
	case "permission":
		return "上游拒绝访问"
	case "rate_limit":
		return "请求频率受限"
	case "busy":
		return "账号并发已满"
	case "overload":
		return "上游服务过载"
	case "network":
		return "上游连接失败"
	case "context":
		return "上下文超限"
	case "server":
		return "上游服务异常"
	case "canceled":
		return "请求已取消"
	case "timeout":
		return "上游请求超时"
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
