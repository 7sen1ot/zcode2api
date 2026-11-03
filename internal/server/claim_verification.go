package server

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a *App) claimVerification(ctx context.Context, id string) (M, error) {
	account := a.account(id)
	if account == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	scene, failure := a.fetchCaptchaScene(ctx, account)
	if failure != nil {
		return nil, failure
	}
	if !truth(scene["enabled"]) {
		return M{"enabled": false}, nil
	}
	return M{"enabled": true, "scene_id": scene["sceneId"], "region": scene["region"], "prefix": scene["prefix"]}, nil
}

func validCaptchaParam(param string) bool {
	if strings.TrimSpace(param) == "" || len(param) > 32<<10 {
		return false
	}
	for _, character := range param {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validateClaimVerification(value M) error {
	param, supplied := value["captcha_verify_param"]
	region, hasRegion := value["captcha_region"]
	if !supplied && !hasRegion {
		return nil
	}
	ticket, ok := param.(string)
	if !ok || !validCaptchaParam(ticket) || (region != "cn" && region != "sgp") {
		return fmt.Errorf("领取验证票据或地域无效，请重新完成安全验证")
	}
	return nil
}

func (a *App) reserveCaptchaTicket(param string) *Failure {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.used == nil {
		a.captcha.used = map[string]time.Time{}
	}
	for key, expires := range a.captcha.used {
		if time.Now().After(expires) {
			delete(a.captcha.used, key)
		}
	}
	id := hash(param)
	if _, exists := a.captcha.used[id]; exists {
		return &Failure{Kind: "risk", Code: "captcha_duplicate_result", Message: "验证票据已使用，请重新完成安全验证"}
	}
	if len(a.captcha.used) >= 2048 {
		return &Failure{Kind: "server", Code: "captcha_capacity_exceeded", Message: "安全验证票据保护缓存已满，请稍后重试"}
	}
	a.captcha.used[id] = time.Now().Add(20 * time.Minute)
	return nil
}

func claimRequiresVerification(code any) bool {
	switch str(code) {
	case "3007", "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "F008", "F018", "F019":
		return true
	}
	return false
}
