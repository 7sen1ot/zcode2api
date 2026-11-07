package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type managedProxyRuntime struct {
	key        string
	endpoints  []proxyEndpoint
	expires    time.Time
	references int
	retired    bool
	done       chan struct{}
	stopOnce   sync.Once
	stopFn     func()
}

func (runtime *managedProxyRuntime) stop() { runtime.stopOnce.Do(runtime.stopFn) }
func (runtime *managedProxyRuntime) alive() bool {
	select {
	case <-runtime.done:
		return false
	default:
		return true
	}
}

func (a *App) acquireManagedProxy(ctx context.Context, options M) (*managedProxyRuntime, error) {
	manager := &a.proxies
	manager.mu.Lock()
	if manager.closing || ctx.Err() != nil {
		manager.mu.Unlock()
		return nil, proxyFailure("proxy_canceled", "代理准备已取消")
	}
	key := hash(js(options))
	current := manager.current
	if current != nil && current.key == key && current.alive() && time.Now().Before(current.expires) {
		current.references++
		manager.mu.Unlock()
		return current, nil
	}
	next, err := a.startManagedProxy(ctx, options)
	if err != nil {
		// A transient refresh failure must not discard a working configuration.
		// Changing the URL/filter/path never silently falls back to an old source.
		if current != nil && current.key == key && current.alive() {
			current.expires = time.Now().Add(time.Minute)
			current.references++
			manager.mu.Unlock()
			return current, nil
		}
		manager.mu.Unlock()
		return nil, err
	}
	next.key, next.references = key, 1
	manager.current = next
	if manager.runtimes == nil {
		manager.runtimes = map[*managedProxyRuntime]bool{}
	}
	manager.runtimes[next] = true
	stop := current != nil && current.references == 0
	if current != nil {
		current.retired = true
		if stop {
			delete(manager.runtimes, current)
		}
	}
	manager.mu.Unlock()
	if stop {
		current.stop()
	}
	return next, nil
}

func (a *App) releaseManagedProxy(runtime *managedProxyRuntime) {
	a.proxies.mu.Lock()
	runtime.references--
	stop := runtime.retired && runtime.references == 0
	if stop {
		delete(a.proxies.runtimes, runtime)
	}
	a.proxies.mu.Unlock()
	if stop {
		runtime.stop()
	}
}

type subscriptionNode struct {
	name   string
	config M
}

func (node subscriptionNode) identity() string {
	config := clone(node.config)
	delete(config, "name")
	return hash(js(config))
}

func decodeSubscription(body []byte, filter string) ([]subscriptionNode, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	if !strings.Contains(text, "://") {
		compact := strings.Join(strings.Fields(text), "")
		var decoded []byte
		var err error
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err = encoding.DecodeString(compact)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, proxyFailure("proxy_subscription_invalid", "订阅不是有效的 URI 或 Base64 URI 列表")
		}
		text = strings.TrimSpace(strings.TrimPrefix(string(decoded), "\ufeff"))
	}
	pattern, err := regexp.Compile(filter)
	if err != nil {
		return nil, proxyFailure("proxy_subscription_invalid", "节点筛选表达式无效")
	}
	seen := map[string]bool{}
	nodes := []subscriptionNode{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Hostname() == "" {
			continue
		}
		name := u.Fragment
		if name == "" {
			name = fmt.Sprintf("节点 %d", len(nodes)+1)
		}
		if !pattern.MatchString(name) || strings.ContainsAny(name, "\r\n\x00") {
			continue
		}
		port := 443
		if u.Port() != "" {
			port, err = strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				continue
			}
		} else if u.Scheme == "http" {
			port = 80
		}
		query := u.Query()
		config := M{"server": u.Hostname(), "port": port, "udp": true}
		flag := func(key string) bool { return query.Get(key) == "1" || strings.EqualFold(query.Get(key), "true") }
		switch u.Scheme {
		case "vless":
			if u.User == nil || u.User.Username() == "" {
				continue
			}
			config["type"], config["uuid"] = "vless", u.User.Username()
			network := query.Get("type")
			if network == "" || network == "raw" {
				network = "tcp"
			}
			if network != "tcp" && network != "ws" {
				continue
			}
			config["network"] = network
			security := query.Get("security")
			config["tls"] = security == "tls" || security == "reality"
			config["skip-cert-verify"] = flag("insecure") || flag("allowInsecure")
			for from, to := range map[string]string{"flow": "flow", "sni": "servername", "fp": "client-fingerprint"} {
				if value := query.Get(from); value != "" {
					config[to] = value
				}
			}
			if security == "reality" {
				if query.Get("pbk") == "" {
					continue
				}
				config["reality-opts"] = M{"public-key": query.Get("pbk"), "short-id": query.Get("sid")}
			}
			if network == "ws" {
				options := M{"path": fallback(query.Get("path"), "/")}
				if host := query.Get("host"); host != "" {
					options["headers"] = M{"Host": host}
				}
				config["ws-opts"] = options
			}
			if value := query.Get("alpn"); value != "" {
				config["alpn"] = strings.Split(value, ",")
			}
		case "hysteria2", "hy2":
			if u.User == nil || u.User.Username() == "" {
				continue
			}
			password := u.User.Username()
			if value, present := u.User.Password(); present {
				password += ":" + value
			}
			config["type"], config["password"] = "hysteria2", password
			config["sni"] = fallback(query.Get("sni"), fallback(query.Get("peer"), u.Hostname()))
			config["skip-cert-verify"] = flag("insecure")
			if pin := query.Get("pinSHA256"); pin != "" {
				config["fingerprint"] = strings.ReplaceAll(pin, ":", "")
			}
			if obfs := query.Get("obfs"); obfs != "" {
				config["obfs"], config["obfs-password"] = obfs, query.Get("obfs-password")
			}
		case "http", "https":
			config["type"], config["tls"] = "http", u.Scheme == "https"
			if u.User != nil {
				config["username"] = u.User.Username()
				config["password"], _ = u.User.Password()
			}
		default:
			continue
		}
		identity := hash(js(config))
		if !seen[identity] {
			config["name"] = fmt.Sprintf("node-%d", len(nodes)+1)
			nodes = append(nodes, subscriptionNode{name, config})
			seen[identity] = true
		}
		if len(nodes) > 64 {
			return nil, proxyFailure("proxy_subscription_too_large", "匹配节点超过 64 个，请收紧节点筛选条件")
		}
	}
	if len(nodes) == 0 {
		return nil, proxyFailure("proxy_subscription_empty", "没有匹配的受支持节点；支持 VLESS TCP/WS、Hysteria2 和 HTTP/HTTPS URI 订阅")
	}
	return nodes, nil
}

func (a *App) subscriptionNodes(ctx context.Context, options M) ([]subscriptionNode, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Subscription bootstrap never recursively uses this pool.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" || request.URL.User != nil {
			return fmt.Errorf("subscription redirect rejected")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, "GET", str(options["subscription_url"]), nil)
	if err != nil {
		return nil, proxyFailure("proxy_subscription_invalid", "订阅地址无效")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, proxyFailure("proxy_subscription_unavailable", "拉取订阅失败，请检查服务器网络及订阅地址")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		failure := &Failure{Kind: "server", Code: "proxy_subscription_unavailable", Status: response.StatusCode,
			Message: fmt.Sprintf("代理订阅获取失败（HTTP %d）", response.StatusCode)}
		// Recognize only known authentication errors; never expose response bodies
		// or request errors, which can contain the private subscription URL.
		text := strings.ToLower(string(body))
		if response.StatusCode == http.StatusUnauthorized || (response.StatusCode == http.StatusForbidden &&
			(strings.Contains(text, "token is error") || strings.Contains(text, "invalid token") || strings.Contains(text, "token expired"))) {
			failure.Code = "proxy_subscription_auth_failed"
			failure.Message = fmt.Sprintf("订阅令牌错误或已失效（HTTP %d），请重新复制有效订阅链接", response.StatusCode)
		}
		return nil, failure
	}
	if readErr != nil {
		return nil, proxyFailure("proxy_subscription_unavailable", "读取代理订阅响应失败，请稍后重试")
	}
	if len(body) > 2<<20 {
		return nil, proxyFailure("proxy_subscription_response_too_large", "代理订阅响应超过 2 MB")
	}
	return decodeSubscription(body, str(options["subscription_filter"]))
}

func (a *App) startManagedProxy(ctx context.Context, options M) (*managedProxyRuntime, error) {
	binary, err := a.resolveMihomo(str(options["mihomo_path"]))
	if err != nil {
		return nil, proxyFailure("proxy_runtime_missing", "未找到 Mihomo；可在设置中点击安装，或填写已有可执行文件路径")
	}
	nodes, err := a.subscriptionNodes(ctx, options)
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(a.data, "runtime")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return nil, proxyFailure("proxy_runtime_error", "无法创建代理运行目录")
	}
	directory, err := os.MkdirTemp(parent, "proxy-")
	if err != nil {
		return nil, proxyFailure("proxy_runtime_error", "无法创建代理运行目录")
	}
	defer func() {
		if err != nil {
			os.RemoveAll(directory)
		}
	}()
	proxies, listeners := []any{}, []any{}
	endpoints := []proxyEndpoint{}
	reserved := []net.Listener{}
	for i, node := range nodes {
		listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
		if listenErr != nil {
			for _, open := range reserved {
				open.Close()
			}
			err = proxyFailure("proxy_runtime_error", "无法分配本机代理端口")
			return nil, err
		}
		reserved = append(reserved, listener)
		address := listener.Addr().(*net.TCPAddr)
		proxies = append(proxies, node.config)
		// Each listener has one fixed outbound. A global load-balance listener
		// would let the SDK and Go's separate CONNECTs pick different exits.
		listeners = append(listeners, M{"name": fmt.Sprintf("in-%d", i), "type": "http", "listen": "127.0.0.1", "port": address.Port, "proxy": node.config["name"]})
		// Local ports change on refresh; node identity must survive that change.
		endpoints = append(endpoints, proxyEndpoint{URL: fmt.Sprintf("http://127.0.0.1:%d", address.Port), Name: node.name, ID: node.identity()})
	}
	config := M{"allow-lan": false, "mode": "rule", "log-level": "silent", "ipv6": false, "listeners": listeners, "proxies": proxies, "rules": []any{"MATCH,REJECT"}}
	filename := filepath.Join(directory, "config.json")
	encoded, _ := json.Marshal(config)
	if err = os.WriteFile(filename, encoded, 0600); err != nil {
		for _, open := range reserved {
			open.Close()
		}
		err = proxyFailure("proxy_runtime_error", "无法写入代理运行配置")
		return nil, err
	}
	for _, open := range reserved {
		open.Close()
	}
	processCtx, cancel := context.WithCancel(a.ctx)
	command := exec.CommandContext(processCtx, binary, "-d", directory, "-f", filename)
	hideChildWindow(command)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.WaitDelay = 2 * time.Second
	if err = command.Start(); err != nil {
		cancel()
		err = proxyFailure("proxy_runtime_error", "无法启动 Mihomo，请检查可执行文件及平台架构")
		return nil, err
	}
	runtime := &managedProxyRuntime{endpoints: endpoints, expires: time.Now().Add(time.Duration(num(options["refresh_interval_sec"])) * time.Second), done: make(chan struct{})}
	go func() { command.Wait(); close(runtime.done) }()
	runtime.stopFn = func() {
		cancel()
		select {
		case <-runtime.done:
		case <-time.After(3 * time.Second):
			command.Process.Kill()
			<-runtime.done
		}
		a.forgetProxyClients(endpoints)
		os.RemoveAll(directory)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	for {
		if !runtime.alive() {
			runtime.stop()
			return nil, proxyFailure("proxy_runtime_error", "Mihomo 启动失败，请检查订阅节点是否受当前内核支持")
		}
		ready := true
		for _, endpoint := range endpoints {
			u, _ := url.Parse(endpoint.URL)
			connection, dialErr := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(readyCtx, "tcp4", u.Host)
			if dialErr != nil {
				ready = false
				break
			}
			connection.Close()
		}
		if ready {
			return runtime, nil
		}
		select {
		case <-readyCtx.Done():
			runtime.stop()
			return nil, proxyFailure("proxy_runtime_error", "Mihomo 代理端口未能就绪")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
