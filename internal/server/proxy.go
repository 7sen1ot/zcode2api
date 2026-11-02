package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

func proxyDefaults() M {
	return M{"enabled": false, "rotation_enabled": false, "rotation_interval_sec": 0, "source": "manual", "urls": []any{}, "subscription_url": "", "subscription_filter": "", "preferred_node": "", "mihomo_path": "mihomo", "refresh_interval_sec": 3600}
}

func validateOutboundProxyFields(value any) error {
	options, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("outbound_proxy 必须为配置对象")
	}
	for key, value := range options {
		switch key {
		case "enabled", "rotation_enabled":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("outbound_proxy.%s 必须为布尔值", key)
			}
		case "source":
			if value != "manual" && value != "subscription" {
				return fmt.Errorf("代理来源须为 manual 或 subscription")
			}
		case "urls":
			list, ok := value.([]any)
			if !ok || len(list) > 64 {
				return fmt.Errorf("代理列表须为数组，最多 64 个地址")
			}
			for i, item := range list {
				address, ok := item.(string)
				if !ok || len(address) > 4096 || !validProxy(address) {
					return fmt.Errorf("第 %d 个代理地址无效，仅支持 http(s)://[用户名:密码@]主机:端口；订阅链接请使用订阅模式", i+1)
				}
			}
		case "subscription_url":
			address, ok := value.(string)
			if !ok || len(address) > 8192 || strings.ContainsAny(address, "\r\n\x00") {
				return fmt.Errorf("订阅地址无效")
			}
			if address != "" {
				u, err := url.Parse(address)
				if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
					return fmt.Errorf("订阅地址必须为 HTTPS URL，访问参数可放在查询字符串中")
				}
			}
		case "subscription_filter":
			pattern, ok := value.(string)
			if !ok || len(pattern) > 512 {
				return fmt.Errorf("节点筛选表达式过长或格式无效")
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("节点筛选须为有效的正则表达式")
			}
		case "preferred_node":
			identity, ok := value.(string)
			if !ok || identity != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(identity) {
				return fmt.Errorf("指定代理节点标识无效，请重新选择")
			}
		case "mihomo_path":
			path, ok := value.(string)
			if !ok || strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\r\n\x00") {
				return fmt.Errorf("Mihomo 可执行文件路径无效")
			}
		case "refresh_interval_sec", "rotation_interval_sec":
			minimum, label := 60.0, "订阅刷新间隔"
			if key == "rotation_interval_sec" {
				minimum, label = 0, "代理轮换间隔"
			}
			switch value.(type) {
			case int, int64, float64, json.Number:
			default:
				return fmt.Errorf("%s须为 %.0f 到 86400 的整数秒", label, minimum)
			}
			n := num(value)
			if math.IsNaN(n) || n < minimum || n > 86400 || n != math.Trunc(n) {
				return fmt.Errorf("%s须为 %.0f 到 86400 的整数秒", label, minimum)
			}
		default:
			return fmt.Errorf("未知代理设置 outbound_proxy.%s", key)
		}
	}
	return nil
}

func validateOutboundProxy(value any) error {
	if err := validateOutboundProxyFields(value); err != nil {
		return err
	}
	options := merge(proxyDefaults(), obj(value))
	if truth(options["enabled"]) {
		if options["source"] == "manual" && len(arr(options["urls"])) == 0 {
			return fmt.Errorf("启用代理时至少填写一个 HTTP/HTTPS 代理地址")
		}
		if options["source"] == "subscription" && str(options["subscription_url"]) == "" {
			return fmt.Errorf("启用订阅代理时须填写订阅地址")
		}
	}
	return nil
}

type proxyContextKey struct{}
type proxyRoute struct{ URL string }
type proxyEndpoint struct{ URL, Name, ID string }

func (endpoint proxyEndpoint) identity() string {
	if endpoint.ID != "" {
		return endpoint.ID
	}
	return endpoint.URL
}

func (endpoint proxyEndpoint) selectionID() string {
	if endpoint.ID != "" {
		return endpoint.ID
	}
	return hash(endpoint.URL)
}

type proxySelectionKey struct{}
type proxySelection struct {
	generation uint64
	identity   string
}

func selectedProxy(ctx context.Context) string {
	if route, ok := ctx.Value(proxyContextKey{}).(proxyRoute); ok {
		return route.URL
	}
	return ""
}

type outboundProxyManager struct {
	mu          sync.Mutex
	generation  uint64
	selected    string
	endpoints   []proxyEndpoint
	key         string
	current     *managedProxyRuntime
	runtimes    map[*managedProxyRuntime]bool
	closing     bool
	unavailable map[string]bool
}

func asProxyFailure(err error) *Failure {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	return &Failure{Kind: "server", Code: "proxy_unavailable", Message: "出站代理不可用，请检查代理设置或运行环境"}
}

func proxyFailure(code, message string) error {
	return &Failure{Kind: "server", Code: code, Message: message}
}

// Pin scene lookup, SDK verification and inference to the same exit.
func (a *App) pinOutboundProxy(parent context.Context, account M) (context.Context, func(), error) {
	if _, present := parent.Value(proxyContextKey{}).(proxyRoute); present {
		return parent, func() {}, nil
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(a.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	address := str(account["proxy_url"])
	if address != "" {
		if !validProxy(address) {
			cleanup()
			return parent, func() {}, proxyFailure("proxy_config_invalid", "账号代理地址无效")
		}
		return context.WithValue(ctx, proxyContextKey{}, proxyRoute{address}), cleanup, nil
	}
	options := obj(a.config()["outbound_proxy"])
	if err := validateOutboundProxy(options); err != nil {
		cleanup()
		return parent, func() {}, proxyFailure("proxy_config_invalid", err.Error())
	}
	options = merge(proxyDefaults(), options)
	if !truth(options["enabled"]) {
		return context.WithValue(ctx, proxyContextKey{}, proxyRoute{}), cleanup, nil
	}
	var endpoints []proxyEndpoint
	var runtime *managedProxyRuntime
	var err error
	if options["source"] == "subscription" {
		runtime, err = a.acquireManagedProxy(ctx, options)
		if err != nil {
			cleanup()
			return parent, func() {}, err
		}
		endpoints = runtime.endpoints
	} else {
		seen := map[string]bool{}
		for _, value := range arr(options["urls"]) {
			address := str(value)
			if !seen[address] {
				endpoints = append(endpoints, proxyEndpoint{URL: address, Name: maskedProxy(address)})
				seen[address] = true
			}
		}
	}
	index, generation := a.proxies.selectEndpoint(options, endpoints)
	var once sync.Once
	release := func() {
		once.Do(func() {
			if runtime != nil {
				a.releaseManagedProxy(runtime)
			}
			cleanup()
		})
	}
	if index < 0 {
		release()
		return parent, func() {}, proxyFailure("proxy_pool_exhausted", "无可用代理节点，请检查代理配置")
	}
	ctx = context.WithValue(ctx, proxySelectionKey{}, proxySelection{generation, endpoints[index].identity()})
	return context.WithValue(ctx, proxyContextKey{}, proxyRoute{endpoints[index].URL}), release, nil
}

func (manager *outboundProxyManager) selectEndpoint(options M, endpoints []proxyEndpoint) (int, uint64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	key := hash(js(options))
	if key != manager.key {
		manager.key, manager.selected = key, ""
		manager.generation++
		manager.unavailable = map[string]bool{}
		for _, endpoint := range endpoints {
			if endpoint.selectionID() == str(options["preferred_node"]) {
				manager.selected = endpoint.identity()
				break
			}
		}
	}
	manager.endpoints = append([]proxyEndpoint(nil), endpoints...)
	start := 0
	for index, endpoint := range endpoints {
		if endpoint.identity() == manager.selected {
			start = index
			break
		}
	}
	for offset := 0; offset < len(endpoints); offset++ {
		index := (start + offset) % len(endpoints)
		identity := endpoints[index].identity()
		if !manager.unavailable[identity] {
			manager.selected = identity
			return index, manager.generation
		}
	}
	return -1, manager.generation
}

// Only current pooled routes may retire nodes; canceled and override routes cannot.
func (a *App) markProxyFailure(ctx context.Context) bool {
	selection, ok := ctx.Value(proxySelectionKey{}).(proxySelection)
	if !ok || ctx.Err() != nil {
		return false
	}
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if selection.generation != a.proxies.generation {
		return false
	}
	a.proxies.unavailable[selection.identity] = true
	for _, endpoint := range a.proxies.endpoints {
		if !a.proxies.unavailable[endpoint.identity()] {
			return true
		}
	}
	return false
}

// Ticket and runtime failures do not identify a broken exit.
func proxyExitFailure(failure *Failure) bool {
	if failure.Kind == "network" && failure.Retryable {
		return true
	}
	switch failure.Code {
	case "3012", "upstream_waf_blocked", "upstream_risk_blocked":
		return true
	case "captcha_verification_failed":
		return failure.VerifyCode == "F001"
	}
	return failure.Kind == "risk" && riskBlockMessage(failure.Message)
}

func maskedProxy(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return "代理地址"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

func (a *App) forgetProxyClients(addresses []proxyEndpoint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if addresses == nil {
		for key, client := range a.clients {
			client.CloseIdleConnections()
			delete(a.clients, key)
		}
		return
	}
	for _, endpoint := range addresses {
		if client := a.clients[endpoint.URL]; client != nil {
			client.CloseIdleConnections()
			delete(a.clients, endpoint.URL)
		}
	}
}

func (a *App) resetOutboundProxies() {
	a.proxies.mu.Lock()
	old := a.proxies.current
	a.proxies.current = nil
	a.proxies.key, a.proxies.selected = "", ""
	a.proxies.generation++
	a.proxies.endpoints = nil
	a.proxies.unavailable = nil
	stop := old != nil && old.references == 0
	if old != nil {
		old.retired = true
		if stop {
			delete(a.proxies.runtimes, old)
		}
	}
	a.proxies.mu.Unlock()
	if stop {
		old.stop()
	}
	a.forgetProxyClients(nil)
}

func (a *App) closeOutboundProxies() {
	a.proxies.mu.Lock()
	a.proxies.closing = true
	all := a.proxies.runtimes
	a.proxies.runtimes, a.proxies.current = nil, nil
	a.proxies.mu.Unlock()
	for runtime := range all {
		runtime.stop()
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
			for _, value := range arr(options["urls"]) {
				endpoints = append(endpoints, proxyEndpoint{URL: str(value), Name: maskedProxy(str(value))})
			}
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
				result := M{"name": endpoint.Name, "reachable": false, "message": "连接失败或超时"}
				probeCtx, end := context.WithTimeout(context.WithValue(ctx, proxyContextKey{}, proxyRoute{endpoint.URL}), 8*time.Second)
				client, err := a.client(probeCtx)
				if err == nil {
					request, _ := http.NewRequestWithContext(probeCtx, "POST", a.origin+"/api/v1/zcode-plan/anthropic/v1/messages", strings.NewReader("{}"))
					request.Header.Set("Content-Type", "application/json")
					response, failure := client.Do(request)
					if failure == nil {
						body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
						response.Body.Close()
						result["status"] = response.StatusCode
						failure := upstreamHTTPFailure(response, body)
						blocked := (response.StatusCode == 403 || response.StatusCode == 405) && (strings.Contains(string(body), "block_message") || failure.Code == "3012")
						result["reachable"] = response.StatusCode == 401
						result["blocked"] = blocked
						if blocked {
							result["message"] = "上游拒绝了此出口"
						} else if response.StatusCode == 401 {
							result["message"] = "出口可达；仍需用真实推理确认账号与模型可用"
						} else {
							result["message"] = "已连接，但上游未返回预期的鉴权响应"
						}
					}
				}
				end()
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
