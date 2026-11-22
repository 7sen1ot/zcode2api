'use strict';

const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawn, execFile } = require('node:child_process');
const { browserLaunchTimeout, captchaExpression } = require('./captcha.cjs');

function processInfo(pid) {
  try {
    const stat = fs.readFileSync('/proc/' + pid + '/stat', 'utf8');
    const fields = stat.slice(stat.lastIndexOf(') ') + 2).split(' ');
    return { pid: Number(pid), parent: Number(fields[1]), started: fields[19] };
  } catch { return null; }
}

async function stopBrowser(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  if (process.platform === 'win32') {
    const exited = new Promise(resolve => child.once('exit', resolve));
    const killed = await new Promise(resolve => execFile('taskkill', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, timeout: 5000 }, error => resolve(!error)));
    if (killed) await exited;
    return;
  }
  // Keep descendants in the Go worker's process group for forced shutdown.
  // Snapshot ownership before closing Chrome; never signal an unrelated reused PID.
  let processes = [];
  try { processes = fs.readdirSync('/proc').filter(name => /^\d+$/.test(name)).map(processInfo).filter(Boolean); } catch {}
  const owned = new Set([child.pid]), descendants = [];
  for (let changed = true; changed;) {
    changed = false;
    for (const item of processes) {
      if (!owned.has(item.pid) && owned.has(item.parent)) {
        owned.add(item.pid); descendants.push(item); changed = true;
      }
    }
  }
  await new Promise(resolve => {
    const finish = () => { clearTimeout(timer); resolve(); };
    const timer = setTimeout(() => child.kill('SIGKILL'), 1000);
    child.once('exit', finish);
    child.kill('SIGTERM');
  });
  for (const item of descendants.reverse()) {
    if (processInfo(item.pid)?.started !== item.started) continue;
    try { process.kill(item.pid, 'SIGKILL'); } catch {}
  }
}

// A service-owned browser runs the SDK without a debugger. Its private loopback channel
// carries SDK commands/results only; account credentials and billing requests stay in Go.
class NativeClaimBrowser {
  constructor(executable) {
    this.executable = executable;
  }

  async open(config) {
    const proxy = config.proxy_url || '';
    const identity = config.action === 'warm' ? '' : config.session_key || crypto.randomBytes(32).toString('hex');
    if (this.child && this.child.exitCode === null && this.child.signalCode === null && (config.action === 'warm' || (this.proxy === proxy && (!this.identity || this.identity === identity)))) {
      if (identity) this.identity = identity;
      return;
    }
    await this.close();
    if (this.stopping) return;
    this.proxy = proxy;
    this.identity = identity;
    this.launchOutput = '';
    this.nonce = crypto.randomBytes(24).toString('hex');
    this.server = http.createServer((request, response) => this.handle(request, response));
    await new Promise((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(0, '127.0.0.1', resolve);
    });
    if (this.stopping) return;
    this.origin = `http://127.0.0.1:${this.server.address().port}`;
    fs.mkdirSync(config.browser_profiles, { recursive: true, mode: 0o700 });
    const profile = this.profile = fs.mkdtempSync(path.join(config.browser_profiles, 'browser-'));
    const args = ['--user-data-dir=' + profile, '--no-first-run', '--no-default-browser-check'];
    if (process.platform === 'win32') args.push('--start-minimized');
    if (process.platform === 'linux') {
      args.push('--disable-dev-shm-usage', '--disable-background-networking', '--disable-component-update', '--password-store=basic');
    }
    // Match Playwright's root-service compatibility without changing non-root sandboxing.
    if (process.platform === 'linux' && process.getuid() === 0) args.push('--no-sandbox');
    args.push(proxy ? '--proxy-server=' + new URL(proxy).origin : '--no-proxy-server');
    args.push('--app=' + this.origin + '/' + this.nonce);
    const child = this.child = spawn(this.executable, args, { windowsHide: true, env: config.browser_env, stdio: ['ignore', 'ignore', 'pipe'] });
    child.stderr.on('data', chunk => { this.launchOutput = (this.launchOutput + chunk).slice(-8192); });
    const onBrowserExit = () => {
      if (this.child !== child) return;
      const reason = /sandbox|zygote_host_impl/i.test(this.launchOutput) ? 'sandbox'
        : /DISPLAY|X server|X11|ozone_platform_x11/i.test(this.launchOutput) ? 'display'
        : /shared librar|cannot open shared object/i.test(this.launchOutput) ? 'system_library' : 'browser_exit';
      this.finish({ ok: false, code: 'captcha_browser_launch_failed', browser_launch_reason: reason, sdk_executed: !!this.job?.sent && !this.job?.warm });
      void this.close();
    };
    child.once('error', onBrowserExit);
    child.once('exit', onBrowserExit);
    await new Promise((resolve, reject) => { child.once('spawn', resolve); child.once('error', reject); });
    if (process.platform !== 'win32') return;
    // Chrome app windows can ignore STARTUPINFO's hidden state. Hide only the
    // process we just created; never touch the user's browser or profile.
    const command = `Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public static class ClaimWindow { [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr hWnd, int nCmdShow); }'; for($i=0;$i -lt 30;$i++){ $p=Get-Process -Id ${Number(child.pid)} -ErrorAction SilentlyContinue; if(!$p){break}; if($p.MainWindowHandle -ne 0){ [ClaimWindow]::ShowWindowAsync($p.MainWindowHandle,0) | Out-Null; break }; Start-Sleep -Milliseconds 100 }`;
    const powershell = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32/WindowsPowerShell/v1.0/powershell.exe');
    this.hider = execFile(powershell, ['-NoProfile', '-NonInteractive', '-WindowStyle', 'Hidden', '-Command', command], { windowsHide: true, timeout: 10000 }, () => {});
  }

  html() {
    return `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>ZCode 领取验证</title></head><body><script>
    (async () => {
      const root = location.pathname;
      for (;;) {
        try {
          const response = await fetch(root + '/next', { cache: 'no-store' });
          if (response.status === 204) continue;
          if (!response.ok) break;
          const job = await response.json();
          let result;
          try {
            result = job.expression ? await (0, eval)(job.expression) : { ok: true };
            result = { ...window.__captchaProgress, ...result, runtime: 'native_browser', browser_version: navigator.userAgent.match(/(?:Chrome|Edg)\\/([0-9.]+)/)?.[1] || '', browser_visibility: document.visibilityState };
            result.browser_webdriver = navigator.webdriver;
            result.browser_origin = location.origin;
          } catch { result = { ok: false, code: 'captcha_runtime_error', stage: 'sdk_init' }; }
          if (!job.warm || !result.ok) window.__disposeCaptcha?.();
          await fetch(root + '/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: job.id, result }) });
        } catch { await new Promise(resolve => setTimeout(resolve, 500)); }
      }
    })();</script></body></html>`;
  }

  handle(request, response) {
    response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Referrer-Policy', 'no-referrer');
    response.setHeader('X-Frame-Options', 'DENY');
    const root = '/' + this.nonce;
    if (request.headers.host !== new URL(this.origin).host || (request.headers.origin && request.headers.origin !== this.origin)) {
      response.writeHead(403); response.end(); return;
    }
    if (request.method === 'GET' && request.url === root) {
      response.setHeader('Content-Type', 'text/html; charset=utf-8');
      response.end(this.html()); return;
    }
    if (request.method === 'GET' && request.url === root + '/next') {
      if (this.poll) { response.writeHead(409); response.end(); return; }
      this.poll = response;
      this.pollTimer = setTimeout(() => {
        if (this.poll === response) { this.poll = null; response.writeHead(204); response.end(); }
      }, 20000);
      response.on('close', () => {
        if (this.poll === response) { clearTimeout(this.pollTimer); this.poll = null; }
      });
      this.dispatch(); return;
    }
    if (request.method === 'POST' && request.url === root + '/result' && request.headers.origin === this.origin) {
      let body = '', size = 0;
      request.on('data', chunk => {
        size += chunk.length;
        if (size > 64 * 1024) { request.destroy(); return; }
        body += chunk;
      });
      request.on('end', () => {
        try {
          const data = JSON.parse(body);
          if (!this.job || data.id !== this.job.id || !data.result || typeof data.result !== 'object' || Array.isArray(data.result)) {
            response.writeHead(409); response.end(); return;
          }
          this.finish(data.result);
          response.end('ok');
        } catch { response.writeHead(400); response.end(); }
      });
      return;
    }
    response.writeHead(404); response.end();
  }

  dispatch() {
    if (!this.poll || !this.job || this.job.sent) return;
    const response = this.poll;
    this.poll = null;
    clearTimeout(this.pollTimer);
    this.job.sent = true;
    response.setHeader('Content-Type', 'application/json');
    response.end(JSON.stringify({ id: this.job.id, expression: this.job.expression, warm: this.job.warm }));
  }

  finish(result) {
    const job = this.job;
    if (!job) return;
    this.job = null;
    clearTimeout(job.timer);
    if (job.warm) this.ready = !!result.ok;
    job.resolve({ ...result, session_reused: false });
  }

  async run(config) {
    // Preparation binds the account and proxy, without creating a verification ticket.
    this.opening = this.open(config);
    try {
      await this.opening;
    } finally {
      this.opening = null;
    }
    if (this.stopping) return { ok: false, code: 'captcha_canceled' };
    if (!this.child || this.child.exitCode !== null || this.child.signalCode !== null) return { ok: false, code: 'captcha_browser_launch_failed', sdk_executed: false };
    if (this.job) return { ok: false, code: 'captcha_capacity_exceeded' };
    const warm = config.action === 'warm' || config.action === 'prepare';
    if (!warm && !this.ready) {
      const ready = await this.execute(config, true);
      if (!ready.ok || this.stopping) return ready;
    }
    return this.execute(config, warm);
  }

  execute(config, warm) {
    const timeout = warm ? browserLaunchTimeout(config) : Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
    return new Promise(resolve => {
      const job = this.job = {
        id: crypto.randomBytes(16).toString('hex'), resolve, sent: false,
        warm, expression: warm ? '' : captchaExpression(config, 7000),
      };
      job.timer = setTimeout(async () => {
        if (this.job !== job) return;
        this.job = null;
        // Cancel late SDK callbacks before accepting another job on this profile.
        await this.close();
        resolve({ ok: false, code: job.warm ? 'captcha_browser_launch_failed' : 'captcha_timeout', stage: job.warm ? 'browser_launch' : 'sdk_verify', browser_launch_reason: job.warm ? 'timeout' : undefined, sdk_executed: job.sent && !job.warm, runtime: 'native_browser', session_reused: false });
      }, timeout);
      this.dispatch();
    });
  }

  async shutdown() {
    this.stopping = true;
    await this.opening?.catch(() => {});
    await this.close();
  }

  close() {
    this.closing ||= this.closeBrowser().finally(() => { this.closing = null; });
    return this.closing;
  }

  async closeBrowser() {
    this.finish({ ok: false, code: 'captcha_canceled' });
    clearTimeout(this.pollTimer);
    this.poll?.end(); this.poll = null;
    this.server?.closeAllConnections(); this.server?.close(); this.server = null;
    this.hider?.kill(); this.hider = null;
    const child = this.child; this.child = null;
    const profile = this.profile; this.profile = null;
    this.identity = '';
    this.ready = false;
    await stopBrowser(child);
    if (child?.pid && child.exitCode === null && child.signalCode === null) return;
    if (profile) {
      // Windows can keep profile files locked briefly after the browser exits.
      await fs.promises.rm(profile, { recursive: true, force: true, maxRetries: 4, retryDelay: 100 }).catch(() => {});
    }
  }
}

module.exports = { NativeClaimBrowser };
