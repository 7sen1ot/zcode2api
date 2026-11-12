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

async function verify(browser, config) {
  let context, page, timer, resourceStatus = 0, retryAfter = '', networkFailed = false;
  const timeout = Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
  const origin = new URL(config.origin).origin;
  if (!origin.startsWith('https://')) return { ok: false, code: 'captcha_origin_invalid' };
  const options = { serviceWorkers: 'block', storageState: config.state?.browserStorage };
  if (config.proxy_url) {
    const proxy = new URL(config.proxy_url);
    if (!['http:', 'https:'].includes(proxy.protocol)) return { ok: false, code: 'captcha_proxy_unsupported' };
    options.proxy = { server: proxy.origin, username: decodeURIComponent(proxy.username), password: decodeURIComponent(proxy.password) };
  }
  try {
    context = await browser.newContext(options);
    context.setDefaultTimeout(timeout);
    const cookies = String(config.cookies || '').split(';').map(value => {
      const split = value.indexOf('=');
      return split > 0 ? { name: value.slice(0, split).trim(), value: value.slice(split + 1).trim(), url: origin + '/', secure: true } : null;
    }).filter(Boolean);
    if (cookies.length) await context.addCookies(cookies);
    page = await context.newPage();
    // Give the SDK the same upstream origin as jsdom without loading the app.
    await page.route(origin + '/', route => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><html><head></head><body></body></html>' }));
    page.on('response', response => {
      if (response.status() >= 400) { resourceStatus = response.status(); retryAfter = response.headers()['retry-after'] || ''; }
    });
    page.on('requestfailed', () => { networkFailed = true; });
    await page.goto(origin + '/', { waitUntil: 'domcontentloaded' });
    const { captchaExpression } = require('./captcha.cjs');
    const result = await Promise.race([
      page.evaluate(captchaExpression(config)),
      new Promise(resolve => { timer = setTimeout(() => resolve({ ok: false, code: 'captcha_timeout' }), timeout); }),
    ]);
    const progress = await page.evaluate(() => window.__captchaProgress || {}).catch(() => ({}));
    const storage = await context.storageState();
    const state = Buffer.byteLength(JSON.stringify(storage)) <= (16 << 10) ? { browserStorage: storage } : undefined;
    if (!result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      if (resourceStatus) return { ...progress, ok: false, code: resourceStatus === 407 ? 'proxy_auth_failed' : 'captcha_resource_http_error', status: resourceStatus, retry_after: retryAfter, state };
      if (networkFailed) return { ...progress, ok: false, code: 'captcha_network_error', state };
    }
    return { ...progress, ...result, state };
  } catch {
    return { ok: false, code: resourceStatus === 407 ? 'proxy_auth_failed' : networkFailed ? 'captcha_network_error' : 'captcha_runtime_error', stage: 'browser_context' };
  } finally {
    clearTimeout(timer);
    await context?.close().catch(() => {});
  }
}

async function worker() {
  let browser, stopping = false, chain = Promise.resolve();
  const shutdown = async () => {
    if (stopping) return;
    stopping = true;
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
        if (!browser?.isConnected()) {
          const driver = playwright(config);
          const file = browserPath(config, driver);
          if (!driver) result = { ok: false, code: 'captcha_playwright_missing' };
          else if (!file) result = { ok: false, code: 'captcha_browser_missing' };
          else {
            browser = await driver.library.chromium.launch({ executablePath: file, headless: true, timeout: 20000 });
            if (stopping) { await browser.close(); return; }
          }
        }
        result ||= config.action === 'warm' ? { ok: true } : await verify(browser, config);
      } catch { result = { ok: false, code: 'captcha_browser_launch_failed', stage: 'browser_launch' }; }
      process.stdout.write(JSON.stringify(result) + '\n');
    }).catch(shutdown);
  });
}

if (require.main === module) void worker();
module.exports = { probe };
