package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type proxyProbeScope struct {
	key        string
	generation uint64
}

type proxyProbeResult struct {
	verdict       string
	retryAfter    time.Duration
	observedRetry time.Time
}

func (a *App) startProxyHealthProbes() {
	a.proxyHealthOnce.Do(func() {
		a.background(func() {
			for a.ctx.Err() == nil {
				// Install cancellation before settings can replace this configuration.
				a.configMu.RLock()
				options := merge(proxyDefaults(), obj(a.cfg["outbound_proxy"]))
				active := truth(options["enabled"]) && options["source"] == "manual"
				ctx, cancel := context.WithCancel(a.ctx)
				a.proxies.mu.Lock()
				if active {
					a.proxies.configureLocked(options, manualProxyEndpoints(arr(options["urls"])))
				}
				scope := proxyProbeScope{a.proxies.key, a.proxies.generation}
				a.proxies.probeCancel = cancel
				a.proxies.mu.Unlock()
				a.configMu.RUnlock()
				if active {
					a.probeManualExits(ctx, options, scope)
				}
				cancel()
				timer := time.NewTimer(5 * time.Minute)
				select {
				case <-a.ctx.Done():
					timer.Stop()
					return
				case <-a.proxyHealthReschedule:
					timer.Stop()
				case <-timer.C:
				}
			}
		})
	})
}

func (a *App) probeManualExits(ctx context.Context, options M, scope proxyProbeScope) {
	endpoints := manualProxyEndpoints(arr(options["urls"]))
	jobs := make(chan proxyEndpoint)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(endpoints)); i++ {
		workers.Go(func() {
			for endpoint := range jobs {
				if ctx.Err() != nil {
					continue
				}
				result := a.probeManualExit(ctx, endpoint.URL)
				a.applyExitVerdict(ctx, scope, endpoint.identity(), result)
			}
		})
	}
	for _, endpoint := range endpoints {
		if !validProxy(endpoint.URL) {
			continue
		}
		select {
		case jobs <- endpoint:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func (a *App) probeManualExit(parent context.Context, address string) proxyProbeResult {
	a.proxies.mu.Lock()
	observedRetry := a.proxies.retryAt[address]
	a.proxies.mu.Unlock()
	status, failure, err := a.probeProxy(parent, address)
	if err != nil {
		if status == 0 && failure != nil && proxyExitFailure(failure) {
			return proxyProbeResult{verdict: "quarantine"}
		}
		return proxyProbeResult{}
	}
	switch {
	case failure.Code == "upstream_waf_blocked":
		return proxyProbeResult{verdict: "blocked", retryAfter: failure.RetryAfter}
	case proxyExitFailure(failure):
		return proxyProbeResult{verdict: "quarantine", retryAfter: failure.RetryAfter}
	case status == http.StatusUnauthorized:
		return proxyProbeResult{verdict: "reachable", observedRetry: observedRetry}
	default:
		return proxyProbeResult{}
	}
}

// Share anonymous probes without changing proxy state or sending credentials.
func (a *App) probeProxy(parent context.Context, address string) (int, *Failure, error) {
	ctx, cancel := context.WithTimeout(pinnedProxyContext(parent, address), 8*time.Second)
	defer cancel()
	client, err := a.client(ctx)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.origin+"/api/v1/zcode-plan/anthropic/v1/messages", strings.NewReader("{}"))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, transportFailure(ctx, err), err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, upstreamHTTPFailure(response, body), err
}

func (a *App) applyExitVerdict(ctx context.Context, scope proxyProbeScope, identity string, result proxyProbeResult) {
	if identity == "" || result.verdict == "" || ctx.Err() != nil {
		return
	}
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	options := merge(proxyDefaults(), obj(a.cfg["outbound_proxy"]))
	if !truth(options["enabled"]) || options["source"] != "manual" || hash(js(options)) != scope.key {
		return
	}
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if a.proxies.closing || ctx.Err() != nil || a.proxies.key != scope.key || a.proxies.generation != scope.generation {
		return
	}
	a.proxies.initStateLocked()
	present := false
	for _, endpoint := range a.proxies.endpoints {
		present = present || endpoint.identity() == identity
	}
	if !present {
		return
	}
	now := time.Now()
	changed := false
	switch result.verdict {
	case "reachable":
		if a.proxies.retryAt[identity] != result.observedRetry {
			return // A newer connection failure supersedes this probe.
		}
		changed = a.proxies.unavailable[identity] || !a.proxies.retryAt[identity].IsZero()
		delete(a.proxies.unavailable, identity)
		delete(a.proxies.retryAt, identity)
	case "blocked":
		until := now.Add(max(10*time.Minute, result.retryAfter))
		if until.After(a.proxies.blocked[identity]) {
			a.proxies.blocked[identity] = until
			changed = true
		}
	case "quarantine":
		until := now.Add(max(time.Minute, result.retryAfter))
		if until.After(a.proxies.retryAt[identity]) {
			a.proxies.unavailable[identity] = true
			a.proxies.retryAt[identity] = until
			changed = true
		}
	}
	if changed {
		a.persistProxyStateLocked()
	}
}

func (a *App) testOutboundProxies(parent context.Context, options M) (M, error) {
	if err := validateOutboundProxy(options); err != nil {
		return nil, err
	}
	options = merge(proxyDefaults(), options)
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	endpoints := []proxyEndpoint{{URL: "", Name: "直连"}}
	if truth(options["enabled"]) {
		endpoints = nil
		if options["source"] == "subscription" {
			runtime, err := a.startManagedProxy(ctx, options)
			if err != nil {
				return nil, err
			}
			defer runtime.stop()
			endpoints = runtime.endpoints
		} else {
			endpoints = manualProxyEndpoints(arr(options["urls"]))
		}
	}
	results := make([]any, len(endpoints))
	var workers sync.WaitGroup
	jobs := make(chan int)
	for i := 0; i < min(4, len(endpoints)); i++ {
		workers.Go(func() {
			for index := range jobs {
				endpoint := endpoints[index]
				started := time.Now()
				result := M{"name": endpoint.Name, "reachable": false, "transport_reachable": false, "failure_kind": "network", "message": "连接失败或超时"}
				status, failure, _ := a.probeProxy(ctx, endpoint.URL)
				if status != 0 {
					result["transport_reachable"] = true
					result["status"] = status
					classify(failure)
					blocked := failure.Code == "upstream_waf_blocked" || failure.Kind == "account_risk"
					reachable := status == 401 && !blocked
					result["reachable"] = reachable
					result["blocked"] = blocked
					result["code"], result["failure_kind"] = failure.Code, failure.Kind
					if reachable {
						result["failure_kind"], result["code"] = "", ""
					}
					if failure.Kind == "account_risk" {
						// Anonymous probes cannot identify an affected account or exit.
						result["failure_kind"] = "risk"
						result["message"] = "上游风控拒绝"
						if failure.Code == "3012" {
							result["message"] = "上游风控拒绝（3012）"
						}
					} else if blocked {
						result["message"] = "上游安全网关拦截"
					} else if status == 401 {
						result["message"] = "出口可达；仍需用真实推理确认账号与模型可用"
					} else {
						result["message"] = "已连接，但上游未返回预期的鉴权响应"
					}
				} else if failure != nil {
					result["code"] = failure.Code
				}
				result["elapsed_ms"] = time.Since(started).Milliseconds()
				results[index] = result
			}
		})
	}
	for i := range endpoints {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return M{"ok": true, "results": results, "note": "检测不保存设置、不发送账号令牌、不计模型用量；连通性通过不代表真实推理一定通过风控"}, nil
}
