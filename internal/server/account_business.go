package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

type businessTokenFlight struct {
	done  chan struct{}
	token string
	err   error
}

// This exchanges a global Z.ai OAuth token for a business JWT. It does not
// refresh the ZCode login JWT, and must never receive a zai_cn access token.
func (a *App) exchangeZaiBusinessToken(ctx context.Context, account shared.M, oauth string) (string, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := shared.NowMS()
	res, err := a.api(ctx, account, "POST", "https://api.z.ai/api/auth/z/login", "", shared.M{"token": oauth}, nil, nil)
	if err != nil {
		return "", 0, err
	}
	d := shared.Obj(res.JSON["data"])
	token := strings.TrimSpace(shared.Str(shared.First(shared.Nullable(d["access_token"]), shared.Nullable(d["accessToken"]))))
	code := shared.Str(res.JSON["code"])
	if res.Status < 200 || res.Status >= 300 || res.JSON["success"] == false || (code != "" && code != "0" && code != "200") || token == "" {
		return "", 0, fault.ApiFailure(res, "Z.ai 业务令牌兑换失败")
	}
	expiry := accountdata.JwtExpiresAt(token)
	if ttl := shared.First(d["expires_in"], d["expiresIn"]); ttl != nil {
		seconds := shared.Num(ttl)
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 9007199254 {
			return "", 0, fmt.Errorf("Z.ai 返回的业务令牌有效期无效")
		}
		deadline := started + int64(seconds*1000)
		if expiry == 0 || deadline < expiry {
			expiry = deadline
		}
	}
	if expiry > 0 && expiry <= shared.NowMS() {
		return "", 0, fmt.Errorf("Z.ai 返回的业务令牌已过期")
	}
	return token, expiry, nil
}

// Only commit derived material if the login which requested it is still current.
func (a *App) updateLoginMaterialIfCurrent(snapshot, changes, fields shared.M) (bool, error) {
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	id := shared.Str(snapshot["id"])
	current := a.account(id)
	if current == nil || current["enc_tokens"] != snapshot["enc_tokens"] || current["status"] == "disabled" {
		return false, nil
	}
	enc, err := a.encryptedTokenChanges(current, changes)
	if err != nil {
		return false, err
	}
	fields = shared.Clone(fields)
	fields["enc_tokens"] = enc
	// Keep applyMu held through the write and balance invalidation.
	if err := a.persistAccountFields(id, fields); err != nil {
		return false, err
	}
	a.invalidateBalanceLocked(id)
	return true, nil
}

func (a *App) accountBusinessToken(ctx context.Context, account shared.M, rejected string) (string, error) {
	snapshot := account
	account = a.account(shared.Str(snapshot["id"]))
	if account == nil || account["status"] == "disabled" {
		return "", fmt.Errorf("账号不存在或已停用")
	}
	if !a.sameLoginSession(snapshot, account) {
		return "", fmt.Errorf("账号登录已变化，请重试")
	}
	tokens := a.accountTokens(account)
	// A late 401 may only invalidate the exact business token that was used.
	// Keep the main login and refresh material intact, even if re-exchange fails.
	if rejected != "" && tokens["business_token"] == rejected {
		_, err := a.updateLoginMaterialIfCurrent(account, shared.M{"business_token": nil, "business_token_obtained_at": nil}, shared.M{"business_token_expires_at": nil})
		if err != nil {
			return "", err
		}
		account = a.account(shared.Str(snapshot["id"]))
		if account == nil || account["status"] == "disabled" || !a.sameLoginSession(snapshot, account) {
			return "", fmt.Errorf("账号登录已变化，请重试")
		}
		tokens = a.accountTokens(account)
	}
	token := tokens["business_token"]
	expiry := shared.Integer(account["business_token_expires_at"])
	if jwtExpiry := accountdata.JwtExpiresAt(token); jwtExpiry > 0 && (expiry == 0 || jwtExpiry < expiry) {
		expiry = jwtExpiry
	}
	valid := token != "" && token != rejected && (expiry == 0 || expiry > shared.NowMS())
	canExchange := accountdata.LoginProvider(shared.Str(account["family"]), tokens) == "zai" && tokens["oauth_access"] != "" && !accountdata.LoginJWTExpired(tokens)
	if !canExchange {
		if valid {
			return token, nil
		}
		return "", fmt.Errorf("缺少有效业务凭据，请重新导入或授权；主登录凭据保持不变")
	}
	if valid && ((expiry > shared.NowMS()+120000) || (expiry == 0 && shared.Integer(tokens["business_token_obtained_at"])+300000 > shared.NowMS())) {
		return token, nil
	}
	key := shared.Str(account["id"]) + ":" + shared.Hash(shared.Str(account["enc_tokens"]))
	a.mu.Lock()
	if a.businessRefreshes == nil {
		a.businessRefreshes = map[string]*businessTokenFlight{}
	}
	flight := a.businessRefreshes[key]
	if flight == nil {
		flight = &businessTokenFlight{done: make(chan struct{})}
		a.businessRefreshes[key] = flight
		a.background(func() {
			defer func() {
				a.mu.Lock()
				delete(a.businessRefreshes, key)
				close(flight.done)
				a.mu.Unlock()
			}()
			fresh, expiresAt, err := a.exchangeZaiBusinessToken(a.ctx, account, tokens["oauth_access"])
			if err != nil {
				kind := upstreamRequestFailure(a.ctx, err).Kind
				if valid && expiry > shared.NowMS() && (kind == "network" || kind == "timeout" || kind == "server" || kind == "rate_limit" || kind == "overload") {
					flight.token = token
					return
				}
				flight.err = err
				return
			}
			if fresh == rejected {
				flight.err = fmt.Errorf("Z.ai 返回的业务凭据仍被拒绝，请重新授权")
				return
			}
			changed, err := a.updateLoginMaterialIfCurrent(account, shared.M{"business_token": fresh, "business_token_obtained_at": fmt.Sprint(shared.NowMS())}, shared.M{"business_token_expires_at": accountdata.OptionalExpiry(expiresAt)})
			if err != nil {
				flight.err = err
			} else if !changed {
				flight.err = fmt.Errorf("账号登录已变化，请重新读取业务凭据")
			} else {
				flight.token = fresh
			}
		})
	}
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-flight.done:
		current := a.account(shared.Str(account["id"]))
		if current == nil || current["status"] == "disabled" || !a.sameLoginSession(account, current) {
			return "", fmt.Errorf("账号登录已变化，请重试")
		}
		if flight.token != "" && a.accountTokens(current)["business_token"] != flight.token {
			return "", fmt.Errorf("业务凭据已更新，请重试")
		}
		if flight.token != "" && flight.token == rejected {
			return "", fmt.Errorf("业务凭据被拒绝，请重新导入或授权")
		}
		return flight.token, flight.err
	}
}

func (a *App) businessCustomer(ctx context.Context, account shared.M) (fault.ApiResult, error) {
	token, err := a.accountBusinessToken(ctx, account, "")
	if err != nil {
		return fault.ApiResult{}, err
	}
	res, err := a.customer(ctx, account, token)
	if err != nil || res.OK || fault.ApiResponseFailure(res).Kind != "auth" {
		return res, err
	}
	token, err = a.accountBusinessToken(ctx, account, token)
	if err != nil {
		return fault.ApiResult{}, err
	}
	return a.customer(ctx, account, token)
}
