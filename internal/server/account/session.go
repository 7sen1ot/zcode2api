package account

import (
	"fmt"
	"math"
	"strings"
	"zcode2api/internal/server/shared"
)

// Login providers and billing families are distinct in ZCode 3.18.2.
// zai_cn uses a ZCode subject and may optionally link a BigModel customer.
func ValidLoginProvider(provider string) bool {
	return provider == "bigmodel" || provider == "zai" || provider == "zai_cn"
}

func LoginFamily(provider string) string {
	if provider == "zai_cn" {
		return "bigmodel"
	}
	return provider
}

func LoginProvider(family string, tokens map[string]string) string {
	if provider := tokens["active_provider"]; provider != "" {
		return provider
	}
	// Existing installations and older exports did not record the login channel.
	return family
}

func JwtExpiresAt(token string) int64 {
	exp := shared.Num(JwtPayload(token)["exp"])
	if math.IsNaN(exp) || math.IsInf(exp, 0) || exp <= 0 || exp > 9007199254740 {
		return 0
	}
	return int64(exp * 1000)
}

func OptionalExpiry(expiry int64) any {
	if expiry <= 0 {
		return nil
	}
	return expiry
}

func LoginJWTExpired(tokens map[string]string) bool {
	return LoginJWTExpiredAt(JwtExpiresAt(tokens["zcode_jwt"]), shared.NowMS())
}

func LoginJWTExpiredAt(expiry, now int64) bool {
	return expiry > 0 && expiry <= now+30000
}

func LoginProfile(profile shared.M) shared.M {
	// Store useful identity fields, never an arbitrary response or avatar blob.
	result := shared.M{}
	for _, key := range []string{"user_id", "id", "name", "username", "displayName", "email", "customerNumber", "customerName", "nickName"} {
		if value, ok := profile[key].(string); ok && strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func NewLoginTokens(provider, jwt string, profile shared.M) (shared.M, string, error) {
	identity := shared.Str(JwtPayload(jwt)["user_id"])
	if !ValidLoginProvider(provider) || jwt == "" || identity == "" || len(strings.Split(jwt, ".")) != 3 {
		return nil, "", fmt.Errorf("缺少有效的 ZCode 登录身份或登录渠道")
	}
	if subject := shared.Str(profile["user_id"]); subject != "" && subject != identity {
		return nil, "", fmt.Errorf("用户资料与 ZCode 凭据身份不一致，未更新账号")
	}
	// A new session replaces all channel-specific material in one write. Missing
	// refresh/linked tokens must not inherit credentials from an older login.
	tokens := shared.M{
		"zcode_jwt": jwt, "active_provider": provider, "session_revision": shared.UUID(),
		"provider_access_token": nil, "provider_access_kind": nil, "refresh_token": nil,
		"oauth_access": nil, "business_token": nil, "business_customer_id": nil,
		"business_token_obtained_at": nil,
		"linked_bigmodel":            nil, "provider_user_info": nil,
	}
	if cleaned := LoginProfile(profile); len(cleaned) > 0 {
		tokens["provider_user_info"] = shared.JSON(cleaned)
	}
	return tokens, identity, nil
}

func AttachLinkedBigModel(tokens shared.M, identity string, linked shared.M) {
	// Local records use camelCase; backend OAuth/profile responses use snake_case.
	if owner := shared.Str(linked["userId"]); owner != "" && owner != identity {
		return
	}
	customer := strings.TrimSpace(shared.Str(shared.First(linked["customerId"], linked["customer_id"])))
	access := strings.TrimSpace(shared.Str(shared.First(linked["accessToken"], linked["access_token"])))
	if customer == "" || access == "" {
		return
	}
	tokens["linked_bigmodel"] = shared.JSON(shared.M{"userId": identity, "customerId": customer, "accessToken": access})
	if tokens["active_provider"] == "zai_cn" {
		tokens["business_token"], tokens["business_customer_id"] = access, customer
	}
}
