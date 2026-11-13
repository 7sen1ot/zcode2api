'use strict';

const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawn, execFile } = require('node:child_process');
const { captchaExpression } = require('./captcha.cjs');

// A service-owned browser runs the SDK without a debugger. Its private loopback channel
// carries SDK commands/results only; account credentials and billing requests stay in Go.
class NativeClaimBrowser {
  constructor(executable) {
    this.executable = executable;
  }

  async open(config) {
    const proxy = config.proxy_url || '';
    if (this.child && this.child.exitCode === null && (config.action === 'warm' || this.proxy === proxy)) return;
    await this.close();
    this.proxy = proxy;
    this.nonce = crypto.randomBytes(24).toString('hex');
    this.server = http.createServer((request, response) => this.handle(request, response));
    await new Promise((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(0, '127.0.0.1', resolve);
    });
    this.origin = `http://127.0.0.1:${this.server.address().port}`;
    const profile = path.join(config.browser_profiles, crypto.createHash('sha256').update(proxy).digest('hex'));
    fs.mkdirSync(profile, { recursive: true, mode: 0o700 });
    const args = ['--user-data-dir=' + profile, '--no-first-run', '--no-default-browser-check', '--start-minimized'];
    args.push(proxy ? '--proxy-server=' + new URL(proxy).origin : '--no-proxy-server');
    args.push('--app=' + this.origin + '/' + this.nonce);
    const child = this.child = spawn(this.executable, args, { windowsHide: true, stdio: 'ignore' });
    child.once('error', () => this.finish({ ok: false, code: 'captcha_browser_launch_failed' }));
    child.once('exit', () => {
      if (this.child === child) this.finish({ ok: false, code: 'captcha_browser_launch_failed' });
    });
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
    job.resolve({ ...result, session_reused: false });
  }

  async run(config) {
    // Native claims need a fresh SDK instance; only the browser process is warmed.
    if (config.action === 'prepare') return { ok: true, preparation_skipped: true, runtime: 'native_browser' };
    await this.open(config);
    if (this.job) return { ok: false, code: 'captcha_capacity_exceeded' };
    const timeout = config.action === 'warm' ? 20000 : Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
    return new Promise(resolve => {
      const job = this.job = {
        id: crypto.randomBytes(16).toString('hex'), resolve, sent: false,
        warm: config.action === 'warm',
        // Hidden pages need time for asynchronous device collection before verification.
        expression: config.action === 'warm' ? '' : captchaExpression(config, 7000),
      };
      job.timer = setTimeout(async () => {
        if (this.job !== job) return;
        this.job = null;
        // Cancel late SDK callbacks before accepting another job on this profile.
        await this.close();
        resolve({ ok: false, code: 'captcha_timeout', runtime: 'native_browser', session_reused: false });
      }, timeout);
      this.dispatch();
    });
  }

  async close() {
    this.finish({ ok: false, code: 'captcha_canceled' });
    clearTimeout(this.pollTimer);
    this.poll?.end(); this.poll = null;
    this.server?.closeAllConnections(); this.server?.close(); this.server = null;
    this.hider?.kill(); this.hider = null;
    const child = this.child; this.child = null;
    if (child && child.exitCode === null && child.pid) {
      await new Promise(resolve => execFile('taskkill', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, timeout: 5000 }, () => resolve()));
    }
  }
}

module.exports = { NativeClaimBrowser };
