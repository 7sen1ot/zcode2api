'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const readline = require('node:readline');
const { spawn } = require('node:child_process');

function findExecutable(name) {
  return (process.env.PATH || '').split(path.delimiter).map(dir => path.join(dir, name)).find(executable) || '';
}

function headlessShell(file) {
  return /headless[_-]shell/i.test(path.basename(file));
}

function browserLaunchFailure(error) {
  const message = String(error?.message || error || '');
  const reason = /sandbox|zygote_host_impl/i.test(message) ? 'sandbox'
    : /DISPLAY|X server|X11|ozone_platform_x11/i.test(message) ? 'display'
    : /shared librar|cannot open shared object/i.test(message) ? 'system_library'
    : error?.code === 'ENOENT' ? 'executable_missing' : 'browser_launch';
  return { ok: false, code: error?.code === 'captcha_display_unavailable' ? error.code : 'captcha_browser_launch_failed', stage: 'browser_launch', browser_launch_reason: reason };
}

function displayStatus(config) {
  const display = String(config.browser_display || process.env.DISPLAY || '').trim();
  const xvfb = process.platform === 'linux' ? findExecutable('Xvfb') : '';
  const available = process.platform !== 'linux' || !!display || !!xvfb;
  return { available, display, xvfb_path: xvfb, message: process.platform !== 'linux' ? '无需 Xvfb' : display ? '已配置显示，启动时检查连接' : xvfb ? '可启动专用 Xvfb' : '未配置 DISPLAY，且未找到 Xvfb' };
}

// One display belongs to one worker, so cancellation never touches another app's X server.
class BrowserDisplay {
  async environment(config) {
    const status = displayStatus(config);
    if (process.platform !== 'linux' || status.display) return { ...process.env, ...(status.display ? { DISPLAY: status.display } : {}) };
    if (!status.available) throw Object.assign(new Error('No display available'), { code: 'captcha_display_unavailable' });
    if (this.child?.exitCode === null && this.child.signalCode === null && this.display) return { ...process.env, DISPLAY: this.display };
    await this.close();
    const child = this.child = spawn(status.xvfb_path, ['-displayfd', '3', '-screen', '0', '1280x800x24', '-nolisten', 'tcp'], { stdio: ['ignore', 'ignore', 'ignore', 'pipe'] });
    child.on('error', () => {});
    try {
      this.display = await new Promise((resolve, reject) => {
        let output = '';
        const finish = (error, display) => {
          clearTimeout(timer);
          child.removeListener('error', failed);
          child.removeListener('exit', failed);
          child.stdio[3].removeListener('data', data);
          child.stdio[3].removeListener('error', failed);
          error ? reject(error) : resolve(display);
        };
        const failed = () => finish(Object.assign(new Error('Xvfb did not become ready'), { code: 'captcha_display_unavailable' }));
        const data = chunk => {
          output += chunk;
          if (/^\d+\n$/.test(output)) finish(null, ':' + output.trim());
          else if (output.length > 32) failed();
        };
        const timer = setTimeout(failed, 5000);
        child.once('error', failed);
        child.once('exit', failed);
        child.stdio[3].on('data', data);
        child.stdio[3].once('error', failed);
      });
      return { ...process.env, DISPLAY: this.display };
    } catch (error) {
      await this.close();
      throw error;
    }
  }

  async close() {
    const child = this.child;
    this.child = null;
    this.display = '';
    if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
    await new Promise(resolve => {
      const finish = () => { clearTimeout(timer); resolve(); };
      const timer = setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 1000);
      child.once('exit', finish);
      child.kill('SIGTERM');
    });
  }
}

function executable(file) {
  try { fs.accessSync(file, process.platform === 'win32' ? fs.constants.F_OK : fs.constants.X_OK); return fs.statSync(file).isFile(); }
  catch { return false; }
}

function playwright(config) {
  const names = ['playwright-core', 'playwright'];
  // An explicit driver path must not fall back to another installation.
  const roots = config.playwright_path ? [path.resolve(config.root, config.playwright_path)] : [
    config.driver_path, config.root, path.join(config.root, 'captcha_node'), path.dirname(process.execPath),
    ...require('node:module').globalPaths,
    process.platform === 'win32' && process.env.APPDATA ? path.join(process.env.APPDATA, 'npm/node_modules') : '',
    path.resolve(path.dirname(process.execPath), '../lib/node_modules'),
  ];
  for (const root of new Set(roots.filter(Boolean).map(root => path.resolve(config.root, root)))) {
    for (const directory of [root, ...names.flatMap(name => [path.join(root, 'node_modules', name), path.join(root, name)])]) {
      try {
        const manifest = JSON.parse(fs.readFileSync(path.join(directory, 'package.json'), 'utf8'));
        if (!names.includes(manifest.name)) continue;
        const library = require(directory);
        if (library.chromium) return { library, path: directory, version: manifest.version };
      } catch {}
    }
  }
  return null;
}

function browserPath(config, driver, headed = false) {
  const windows = process.platform === 'win32';
  const names = windows ? ['msedge.exe', 'chrome.exe', 'chromium.exe'] : ['chromium', 'chromium-browser', 'google-chrome', 'google-chrome-stable', 'microsoft-edge'];
  if (config.browser_path) {
    const configured = config.browser_path;
    const found = path.isAbsolute(configured) || /[/\\]/.test(configured) ? path.resolve(config.root, configured) : findExecutable(configured);
    return found && executable(found) && (!headed || !headlessShell(found)) ? found : '';
  }
  const candidates = names.map(findExecutable).filter(Boolean);
  if (windows) {
    for (const base of [process.env.PROGRAMFILES, process.env['PROGRAMFILES(X86)'], process.env.LOCALAPPDATA].filter(Boolean)) {
      candidates.push(path.join(base, 'Microsoft/Edge/Application/msedge.exe'), path.join(base, 'Google/Chrome/Application/chrome.exe'), path.join(base, 'Chromium/Application/chrome.exe'));
    }
  } else if (process.platform === 'darwin') {
    for (const base of ['/Applications', path.join(os.homedir(), 'Applications')]) {
      candidates.push(path.join(base, 'Google Chrome.app/Contents/MacOS/Google Chrome'), path.join(base, 'Microsoft Edge.app/Contents/MacOS/Microsoft Edge'), path.join(base, 'Chromium.app/Contents/MacOS/Chromium'));
    }
  }
  if (driver) candidates.push(driver.library.chromium.executablePath());
  const cache = windows ? path.join(process.env.LOCALAPPDATA || os.homedir(), 'ms-playwright')
    : process.platform === 'darwin' ? path.join(os.homedir(), 'Library/Caches/ms-playwright')
      : path.join(process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache'), 'ms-playwright');
  const roots = [config.browser_cache, process.env.PLAYWRIGHT_BROWSERS_PATH, cache].filter(value => value && value !== '0');
  for (const root of roots) {
    let entries;
    try { entries = fs.readdirSync(root).filter(name => /^chromium(?:_headless_shell)?-\d+$/.test(name)).sort((a, b) => Number(b.split('-').at(-1)) - Number(a.split('-').at(-1))); }
    catch { continue; }
    for (const entry of entries) {
      for (const relative of [
        'chrome-win/chrome.exe', 'chrome-win64/chrome.exe', 'chrome-win/headless_shell.exe', 'chrome-headless-shell-win64/chrome-headless-shell.exe',
        'chrome-linux/chrome', 'chrome-linux64/chrome', 'chrome-linux/headless_shell', 'chrome-headless-shell-linux64/chrome-headless-shell',
        'chrome-mac/Chromium.app/Contents/MacOS/Chromium', 'chrome-mac/headless_shell',
        'chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing',
        'chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing',
        'chrome-headless-shell-mac-arm64/chrome-headless-shell', 'chrome-headless-shell-mac-x64/chrome-headless-shell',
      ]) {
        candidates.push(path.join(root, entry, relative));
      }
    }
  }
  return candidates.find(file => executable(file) && (!headed || !headlessShell(file))) || '';
}

function browserPlan(config, nativeUnavailable = '') {
  const driver = playwright(config), display = displayStatus(config);
  const requested = config.headless_only ? 'playwright' : config.browser_backend || 'auto';
  const headless = !!config.headless_only || config.browser_headless !== false;
  const fullBrowser = browserPath(config, driver, true);
  const proxy = config.proxy_url ? new URL(config.proxy_url) : null;
  const nativeCode = nativeUnavailable || (!['win32', 'linux'].includes(process.platform) ? 'captcha_native_unsupported'
    : !fullBrowser ? 'captcha_browser_missing'
    : proxy && (!['http:', 'https:'].includes(proxy.protocol) || proxy.username || proxy.password) ? 'captcha_proxy_unsupported'
    : !display.available ? 'captcha_display_unavailable' : '');
  const backend = requested === 'native' || (requested === 'auto' && !nativeCode) ? 'native' : 'playwright';
  const file = backend === 'native' || !headless ? fullBrowser : browserPath(config, driver);
  const code = backend === 'native' ? nativeCode : !driver ? 'captcha_playwright_missing' : !file ? 'captcha_browser_missing' : !headless && !display.available ? 'captcha_display_unavailable' : '';
  return { requested, backend, headless: backend === 'playwright' && headless, driver, display, file, code, nativeCode, fallback_code: requested === 'auto' && backend === 'playwright' ? nativeCode : '' };
}

function probe(config) {
  const plan = browserPlan(config), driver = plan.driver, browser = plan.file;
  return {
    browser_ready: !plan.code,
    browser_backend: plan.backend,
    browser_headless: plan.headless,
    native_browser: { available: !plan.nativeCode, code: plan.nativeCode },
    display: plan.display,
    playwright: { available: !!driver, installed: !!driver, path: driver?.path || '', version: driver?.version || '', message: driver ? '已找到' : config.playwright_path ? '指定目录无法加载 Playwright，请检查路径和 Node.js 版本' : '未找到 Playwright，可指定已有依赖目录或安装驱动' },
    browser: { available: !!browser, installed: !!browser, path: browser, message: plan.code === 'captcha_display_unavailable' ? plan.display.message : browser ? '已找到浏览器' : config.browser_path ? '指定路径不可用，或当前通道需要完整浏览器' : '未找到适用于当前通道的 Chromium、Chrome 或 Edge' },
  };
}

const maxBrowserSessions = 16;
const browserSessionTTL = 15 * 60 * 1000;

async function closeSession(sessions, key) {
  const session = sessions.get(key);
  sessions.delete(key);
  await session?.context.close().catch(() => {});
}

async function verify(browser, sessions, config) {
  let session, timer, keepPrepared = false;
  const timeout = Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
  const deadline = Date.now() + timeout;
  const origin = new URL(config.origin).origin;
  if (!origin.startsWith('https://')) return { ok: false, code: 'captcha_origin_invalid' };
  // Go binds this page's key to credentials, purpose, scene and exit.
  const key = /^[a-f0-9]{64}$/.test(config.session_key || '') ? config.session_key : '';
  const identity = JSON.stringify([origin, config.proxy_url || '', config.scene_id, config.region, config.prefix]);
  const options = { serviceWorkers: 'block', storageState: config.state?.browserStorage };
  if (config.proxy_url) {
    const proxy = new URL(config.proxy_url);
    if (!['http:', 'https:'].includes(proxy.protocol)) return { ok: false, code: 'captcha_proxy_unsupported' };
    options.proxy = { server: proxy.origin, username: decodeURIComponent(proxy.username), password: decodeURIComponent(proxy.password) };
  }
  try {
    for (const [candidate, saved] of sessions) {
      if (saved.page.isClosed() || Date.now() - saved.lastUsed >= browserSessionTTL || (candidate === key && saved.identity !== identity)) await closeSession(sessions, candidate);
    }
    session = key ? sessions.get(key) : null;
    const reused = !!session;
    if (!session) {
      while (sessions.size >= maxBrowserSessions) await closeSession(sessions, sessions.keys().next().value);
      const context = await browser.newContext(options);
      session = { context, identity, lastUsed: Date.now(), resourceStatus: 0, retryAfter: '', networkFailed: false };
      if (key) sessions.set(key, session);
      // Only Go may submit claims and consume tickets; browser pages handle verification.
      await context.route(/\/api\/v1\/zcode-plan\/billing\/claim(?:[?#]|$)/, route => route.abort('blockedbyclient'));
      session.page = await context.newPage();
      session.page.on('response', response => {
        const host = new URL(response.url()).hostname;
        if (!/(^|\.)(alicdn\.com|aliyuncs\.com|aliyun\.com)$/.test(host)) return;
        if (response.status() >= 400) { session.resourceStatus = response.status(); session.retryAfter = response.headers()['retry-after'] || ''; }
      });
      session.page.on('requestfailed', request => {
        const host = new URL(request.url()).hostname;
        if (/(^|\.)(alicdn\.com|aliyuncs\.com|aliyun\.com)$/.test(host)) session.networkFailed = true;
      });
    }
    const { context, page } = session;
    session.resourceStatus = 0; session.retryAfter = ''; session.networkFailed = false;
    session.lastUsed = Date.now();
    if (key) { sessions.delete(key); sessions.set(key, session); }
    context.setDefaultTimeout(timeout);
    const cookies = String(config.cookies || '').split(';').map(value => {
      const split = value.indexOf('=');
      return split > 0 ? { name: value.slice(0, split).trim(), value: value.slice(split + 1).trim(), url: origin + '/', secure: true } : null;
    }).filter(Boolean);
    if (cookies.length && session.cookies !== config.cookies) await context.addCookies(cookies);
    session.cookies = config.cookies;
    if (!reused) {
      const response = await page.goto(origin + '/', { waitUntil: 'domcontentloaded', timeout: Math.max(1, deadline - Date.now()) });
      if (!response?.ok()) {
        const status = response?.status() || 0;
        const retryAfter = response?.headers()['retry-after'] || '';
        await closeSession(sessions, key);
        return { ok: false, code: 'captcha_resource_http_error', stage: 'browser_page', status, retry_after: retryAfter };
      }
    }
    if (new URL(page.url()).origin !== origin) {
      await closeSession(sessions, key);
      return { ok: false, code: 'captcha_origin_invalid', stage: 'browser_page' };
    }
    const { captchaExpression } = require('./captcha.cjs');
    const result = await Promise.race([
      page.evaluate(captchaExpression(config, 7000)),
      new Promise(resolve => { timer = setTimeout(() => resolve({ ok: false, code: 'captcha_timeout' }), Math.max(1, deadline - Date.now())); }),
    ]);
    keepPrepared = config.action === 'prepare' && result.ok;
    const progress = { ...await page.evaluate(() => ({ ...window.__captchaProgress, browser_visibility: document.visibilityState, browser_webdriver: navigator.webdriver, browser_origin: location.origin })).catch(() => ({})), runtime: 'browser', browser_version: browser.version(), session_reused: reused };
    const storage = await context.storageState();
    const state = Buffer.byteLength(JSON.stringify(storage)) <= (16 << 10) ? { browserStorage: storage } : undefined;
    const { resourceStatus, retryAfter, networkFailed } = session;
    // Cancel late callbacks before another request can initialize an SDK on this page.
    if (result.code === 'captcha_timeout') await closeSession(sessions, key);
    if (!result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      if (resourceStatus) return { ...progress, ok: false, code: resourceStatus === 407 ? 'proxy_auth_failed' : 'captcha_resource_http_error', status: resourceStatus, retry_after: retryAfter, state };
      if (networkFailed) return { ...progress, ok: false, code: 'captcha_network_error', state };
    }
    return { ...progress, ...result, state };
  } catch (error) {
    await closeSession(sessions, key);
    return { ok: false, code: session?.resourceStatus === 407 ? 'proxy_auth_failed' : session?.networkFailed ? 'captcha_network_error' : error?.name === 'TimeoutError' ? 'captcha_timeout' : 'captcha_runtime_error', stage: 'browser_context', runtime: 'browser' };
  } finally {
    clearTimeout(timer);
    // Keep prepared instances; dispose finished ones so each card gets a fresh ticket.
    if (!keepPrepared) await session?.page?.evaluate(() => window.__disposeCaptcha?.()).catch(() => {});
    if (!key) await session?.context.close().catch(() => {});
  }
}

async function worker() {
  let browser, native, nativeUnavailable = '', stopping = false, chain = Promise.resolve();
  const display = new BrowserDisplay();
  const sessions = new Map();
  const shutdown = async () => {
    if (stopping) return;
    stopping = true;
    sessions.clear();
    await native?.shutdown().catch(() => {});
    await browser?.close().catch(() => {});
    await display.close();
  };
  process.once('SIGINT', shutdown);
  process.once('SIGTERM', shutdown);
  const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
  input.on('close', shutdown);
  input.on('line', line => {
    chain = chain.then(async () => {
      if (stopping) return;
      let result, plan;
      try {
        const config = JSON.parse(line);
        plan = browserPlan(config, nativeUnavailable);
        if (plan.code) result = { ok: false, code: plan.code, stage: 'browser_selection' };
        if (!result && plan.backend === 'native') {
          await browser?.close().catch(() => {});
          browser = null;
          sessions.clear();
          if (!native) {
            native = new (require('./captcha_native.cjs').NativeClaimBrowser)(plan.file);
          }
          try {
            result = await native.run({ ...config, browser_env: await display.environment(config) });
          } catch (error) {
            result = browserLaunchFailure(error);
          }
          // Never switch runtimes after an SDK refusal, timeout or interaction request.
          if (!stopping && !result.ok && ['captcha_browser_launch_failed', 'captcha_display_unavailable'].includes(result.code) && !result.sdk_executed && plan.requested === 'auto') {
            nativeUnavailable = result.code;
            await native.close();
            result = null;
            plan = browserPlan(config, nativeUnavailable);
            if (plan.code) result = { ok: false, code: plan.code, stage: 'browser_selection' };
          }
        }
        if (!stopping && !result && plan.backend === 'playwright') {
          await native?.close();
          if (stopping) return;
          if (!browser?.isConnected()) {
            sessions.clear();
            const env = plan.headless ? process.env : await display.environment(config);
            if (stopping) return;
            browser = await plan.driver.library.chromium.launch({ executablePath: plan.file, headless: plan.headless, env, timeout: 20000 });
            if (stopping) { await browser.close(); return; }
          }
          result = config.action === 'warm' ? { ok: true } : await verify(browser, sessions, config);
        }
      } catch (error) {
        result = browserLaunchFailure(error);
      }
      if (!stopping) process.stdout.write(JSON.stringify({ ...result, runtime: plan?.backend === 'native' ? 'native_browser' : 'browser', browser_backend: plan?.backend, browser_headless: plan?.headless, browser_path: plan?.file, browser_display: plan?.headless ? undefined : display.display || plan?.display.display || undefined, fallback_code: plan?.fallback_code || undefined }) + '\n');
    }).catch(shutdown);
  });
}

if (require.main === module) void worker();
module.exports = { probe };
