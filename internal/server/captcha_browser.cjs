'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const readline = require('node:readline');

function executable(file) {
  try { fs.accessSync(file, process.platform === 'win32' ? fs.constants.F_OK : fs.constants.X_OK); return fs.statSync(file).isFile(); }
  catch { return false; }
}

function playwright(config) {
  const names = ['playwright-core', 'playwright'];
  // An explicit location is authoritative. Never silently replace a broken
  // configured driver with a different version from another installation.
  const roots = config.playwright_path ? [path.resolve(config.root, config.playwright_path)] : [
    config.driver_path, config.root, path.join(config.root, 'captcha_node'), path.dirname(process.execPath),
    ...require('node:module').globalPaths,
    process.platform === 'win32' && process.env.APPDATA ? path.join(process.env.APPDATA, 'npm/node_modules') : '',
    path.resolve(path.dirname(process.execPath), '../lib/node_modules'),
  ];
  for (const root of new Set(roots.filter(Boolean).map(root => path.resolve(config.root, root)))) {
    // Accept a package directory, node_modules directory, or its project root.
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

function browserPath(config, driver) {
  const windows = process.platform === 'win32';
  const names = windows ? ['msedge.exe', 'chrome.exe', 'chromium.exe'] : ['chromium', 'chromium-browser', 'google-chrome', 'google-chrome-stable', 'microsoft-edge'];
  const search = name => (process.env.PATH || '').split(path.delimiter).map(dir => path.join(dir, name)).find(executable);
  if (config.browser_path) {
    const configured = config.browser_path;
    const found = path.isAbsolute(configured) || /[/\\]/.test(configured) ? path.resolve(config.root, configured) : search(configured);
    return found && executable(found) ? found : '';
  }
  const candidates = names.map(search).filter(Boolean);
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
  return candidates.find(executable) || '';
}

function probe(config) {
  const driver = playwright(config), browser = browserPath(config, driver);
  return {
    playwright: { available: !!driver, installed: !!driver, path: driver?.path || '', version: driver?.version || '', message: driver ? '已找到' : config.playwright_path ? '指定目录无法加载 Playwright，请检查路径和 Node.js 版本' : '未找到 Playwright，可指定已有依赖目录或安装驱动' },
    browser: { available: !!browser, installed: !!browser, path: browser, message: browser ? '已找到浏览器' : config.browser_path ? '指定的浏览器路径不存在或不可执行' : '未找到 Chromium、Chrome 或 Edge' },
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
  // This key is supplied by Go and binds credentials, purpose, scene and exit.
  // Never share a page or SDK instance between different accounts or routes.
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
      // Browser pages only prepare verification. The Go claim path remains the
      // sole owner of the billing mutation and its one-use ticket handling.
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
      page.evaluate(captchaExpression(config)),
      new Promise(resolve => { timer = setTimeout(() => resolve({ ok: false, code: 'captcha_timeout' }), Math.max(1, deadline - Date.now())); }),
    ]);
    keepPrepared = config.action === 'prepare' && result.ok;
    const progress = { ...await page.evaluate(() => window.__captchaProgress || {}).catch(() => ({})), runtime: 'browser', browser_version: browser.version(), session_reused: reused };
    const storage = await context.storageState();
    const state = Buffer.byteLength(JSON.stringify(storage)) <= (16 << 10) ? { browserStorage: storage } : undefined;
    const { resourceStatus, retryAfter, networkFailed } = session;
    // Abort a timed-out initialization as well as its late callbacks before
    // another request can initialize a second SDK instance on the same page.
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
    // Prepared instances stay alive. Finished instances are disposed so a
    // second card always gets a fresh ticket while retaining the same page.
    if (!keepPrepared) await session?.page?.evaluate(() => window.__disposeCaptcha?.()).catch(() => {});
    if (!key) await session?.context.close().catch(() => {});
  }
}

async function worker() {
  let browser, native, stopping = false, chain = Promise.resolve();
  const sessions = new Map();
  const shutdown = async () => {
    if (stopping) return;
    stopping = true;
    sessions.clear();
    await native?.close();
    await browser?.close().catch(() => {});
  };
  process.once('SIGINT', shutdown);
  process.once('SIGTERM', shutdown);
  const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
  input.on('close', shutdown);
  input.on('line', line => {
    chain = chain.then(async () => {
      if (stopping) return;
      let result;
      try {
        const config = JSON.parse(line);
        const proxy = config.proxy_url ? new URL(config.proxy_url) : null;
        if (process.platform === 'win32' && !proxy?.username && !proxy?.password) {
          if (!native) {
            const file = browserPath(config, playwright(config));
            if (!file) result = { ok: false, code: 'captcha_browser_missing' };
            else native = new (require('./captcha_native.cjs').NativeClaimBrowser)(file);
          }
          result ||= await native.run(config);
          if (!stopping) process.stdout.write(JSON.stringify(result) + '\n');
          return;
        }
        if (!browser?.isConnected()) {
          sessions.clear();
          const driver = playwright(config);
          const file = browserPath(config, driver);
          if (!driver) result = { ok: false, code: 'captcha_playwright_missing' };
          else if (!file) result = { ok: false, code: 'captcha_browser_missing' };
          else {
            browser = await driver.library.chromium.launch({ executablePath: file, headless: true, timeout: 20000 });
            if (stopping) { await browser.close(); return; }
          }
        }
        result ||= config.action === 'warm' ? { ok: true } : await verify(browser, sessions, config);
      } catch { result = { ok: false, code: 'captcha_browser_launch_failed', stage: 'browser_launch' }; }
      process.stdout.write(JSON.stringify(result) + '\n');
    }).catch(shutdown);
  });
}

if (require.main === module) void worker();
module.exports = { probe };
