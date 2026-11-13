package server

import (
	"context"
	"errors"
	"fmt"
	"net"
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
			if !ok || identity != "" && !hexIDPattern.MatchString(identity) {
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
			if !integerInRange(value, minimum, 86400) {
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

func manualProxyEndpoints(urls []any) []proxyEndpoint {
	endpoints := []proxyEndpoint{}
	seen := map[string]bool{}
	for _, value := range urls {
		address := str(value)
		if !seen[address] {
			endpoints = append(endpoints, proxyEndpoint{URL: address, Name: maskedProxy(address)})
			seen[address] = true
		}
	}
	return endpoints
}

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
	mu            sync.Mutex
	generation    uint64
	selected      string
	claimCursor   int
	endpoints     []proxyEndpoint
	key           string
	current       *managedProxyRuntime
	runtimes      map[*managedProxyRuntime]bool
	closing       bool
	unavailable   map[string]bool
	retryAt       map[string]time.Time
	blocked       map[string]time.Time
	probeCancel   context.CancelFunc
	persistQueued bool
}

// Call with mu held; the zero-value manager and configuration resets are valid.
func (manager *outboundProxyManager) initStateLocked() {
	if manager.unavailable == nil {
		manager.unavailable = map[string]bool{}
	}
	if manager.retryAt == nil {
		manager.retryAt = map[string]time.Time{}
	}
	if manager.blocked == nil {
		manager.blocked = map[string]time.Time{}
	}
}

func (manager *outboundProxyManager) configureLocked(options M, endpoints []proxyEndpoint) {
	manager.initStateLocked()
	key := hash(js(options))
	if key != manager.key {
		manager.key, manager.selected = key, ""
		manager.generation++
		manager.claimCursor = 0
		for _, endpoint := range endpoints {
			if endpoint.selectionID() == str(options["preferred_node"]) {
				manager.selected = endpoint.identity()
				break
			}
		}
	}
	manager.endpoints = append([]proxyEndpoint(nil), endpoints...)
	manager.pruneStateLocked(time.Now())
}

func (manager *outboundProxyManager) pruneStateLocked(now time.Time) {
	manager.initStateLocked()
	present := map[string]bool{}
	for _, endpoint := range manager.endpoints {
		present[endpoint.identity()] = true
	}
	for identity, until := range manager.retryAt {
		if !present[identity] || !now.Before(until) {
			delete(manager.retryAt, identity)
			delete(manager.unavailable, identity)
		}
	}
	for identity := range manager.unavailable {
		if !present[identity] || !now.Before(manager.retryAt[identity]) {
			delete(manager.unavailable, identity)
		}
	}
	for identity, until := range manager.blocked {
		if !present[identity] || !now.Before(until) {
			delete(manager.blocked, identity)
		}
	}
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
	return a.pinOutboundProxyMode(parent, account, false)
}

// Rotate per claim, keeping scene lookup, SDK verification and submission on the same exit.
func (a *App) pinClaimOutboundProxy(parent context.Context, account M) (context.Context, func(), error) {
	return a.pinOutboundProxyMode(parent, account, true)
}

func (a *App) pinOutboundProxyMode(parent context.Context, account M, rotatePerClaim bool) (context.Context, func(), error) {
	if _, present := parent.Value(proxyContextKey{}).(proxyRoute); present {
		return parent, func() {}, nil
	}
	ctx, cleanup := a.operationContext(parent, 0)
	address := str(account["proxy_url"])
	if address != "" {
		if !validProxy(address) {
			cleanup()
			return parent, func() {}, proxyFailure("proxy_config_invalid", "账号代理地址无效")
		}
		return pinnedProxyContext(ctx, address), cleanup, nil
	}
	options := obj(a.configValue("outbound_proxy"))
	if err := validateOutboundProxy(options); err != nil {
		cleanup()
		return parent, func() {}, proxyFailure("proxy_config_invalid", err.Error())
	}
	options = merge(proxyDefaults(), options)
	if !truth(options["enabled"]) {
		return pinnedProxyContext(ctx, ""), cleanup, nil
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
		endpoints = manualProxyEndpoints(arr(options["urls"]))
	}
	var index int
	var generation uint64
	if rotatePerClaim && options["source"] == "manual" {
		index, generation = a.proxies.selectRotatingEndpoint(options, endpoints)
	} else {
		index, generation = a.proxies.selectEndpoint(options, endpoints)
	}
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
		a.proxies.mu.Lock()
		blocked := len(a.proxies.blocked) > 0 && len(a.proxies.unavailable) == 0
		a.proxies.mu.Unlock()
		if blocked {
			return parent, func() {}, proxyFailure("proxy_upstream_blocked", "代理连接可达，但出口暂被上游安全网关拦截")
		}
		return parent, func() {}, proxyFailure("proxy_pool_exhausted", "无可用代理节点，请检查代理配置")
	}
	ctx = context.WithValue(ctx, proxySelectionKey{}, proxySelection{generation, endpoints[index].identity()})
	return pinnedProxyContext(ctx, endpoints[index].URL), release, nil
}

func (manager *outboundProxyManager) selectEndpoint(options M, endpoints []proxyEndpoint) (int, uint64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.configureLocked(options, endpoints)
	return manager.selectAvailableEndpointLocked(time.Now()), manager.generation
}

// Call with mu held. Expired cooldowns are eligible on selection and failover.
func (manager *outboundProxyManager) selectAvailableEndpointLocked(now time.Time) int {
	manager.pruneStateLocked(now)
	start := 0
	for index, endpoint := range manager.endpoints {
		if endpoint.identity() == manager.selected {
			start = index
			break
		}
	}
	for offset := 0; offset < len(manager.endpoints); offset++ {
		index := (start + offset) % len(manager.endpoints)
		identity := manager.endpoints[index].identity()
		if !manager.unavailable[identity] && manager.blocked[identity].IsZero() {
			manager.selected = identity
			return index
		}
	}
	return -1
}

// A route failure changes the shared selection, never an in-flight pinned exit.
func (a *App) prepareProxyFailover(ctx context.Context, failure *Failure) {
	if failure == nil || ctx.Err() != nil {
		return
	}
	blocked := failure.Code == "upstream_waf_blocked"
	if !blocked && !proxyExitFailure(failure) {
		return
	}
	if _, pooled := ctx.Value(proxySelectionKey{}).(proxySelection); !pooled {
		return
	}
	failure.Retryable = a.markProxyRoute(ctx, blocked, failure.RetryAfter)
	failure.ProxyFailover = failure.Retryable
}

// Claim selection advances its own cursor without moving inference's sticky exit.
func (manager *outboundProxyManager) selectRotatingEndpoint(options M, endpoints []proxyEndpoint) (int, uint64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.configureLocked(options, endpoints)
	if len(endpoints) == 0 {
		return -1, manager.generation
	}
	manager.claimCursor %= len(endpoints)
	for offset := 0; offset < len(endpoints); offset++ {
		index := (manager.claimCursor + offset) % len(endpoints)
		identity := endpoints[index].identity()
		if !manager.unavailable[identity] && manager.blocked[identity].IsZero() {
			manager.claimCursor = (index + 1) % len(endpoints)
			return index, manager.generation
		}
	}
	return -1, manager.generation
}

// Only current pooled routes may retire nodes; canceled and override routes cannot.
func (a *App) markProxyRoute(ctx context.Context, blocked bool, retryAfter time.Duration) bool {
	selection, ok := ctx.Value(proxySelectionKey{}).(proxySelection)
	if !ok || ctx.Err() != nil {
		return false
	}
	a.proxies.mu.Lock()
	a.proxies.initStateLocked()
	if a.proxies.closing || ctx.Err() != nil || selection.generation != a.proxies.generation {
		a.proxies.mu.Unlock()
		return false
	}
	now := time.Now()
	reason := "代理连接失败"
	if blocked {
		reason = "上游安全网关拦截"
		a.proxies.blocked[selection.identity] = proxyCooldownUntil(a.proxies.blocked[selection.identity], now, 10*time.Minute, 2*time.Hour, retryAfter)
	} else {
		a.proxies.unavailable[selection.identity] = true
		a.proxies.retryAt[selection.identity] = proxyCooldownUntil(a.proxies.retryAt[selection.identity], now, 30*time.Second, 5*time.Minute, retryAfter)
	}
	a.persistProxyStateLocked()
	previous := a.proxies.selected
	index := a.proxies.selectAvailableEndpointLocked(now)
	var previousName, nextName string
	if index >= 0 && previous != a.proxies.selected {
		nextName = a.proxies.endpoints[index].Name
		for _, endpoint := range a.proxies.endpoints {
			if endpoint.identity() == previous {
				previousName = endpoint.Name
				break
			}
		}
	}
	a.proxies.mu.Unlock()
	if nextName != "" {
		a.log.Printf("代理节点切换（%s）: %q -> %q", reason, previousName, nextName)
	}
	return index >= 0
}

// Escalate repeated failures without shortening an existing cooldown or Retry-After.
func proxyCooldownUntil(previous, now time.Time, minimum, maximum, retryAfter time.Duration) time.Time {
	delay := max(minimum, retryAfter)
	if previous.After(now) {
		delay = max(retryAfter, min(maximum, max(delay, previous.Sub(now)*2)))
	}
	return maxTime(previous, now.Add(delay))
}

// Account refusals, ticket and runtime failures do not identify a broken exit.
func proxyExitFailure(failure *Failure) bool {
	switch failure.Code {
	case "proxy_auth_failed", "proxy_connection_failed":
		return true
	}
	return false
}

// Persist cooldowns across restarts. Hold proxies.mu; App.Close joins these serialized writes.
func (a *App) persistProxyStateLocked() {
	if a.proxies.persistQueued {
		return
	}
	a.proxies.persistQueued = true
	a.background(func() {
		a.proxies.mu.Lock()
		defer a.proxies.mu.Unlock()
		a.proxies.persistQueued = false
		if err := a.writeProxyStateLocked(); err != nil {
			a.log.Printf("proxy state persistence: %v", err)
		}
	})
}

func (a *App) writeProxyStateLocked() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM proxy_state"); err != nil {
		return err
	}
	now := time.Now()
	identities := map[string]bool{}
	for identity := range a.proxies.blocked {
		identities[identity] = true
	}
	for identity := range a.proxies.retryAt {
		identities[identity] = true
	}
	for identity := range identities {
		var blocked, retry int64
		if until := a.proxies.blocked[identity]; until.After(now) {
			blocked = until.UnixMilli()
		}
		if until := a.proxies.retryAt[identity]; a.proxies.unavailable[identity] && until.After(now) {
			retry = until.UnixMilli()
		}
		if blocked == 0 && retry == 0 {
			continue
		}
		if _, err = tx.Exec("INSERT INTO proxy_state(identity,blocked_until,retry_at) VALUES(?,?,?)", identity, blocked, retry); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) restoreProxyState() {
	now := time.Now()
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	a.proxies.initStateLocked()
	for _, v := range a.rows("SELECT identity,blocked_until,retry_at FROM proxy_state") {
		row := obj(v)
		identity := str(row["identity"])
		if identity == "" {
			continue
		}
		if blocked := time.UnixMilli(integer(row["blocked_until"])); blocked.After(now) {
			a.proxies.blocked[identity] = blocked
		}
		if retry := time.UnixMilli(integer(row["retry_at"])); retry.After(now) {
			a.proxies.unavailable[identity] = true
			a.proxies.retryAt[identity] = retry
		}
	}
}

// A failed request through a proxy does not by itself identify a failed proxy.
// Only connection errors naming the proxy hop can quarantine the route.
type proxyDialError struct{ error }

func (e *proxyDialError) Unwrap() error { return e.error }

func transportFailure(ctx context.Context, err error) *Failure {
	f := &Failure{Kind: "network", Code: "network", Message: "上游连接失败", Retryable: true, Detail: M{"message": boundedFailureText(err.Error())}}
	address := selectedProxy(ctx)
	if address == "" || ctx.Err() != nil {
		return f
	}
	var dialError *proxyDialError
	if errors.As(err, &dialError) {
		f.Code, f.Message = "proxy_connection_failed", "代理连接失败"
		return f
	}
	proxy, parseErr := url.Parse(address)
	if parseErr != nil {
		return f
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if cause.Error() == "Proxy Authentication Required" {
			f.Code, f.Message = "proxy_auth_failed", "代理认证失败"
			return f
		}
	}
	var operation *net.OpError
	if errors.As(err, &operation) {
		port := proxy.Port()
		if port == "" {
			port = "80"
			if proxy.Scheme == "https" {
				port = "443"
			}
		}
		proxyHop := operation.Op == "proxyconnect"
		if operation.Op == "dial" && operation.Addr != nil {
			host, targetPort, splitErr := net.SplitHostPort(operation.Addr.String())
			proxyHop = splitErr == nil && strings.EqualFold(host, proxy.Hostname()) && targetPort == port
		}
		var dns *net.DNSError
		if operation.Op == "dial" && errors.As(operation, &dns) && strings.EqualFold(dns.Name, proxy.Hostname()) {
			proxyHop = true
		}
		if proxyHop {
			f.Code, f.Message = "proxy_connection_failed", "代理连接失败"
		}
	}
	return f
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
	if a.proxies.probeCancel != nil {
		a.proxies.probeCancel()
	}
	old := a.proxies.current
	a.proxies.current = nil
	a.proxies.key, a.proxies.selected = "", ""
	a.proxies.generation++
	a.proxies.endpoints = nil
	a.proxies.claimCursor = 0
	a.proxies.initStateLocked()
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
	wake(a.proxyHealthReschedule)
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
