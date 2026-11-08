package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type diskJar struct {
	mu      sync.Mutex
	path    string
	cookies map[string]string
}

func (a *App) jar(id string) *diskJar {
	a.mu.Lock()
	defer a.mu.Unlock()
	if j := a.jars[id]; j != nil {
		return j
	}
	j := &diskJar{path: filepath.Join(a.data, "cookies", id+".json"), cookies: map[string]string{}}
	if b, e := os.ReadFile(j.path); e == nil {
		for k, v := range obj(parse(string(b), nil)) {
			j.cookies[k] = str(v)
		}
	}
	a.jars[id] = j
	return j
}
func (j *diskJar) header() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []string{}
	for k, v := range j.cookies {
		out = append(out, k+"="+v)
	}
	return strings.Join(out, "; ")
}
func (j *diskJar) capture(res *http.Response) {
	cookies := res.Cookies()
	if len(cookies) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cookies {
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			delete(j.cookies, c.Name)
		} else {
			j.cookies[c.Name] = c.Value
		}
	}
	_ = os.MkdirAll(filepath.Dir(j.path), 0700)
	_ = os.WriteFile(j.path, []byte(js(j.cookies)), 0600)
}
func platform() string {
	p := runtime.GOOS
	if p == "windows" {
		p = "win32"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return p + "-" + arch
}
func (a *App) client(ctx context.Context) (*http.Client, error) {
	proxy := selectedProxy(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.clients[proxy]; c != nil {
		return c, nil
	}
	if len(a.clients) >= 128 {
		for key, cached := range a.clients {
			cached.CloseIdleConnections()
			delete(a.clients, key)
			break
		}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxIdleConns = 200
	t.MaxIdleConnsPerHost = 100
	t.IdleConnTimeout = 90 * time.Second
	t.DialContext = (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.ForceAttemptHTTP2 = true
	if proxy != "" {
		u, e := url.Parse(proxy)
		if e != nil || !validProxy(proxy) {
			return nil, invalid("proxy_url 仅支持 http(s)://host:port")
		}
		t.Proxy = http.ProxyURL(u)
		// Record failures at the proxy dial itself, before DNS resolution loses
		// the configured hostname. Later TLS/target/stream errors remain upstream.
		dial := t.DialContext
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, err := dial(ctx, network, address)
			if err != nil {
				return nil, &proxyDialError{err}
			}
			return connection, nil
		}
	}
	c := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	a.clients[proxy] = c
	return c, nil
}

type apiResult struct {
	Status    int
	JSON      M
	Header    http.Header
	OK        bool
	errorBody []byte
	failure   *Failure
}

func validProxy(s string) bool {
	u, e := url.Parse(s)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n <= 65535
	}
	return !strings.HasSuffix(u.Host, ":")
}

func (a *App) api(ctx context.Context, account M, method, path, token string, body any, headers M, jar *diskJar) (result apiResult, err error) {
	if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
		path = a.origin + path
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ctx, release, err := a.pinOutboundProxy(ctx, account)
	if err != nil {
		return apiResult{}, err
	}
	defer release()
	ctx, finish, failure := a.beginUpstreamRequest(ctx, a.upstreamScope(ctx, account, path, token != "" || str(headers["Authorization"]) != ""))
	if failure != nil {
		a.prepareProxyFailover(ctx, failure)
		return apiResult{}, failure
	}
	defer func() {
		if err != nil {
			failure := upstreamRequestFailure(ctx, err)
			finish(failure)
			a.prepareProxyFailover(ctx, failure)
			if proxyExitFailure(failure) {
				err = failure
			}
		} else {
			finish(result.failure)
			a.prepareProxyFailover(ctx, result.failure)
		}
	}()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(js(body))
	}
	req, e := http.NewRequestWithContext(ctx, method, path, reader)
	if e != nil {
		return apiResult{}, e
	}
	cfg := a.config()
	req.Header.Set("User-Agent", "ZCode/"+str(cfg["app_version"]))
	req.Header.Set("X-ZCode-App-Version", str(cfg["app_version"]))
	req.Header.Set("X-Platform", platform())
	req.Header.Set("X-Client-Language", "zh-CN")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, str(v))
	}
	if jar != nil {
		if s := jar.header(); s != "" {
			req.Header.Set("Cookie", s)
		}
	}
	c, e := a.client(ctx)
	if e != nil {
		return apiResult{}, e
	}
	res, e := c.Do(req)
	if e != nil {
		return apiResult{}, e
	}
	defer res.Body.Close()
	if jar != nil {
		jar.capture(res)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if e != nil {
		if res.StatusCode == http.StatusTooManyRequests {
			return apiResult{}, &Failure{Kind: "rate_limit", Code: "rate_limit_error", Status: res.StatusCode, Message: "请求频率受限", Retryable: true, RetryAfter: retryAfter(res.Header.Get("Retry-After"), time.Now())}
		}
		return apiResult{}, e
	}
	if len(b) > 32<<20 {
		return apiResult{}, invalid("上游响应超过 32MB")
	}
	j := obj(parse(string(b), nil))
	result = apiResult{Status: res.StatusCode, JSON: j, Header: res.Header, OK: res.StatusCode == 200 && (codeIs(j["code"], 0) || codeIs(j["code"], 200))}
	if !result.OK {
		result.errorBody = append([]byte(nil), b[:min(len(b), 64<<10)]...)
		result.failure = classify(upstreamHTTPFailure(res, result.errorBody))
	}
	return result, nil
}
func apiFailure(r apiResult, label string) error {
	failure := apiResponseFailure(r)
	failure.Message = label + ": " + conciseFailureMessage(failure)
	return failure
}

func apiResponseFailure(r apiResult) *Failure {
	if r.failure != nil {
		failure := *r.failure
		return &failure
	}
	failure := providerFailure(r.JSON, r.Status, r.Header.Get("Retry-After"))
	if len(r.errorBody) > 0 {
		failure = upstreamHTTPFailure(&http.Response{StatusCode: r.Status, Header: r.Header}, r.errorBody)
	}
	return classify(failure)
}
func (a *App) customer(ctx context.Context, account M, token string) (apiResult, error) {
	host := "https://bigmodel.cn"
	if str(account["family"]) == "zai" {
		host = "https://api.z.ai"
	}
	return a.api(ctx, account, "GET", host+"/api/biz/customer/getCustomerInfo", "", nil, M{"Authorization": "Bearer " + token}, nil)
}
