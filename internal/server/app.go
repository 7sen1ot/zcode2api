package server

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type accountState struct {
	Inflight       int
	LastUsed       int64
	Cooldown       int64
	BusyUntil      int64
	BusyGeneration int64
	BusyProbe      bool
}
type stickyEntry struct {
	Account string
	At      int64
}
type responseEntry struct {
	Key      string
	Input    []any
	Response M
	Bytes    int
}
type profileEntry struct {
	Key   string
	Name  any
	Until int64
	Done  chan struct{}
}
type App struct {
	db                   *sql.DB
	data, secret         string
	root                 string
	version              string
	log                  *log.Logger
	logfile              *os.File
	web                  fs.FS
	configMu             sync.RWMutex
	cfg                  M
	mu                   sync.Mutex
	states               map[string]*accountState
	upstreamCooldowns    map[string]*upstreamCooldown
	cooldownGeneration   uint64
	available            chan struct{}
	scheduleMu           sync.Mutex
	waiters              []*accountWaiter
	sticky               map[string]stickyEntry
	sessions             map[string]int64
	responses            map[string]responseEntry
	responseOrder        []string
	responseBytes        int
	clients              map[string]*http.Client
	jars                 map[string]*diskJar
	claimLocks           map[string]bool
	claimReschedule      chan struct{}
	flowLocks            map[string]bool
	reauth               map[string]int64
	pollDue              map[string]int64
	polling              map[string]bool
	profiles             map[string]*profileEntry
	ctx                  context.Context
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	webPort, gatewayPort int
	origin               string
	captcha              captchaManager
	proxies              outboundProxyManager
	mihomoInstallMu      sync.Mutex
	updates              updateManager
}

func New(root, data string, web fs.FS, buildVersion string) (*App, error) {
	cfg, e := loadConfig(root, data)
	if e != nil {
		return nil, e
	}
	secret, e := loadSecret(data)
	if e != nil {
		return nil, e
	}
	d, e := openDB(data)
	if e != nil {
		return nil, e
	}
	_ = os.MkdirAll(filepath.Join(data, "logs"), 0700)
	lf, _ := os.OpenFile(filepath.Join(data, "logs", "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	var out io.Writer = os.Stdout
	if lf != nil {
		out = io.MultiWriter(os.Stdout, lf)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{db: d, data: data, secret: secret, log: log.New(out, "", log.LstdFlags), logfile: lf, web: web, cfg: cfg, ctx: ctx, cancel: cancel, states: map[string]*accountState{}, sticky: map[string]stickyEntry{}, sessions: map[string]int64{}, responses: map[string]responseEntry{}, clients: map[string]*http.Client{}, jars: map[string]*diskJar{}, claimLocks: map[string]bool{}, claimReschedule: make(chan struct{}, 1), flowLocks: map[string]bool{}, reauth: map[string]int64{}, pollDue: map[string]int64{}, polling: map[string]bool{}, origin: "https://zcode.z.ai"}
	a.root = root
	a.version = buildVersion
	saved := obj(a.setting("config_overrides", M{}))
	clean := M{}
	for k, v := range saved {
		if _, exists := cfg[k]; !exists {
			continue
		}
		if k == "http" || k == "retry" || k == "outbound_proxy" {
			part := M{}
			for field, value := range obj(v) {
				if _, exists := obj(cfg[k])[field]; exists {
					if k == "http" && field == "captcha" {
						options := M{}
						for name, option := range obj(value) {
							if _, exists := obj(obj(cfg[k])[field])[name]; exists {
								options[name] = option
							}
						}
						value = options
					}
					part[field] = value
				}
			}
			v = part
			a.cfg[k] = deepMerge(obj(cfg[k]), part, false)
		} else {
			a.cfg[k] = v
		}
		clean[k] = v
	}
	a.cfg["inference_driver"] = "http"
	if clean["inference_driver"] != nil {
		clean["inference_driver"] = "http"
	}
	if js(clean) != js(saved) {
		_ = a.setSetting("config_overrides", clean)
	}
	a.ensureGroups()
	a.background(a.runAccountQueue)
	return a, nil
}
func (a *App) config() M { a.configMu.RLock(); defer a.configMu.RUnlock(); return clone(a.cfg) }
func (a *App) Close() {
	a.cancel()
	a.closeOutboundProxies()
	a.wg.Wait()
	a.mu.Lock()
	for _, c := range a.clients {
		c.CloseIdleConnections()
	}
	a.mu.Unlock()
	_ = a.db.Close()
	if a.logfile != nil {
		a.logfile.Close()
	}
}
func (a *App) background(f func()) { a.wg.Go(f) }
func (a *App) ensureGroups() {
	if a.row("SELECT id FROM groups WHERE id='g_main'") == nil {
		legacy := a.rows("SELECT id FROM groups WHERE id IN ('g_domestic','g_intl','g_backup')")
		if len(legacy) > 0 {
			_ = a.exec("INSERT INTO groups(id,name,created_at) VALUES('g_main','默认池',?)", nowMS())
			_ = a.exec("UPDATE accounts SET group_id='g_main' WHERE group_id IN ('g_domestic','g_intl','g_backup')")
			_ = a.exec("DELETE FROM groups WHERE id IN ('g_domestic','g_intl','g_backup')")
		}
	}
	if len(a.rows("SELECT id FROM groups")) == 0 {
		_ = a.exec("INSERT INTO groups(id,name,created_at) VALUES('g_main','默认池',?)", nowMS())
	}
}
func (a *App) Run(ctx context.Context) error {
	cfg := a.config()
	host := str(cfg["host"])
	gwHost, webHost := host, host
	if truth(cfg["bind_public"]) {
		gwHost = "0.0.0.0"
		if truth(cfg["admin_bind_public"]) {
			webHost = gwHost
		}
	}
	wl, e := net.Listen("tcp", net.JoinHostPort(webHost, str(cfg["web_port"])))
	if e != nil {
		return e
	}
	gl, e := net.Listen("tcp", net.JoinHostPort(gwHost, str(cfg["gateway_port"])))
	if e != nil {
		wl.Close()
		return e
	}
	a.webPort = wl.Addr().(*net.TCPAddr).Port
	a.gatewayPort = gl.Addr().(*net.TCPAddr).Port
	w := &http.Server{Handler: a.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	g := &http.Server{Handler: a.GatewayHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	info := M{"web_port": a.webPort, "gateway_port": a.gatewayPort, "pid": os.Getpid(), "backend": "go", "updated_at": time.Now().UTC().Format(time.RFC3339)}
	if e = os.WriteFile(filepath.Join(a.data, "ports.json"), []byte(js(info)), 0600); e != nil {
		wl.Close()
		gl.Close()
		return e
	}
	a.log.Printf("Go HTTP 服务已启动 Web: http://%s:%d 网关: http://%s:%d/v1 管理密码文件: %s", host, a.webPort, host, a.gatewayPort, filepath.Join(a.data, "admin-password.txt"))
	a.audit("system", "startup", info, "")
	a.startWorkers()
	errs := make(chan error, 2)
	go func() { errs <- w.Serve(wl) }()
	go func() { errs <- g.Serve(gl) }()
	select {
	case <-ctx.Done():
	case e = <-errs:
		if e == http.ErrServerClosed {
			e = nil
		}
	}
	a.cancel()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = w.Shutdown(shutdown)
	_ = g.Shutdown(shutdown)
	_ = w.Close()
	_ = g.Close()
	return e
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, js(v))
}
func readBody(w http.ResponseWriter, r *http.Request) (M, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if e != nil {
		return nil, &Failure{Status: 413, Kind: "invalid", Code: "body_too_large", Message: "请求体超过限制"}
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return M{}, nil
	}
	v := parse(string(b), nil)
	m, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("请求体必须为 JSON 对象")
	}
	return m, nil
}
func (a *App) ip(r *http.Request) string {
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	if truth(a.config()["trust_proxy"]) {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	return ip
}
func (a *App) health(w http.ResponseWriter, r *http.Request, scope string) bool {
	if r.Method != "GET" {
		return false
	}
	switch r.URL.Path {
	case "/healthz":
		writeJSON(w, 200, M{"ok": true, "scope": scope, "version": a.version, "inference_driver": "http", "backend": "go", "ts": nowMS()})
		return true
	case "/readyz":
		n := integer(a.row("SELECT COUNT(*) n FROM accounts WHERE status='active'")["n"])
		status := 200
		if n == 0 {
			status = 503
		}
		writeJSON(w, status, M{"ok": n > 0, "scope": scope, "active_accounts": n, "inference_driver": "http"})
		return true
	}
	return false
}
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				a.log.Printf("request panic: %v", v)
				writeJSON(w, 500, M{"ok": false, "error": "内部服务错误"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (a *App) scheduleNext(id string, delay time.Duration) {
	a.mu.Lock()
	a.pollDue[id] = nowMS() + delay.Milliseconds()
	a.mu.Unlock()
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(max(d, time.Millisecond))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (a *App) account(id string) M { return a.row("SELECT * FROM accounts WHERE id=?", id) }
func (a *App) groups() []any       { return a.rows("SELECT * FROM groups ORDER BY created_at") }
func (a *App) defaultChain() []any {
	out := []any{}
	groups := a.groups()
	for _, role := range []string{"primary", "backup"} {
		for _, v := range groups {
			g := obj(v)
			if truth(g["enabled"]) && str(g["role"]) == role {
				out = append(out, g["id"])
				break
			}
		}
	}
	return out
}
