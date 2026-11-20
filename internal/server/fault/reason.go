package fault

import (
	"strings"
	"zcode2api/internal/server/shared"
)

// Logs retain a concrete cause separately from the gateway's short category.
func FailureReason(f *Failure) string {
	if f == nil {
		return ""
	}
	detail := shared.Obj(f.Detail)
	body := shared.Obj(shared.Parse(detail["body"], nil))
	upstream := shared.Obj(body["error"])
	data := shared.Obj(body["data"])
	message := ""
	for _, value := range []any{upstream["message"], upstream["msg"], data["message"], body["msg"], body["message"], body["error"], detail["message"], f.Message} {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			message = strings.Join(strings.Fields(text), " ")
			break
		}
	}
	// ZCode can wrap a message as [code][reason][request id]. The complete
	// envelope remains in error_detail; a row needs only its reason.
	parts := strings.Split(message, "][")
	if (len(parts) == 2 || len(parts) == 3) && strings.HasPrefix(parts[0], "[") && strings.HasSuffix(parts[len(parts)-1], "]") {
		prefix := strings.TrimPrefix(parts[0], "[")
		status := shared.Integer(prefix)
		if prefix == f.Code || status >= 100 && status <= 599 {
			message = parts[1]
			if len(parts) == 2 {
				message = strings.TrimSuffix(message, "]")
			}
		}
	}
	switch strings.ToLower(message) {
	case "model concurrency limit exceeded":
		message = "上游模型并发已达上限"
	case "exceed quota limit":
		message = "本次请求超出上游可用额度"
	case "captcha verify failed":
		message = "验证码校验未通过"
	}
	if strings.HasPrefix(message, "Invalid request body: body.") {
		field, cause, ok := strings.Cut(strings.TrimPrefix(message, "Invalid request body: body."), ": ")
		if ok && strings.Contains(strings.ToLower(cause), "input should be a valid integer") {
			message = field + " 必须是整数"
		}
	}
	if strings.HasPrefix(message, "上游安全验证未通过，需要按官方验证流程处理") {
		// The SDK's generic wrapper is less specific than its F-code.
		message = ConciseFailureMessage(f)
	}
	if f.Code == "captcha_runtime_error" {
		switch shared.Str(detail["sdk_stage"]) {
		case "sdk_load":
			message = "SDK 脚本加载失败"
		case "sdk_init":
			message = "SDK 初始化失败"
		case "sdk_ready_wait":
			message = "等待 SDK 就绪失败"
		case "sdk_verify":
			message = "SDK 验证阶段执行失败"
		case "browser_launch":
			message = "验证浏览器启动失败"
		case "browser_page":
			message = "验证页面加载失败"
		case "browser_context":
			message = "浏览器验证环境执行失败"
		}
	}
	if message == "" {
		message = ConciseFailureMessage(f)
	}
	runes := []rune(message)
	if len(runes) > 240 {
		return string(runes[:239]) + "…"
	}
	return message
}
