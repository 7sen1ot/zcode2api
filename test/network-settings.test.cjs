'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const library = require('../captcha_node/node_modules/jsdom');
const { run, captchaOutcome, installCanvasWebGL } = require('../internal/server/captcha.cjs');

function sdkLibrary(source, inspect = () => {}) {
  class Resources extends library.ResourceLoader {
    fetch(url) {
      assert.equal(url, 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js');
      return Promise.resolve(Buffer.from(source));
    }
  }
  class DOM extends library.JSDOM {
    constructor(...args) { super(...args); inspect(this.window); }
  }
  return { ...library, JSDOM: DOM, ResourceLoader: Resources };
}

const sdkConfig = { origin: 'https://zcode.example', scene_id: 'test-scene', region: 'cn', prefix: 'test-prefix', simulate_canvas_webgl: true, timeout_ms: 5000 };

test('SDK initializes its actual jsdom realm and obtains a callback ticket', async () => {
  let window;
  const source = `window.initAliyunCaptcha = options => {
    if (options.SceneId !== 'test-scene' || window.AliyunCaptchaConfig.region !== 'cn' || window.AliyunCaptchaConfig.prefix !== 'test-prefix') throw new Error('SDK config missing');
    if (!document.querySelector(options.element) || !document.querySelector(options.button)) throw new Error('SDK elements missing');
    window.__started = 0;
    options.getInstance({ startTracelessVerification() { window.__started++; options.success('offline-test-ticket'); } });
  };`;
  const initialListeners = process.listenerCount('SIGTERM');
  const result = await run(sdkConfig, { loadJSDOM: () => sdkLibrary(source, realm => { window = realm; }), delayMs: 0, timeoutMs: 1000 });
  assert.deepEqual(JSON.parse(JSON.stringify(result)), { ok: true, param: 'offline-test-ticket', region: 'cn' });
  assert.equal(window.__started, 1);
  assert.equal(typeof window.OffscreenCanvas, 'function');
  assert.equal(process.listenerCount('SIGTERM'), initialListeners);
});

test('Canvas and WebGL provide explicit synthetic information and stable contexts', () => {
  const dom = new library.JSDOM('', { beforeParse: installCanvasWebGL });
  try {
    const canvas = dom.window.document.createElement('canvas');
    const two = canvas.getContext('2d');
    assert.equal(canvas.getContext('2d'), two);
    two.fillText('test', 0, 0);
    assert.equal(two.measureText('test').width, 32);
    assert.equal(two.getImageData(0, 0, 2, 2).data.length, 16);
    assert.match(canvas.toDataURL(), /^data:image\/png;base64,/);
    const gl = canvas.getContext('webgl');
    assert.equal(gl.getParameter(37445), 'Intel');
    assert.equal(gl.getParameter(37446), 'Intel Graphics');
    const shader = gl.createShader(gl.VERTEX_SHADER);
    gl.shaderSource(shader, 'void main() {}'); gl.compileShader(shader);
    assert.equal(gl.getShaderParameter(shader, gl.COMPILE_STATUS), true);
    assert.equal(canvas.getContext('experimental-webgl'), gl);
    assert.equal(new dom.window.OffscreenCanvas(2, 2).getContext('2d').canvas.width, 2);
  } finally { dom.window.close(); }
});

test('simulation can be disabled and SDK risk rejection remains a failure', async () => {
  let window;
  const source = `window.initAliyunCaptcha = options => options.getInstance({ startTracelessVerification() { options.fail({ Result: { VerifyCode: 'F001', VerifyResult: false } }); } });`;
  const result = await run({ ...sdkConfig, simulate_canvas_webgl: false }, { loadJSDOM: () => sdkLibrary(source, realm => { window = realm; }), delayMs: 0, timeoutMs: 1000 });
  assert.equal(typeof window.OffscreenCanvas, 'undefined');
  assert.equal(result.ok, false);
  assert.equal(result.verify_code, 'F001');
  assert.equal(result.param, undefined);
  assert.equal(captchaOutcome({ Result: { VerifyCode: 'T006' } }, 'cn').ok, false);
});

test('SDK timeout and missing dependencies report errors without leaving signal listeners', async () => {
  const signals = ['SIGINT', 'SIGTERM'].map(name => process.listenerCount(name));
  const stalled = `window.initAliyunCaptcha = options => options.getInstance({ startTracelessVerification() {} });`;
  const result = await run(sdkConfig, { loadJSDOM: () => sdkLibrary(stalled), delayMs: 0, timeoutMs: 30 });
  assert.equal(result.code, 'captcha_timeout');
  assert.deepEqual(['SIGINT', 'SIGTERM'].map(name => process.listenerCount(name)), signals);
  const missing = await run(sdkConfig, { loadJSDOM() { throw new Error('not installed'); } });
  assert.equal(missing.code, 'captcha_jsdom_missing');
});

test('SDK distinguishes a resource connection failure from initialization errors', async () => {
  class BrokenResources extends library.ResourceLoader {
    fetch() { return Promise.reject(new Error('offline resource connection failed')); }
  }
  const network = await run(sdkConfig, { loadJSDOM: () => ({ ...library, ResourceLoader: BrokenResources }), delayMs: 0, timeoutMs: 1000 });
  assert.equal(network.code, 'captcha_network_error');
  const initialization = await run(sdkConfig, { loadJSDOM: () => sdkLibrary('window.initAliyunCaptcha = () => { throw new Error("invalid initialization"); };'), delayMs: 0, timeoutMs: 1000 });
  assert.equal(initialization.code, 'captcha_sdk_error');
});

function settingsFixture() {
  return {
    poll_interval_sec: 60, poll_jitter_sec: 10, poll_concurrency: 4, claim_auto_enabled: true, claim_auto_min: 240,
    default_reasoning_level: 'low', app_version: '3.16.2', aliases: {}, retry: {}, web_port: 6789, gateway_port: 6780,
    http: { client_version: '0.17.0', first_token_timeout_ms: 60000, idle_timeout_ms: 120000, total_timeout_ms: 0, captcha: { mode: 'off', node_path: 'node', jsdom_path: '', simulate_canvas_webgl: true, timeout_ms: 30000 } },
    outbound_proxy: { enabled: false, rotation_enabled: false, rotation_interval_sec: 0, source: 'manual', urls: [], subscription_url: '', subscription_filter: '', mihomo_path: 'mihomo', refresh_interval_sec: 3600 },
  };
}

async function settingsPage(draft, installError = '', overrides = {}) {
  const dom = new library.JSDOM('<main></main>', { url: 'http://localhost/#/settings', runScripts: 'outside-only' });
  const { window } = dom;
  const calls = [], mihomoChecks = [], runtimeChecks = [];
  let installedMihomoPath = '';
  Object.assign(window, {
    $: (selector, root = window.document) => root.querySelector(selector),
    esc: value => String(value ?? '').replace(/[&<>"']/g, x => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[x]),
    pagehead: (title, desc, actions = '') => `<div class="pagehead"><div><h1>${title}</h1><p>${desc}</p></div><div class="acts">${actions}</div></div>`, RUNTIME: null, settingsDirty: false, settingsSaving: false,
    syncSelect() {}, toast() {}, btnLoading() {}, confirmAction: async () => true,
    api: async (url, options = {}) => {
      if (url === '/api/settings' && !options.method) return { ...settingsFixture(), ...overrides.settings };
      if (url.startsWith('/api/settings/captcha/runtime')) {
        const query = new URL(url, 'http://localhost').searchParams;
        const paths = { node_path: query.get('node_path'), jsdom_path: query.get('jsdom_path') };
        runtimeChecks.push(paths);
        if (overrides.runtimeStatus) return await overrides.runtimeStatus(paths);
        return { ok: true, ready: true, node: { installed: true, available: true, version: '24.18.0', path: 'C:/runtime/node.exe' }, jsdom: { installed: true, available: true, version: '26.1.0', path: 'C:/runtime/captcha_node/node_modules/jsdom' } };
      }
      if (url === '/api/settings/proxy/mihomo') return { installed: false, supported: true, version: 'v1.19.33', platform: 'windows/amd64', ...overrides.mihomo };
      if (url.startsWith('/api/settings/proxy/mihomo?')) {
        const path = new URL(url, 'http://localhost').searchParams.get('path');
        mihomoChecks.push(path);
        if (overrides.mihomoGates?.[path]) await overrides.mihomoGates[path];
        if (overrides.mihomoError) throw new Error(overrides.mihomoError);
        return { installed: path === installedMihomoPath, path: path === installedMihomoPath ? path : '', supported: true, version: 'v1.19.33', platform: 'windows/amd64', ...overrides.mihomo, ...overrides.mihomoPaths?.[path] };
      }
      calls.push({ url, ...JSON.parse(JSON.stringify(options)) });
      if (url === '/api/settings' && options.method === 'PUT') {
        if (overrides.saveError) throw new Error(overrides.saveError);
        if (overrides.saveGate) await overrides.saveGate;
      }
      if (url.endsWith('/mihomo/install')) {
        if (installError) throw new Error(installError);
        installedMihomoPath = 'C:/data/tools/mihomo.exe';
        return { ok: true, path: 'C:/data/tools/mihomo.exe', version: 'v1.19.33' };
      }
      if (url.endsWith('/proxy/test')) return { ok: true, note: '检测不保存设置', results: overrides.proxyResults || [{ name: '<img src=x onerror=alert(1)>', reachable: true, status: 401, message: '出口可达', elapsed_ms: 12 }] };
      if (url.endsWith('/proxy/nodes')) {
        if (overrides.nodesGate) await overrides.nodesGate;
        if (overrides.nodesError) throw new Error(overrides.nodesError);
        return { ok: true, nodes: overrides.nodes || [{ id: 'a'.repeat(64), name: '香港 01', protocol: 'VLESS', current: true }, { id: 'b'.repeat(64), name: '<img src=x onerror=alert(1)>', protocol: 'HTTP' }] };
      }
      return { ok: true };
    },
  });
  if (draft) window.sessionStorage.setItem('z2a_settings_draft', JSON.stringify(draft));
  const source = fs.readFileSync(path.join(__dirname, '../src/web/app.js'), 'utf8');
  const start = source.indexOf('async function pageSettings(main)');
  const end = source.indexOf('\nasync function pageLogs(', start);
  assert.ok(start >= 0 && end > start);
  window.eval(source.slice(start, end));
  await window.pageSettings(window.document.querySelector('main'));
  const change = (id, value) => {
    const input = window.document.getElementById(id);
    if (input.type === 'checkbox') input.checked = value; else input.value = value;
    input.dispatchEvent(new window.Event('input', { bubbles: true }));
    input.dispatchEvent(new window.Event('change', { bubbles: true }));
  };
  const settle = () => new Promise(resolve => setImmediate(resolve));
  const submit = async () => { window.document.querySelector('#s-save').click(); await settle(); };
  return { dom, window, calls, mihomoChecks, runtimeChecks, change, submit, settle };
}

test('default reasoning loads the saved level and persists edits without requiring advanced settings', async () => {
  const page = await settingsPage(undefined, '', { settings: { default_reasoning_level: 'high' } });
  try {
    const select = page.window.document.querySelector('#s-level');
    assert.equal(select.value, 'high');
    assert.deepEqual(Array.from(select.options, option => option.value), ['low', 'high', 'max']);
    assert.equal(select.closest('details').id, 'gateway-panel');
    assert.equal(page.window.settingsDirty, false);
    page.change('s-level', 'max');
    assert.equal(page.window.settingsDirty, true);
    assert.equal(JSON.parse(page.window.sessionStorage.getItem('z2a_settings_draft'))['s-level'], 'max');
    const panels = page.window.document.querySelectorAll('#settings-form > details');
    assert.equal(panels.length, 4);
    assert.equal(page.window.document.querySelectorAll('#settings-form details details').length, 0);
    for (const panel of panels) {
      assert.equal(panel.open, false);
      panel.querySelector('summary').click(); assert.equal(panel.open, true);
      panel.querySelector('summary').click(); assert.equal(panel.open, false);
    }
    await page.submit();
    assert.equal(page.calls.find(call => call.method === 'PUT').body.default_reasoning_level, 'max');
    assert.equal(page.window.settingsDirty, false);
    assert.equal(page.window.sessionStorage.getItem('z2a_settings_draft'), null);
  } finally { page.dom.window.close(); }
});

test('validation opens the containing settings panel for JSON and field errors', async () => {
  const page = await settingsPage();
  try {
    const document = page.window.document, gateway = document.querySelector('#gateway-panel'), captcha = document.querySelector('#captcha-panel');
    page.change('s-aliases', '{invalid');
    gateway.open = false;
    await page.submit();
    assert.equal(gateway.open, true);
    assert.equal(document.activeElement.id, 's-aliases');
    assert.equal(page.calls.filter(call => call.method === 'PUT').length, 0);
    page.change('s-aliases', '{}');
    page.change('s-captcha-timeout', '100');
    captcha.open = false;
    await page.submit();
    assert.equal(captcha.open, true);
    assert.equal(page.calls.filter(call => call.method === 'PUT').length, 0);
  } finally { page.dom.window.close(); }
});

test('default reasoning restores drafts and preserves the saved level when saving other fields', async () => {
  const page = await settingsPage({ 's-level': 'high' });
  try {
    assert.equal(page.window.document.querySelector('#s-level').value, 'high');
    await page.submit();
    assert.equal(page.calls.find(call => call.method === 'PUT').body.default_reasoning_level, 'high');
  } finally { page.dom.window.close(); }
  const unchanged = await settingsPage(undefined, '', { settings: { default_reasoning_level: 'max' } });
  try {
    unchanged.change('s-poll', '120');
    await unchanged.submit();
    assert.equal(unchanged.calls.find(call => call.method === 'PUT').body.default_reasoning_level, 'max');
  } finally { unchanged.dom.window.close(); }
});

test('settings form saves proxy switches and captcha controls in their real API fields', async () => {
  const page = await settingsPage();
  try {
    page.change('s-proxy-enabled', true);
    page.change('s-proxy-urls', 'http://127.0.0.1:7890\nhttp://127.0.0.1:7891\nhttp://127.0.0.1:7890');
    page.change('s-captcha-mode', 'auto');
    page.change('s-captcha-canvas', false);
    await page.submit();
    const saved = page.calls.find(x => x.method === 'PUT').body;
    assert.equal(saved.outbound_proxy.enabled, true);
    assert.equal(saved.outbound_proxy.rotation_enabled, undefined);
    assert.equal(saved.outbound_proxy.rotation_interval_sec, undefined);
    assert.deepEqual(saved.outbound_proxy.urls, ['http://127.0.0.1:7890', 'http://127.0.0.1:7891']);
    assert.equal(saved.http.captcha.mode, 'auto');
    assert.equal(saved.http.captcha.simulate_canvas_webgl, false);
    assert.equal(saved.http.client_version, '0.17.0');
    assert.equal(page.window.document.querySelector('#s-save').disabled, true);
  } finally { page.dom.window.close(); }
});

test('subscription form detects a draft without saving or caching its credentials', async () => {
  const page = await settingsPage();
  try {
    page.change('s-proxy-enabled', true);
    page.change('s-proxy-source', 'subscription');
    page.change('s-proxy-subscription', 'https://example.test/sub?token=browser-test-secret');
    assert.equal(page.window.document.querySelector('#proxy-manual-fields').hidden, true);
    assert.equal(page.window.document.querySelector('#proxy-subscription-fields').hidden, false);
    assert.equal(page.window.document.querySelector('#s-proxy-subscription').type, 'text');
    assert.equal(page.window.document.querySelector('#s-proxy-filter'), null);
    assert.equal(page.window.document.querySelector('#s-proxy-reveal'), null);
    assert.doesNotMatch(page.window.sessionStorage.getItem('z2a_settings_draft'), /browser-test-secret/);
    page.window.document.querySelector('#s-proxy-test').click();
    await page.settle();
    assert.equal(page.calls.length, 1);
    assert.equal(page.calls[0].url, '/api/settings/proxy/test');
    assert.equal(page.calls[0].body.outbound_proxy.subscription_filter, '');
    const results = page.window.document.querySelector('#s-proxy-result');
    assert.match(results.textContent, /出口可达/);
    assert.equal(results.querySelector('details').open, false);
    assert.match(results.querySelector('summary').textContent, /1 个节点 · 可达 1 · 失败 0/);
    results.querySelector('summary').click();
    assert.equal(results.querySelector('details').open, true);
    assert.doesNotMatch(results.textContent, /检测不保存设置/);
    assert.equal(results.querySelector('img'), null);
    assert.equal(page.window.settingsDirty, true);
  } finally { page.dom.window.close(); }
});

test('old captcha JSON drafts migrate to dedicated controls and invalid timeouts do not save', async () => {
  const page = await settingsPage({ 's-http': JSON.stringify({ first_token_timeout_ms: 60000, captcha: { mode: 'always', simulate_canvas_webgl: false } }) });
  try {
    assert.equal(page.window.document.querySelector('#s-captcha-mode').value, 'always');
    assert.equal(page.window.document.querySelector('#s-captcha-canvas').checked, false);
    assert.equal(JSON.parse(page.window.document.querySelector('#s-http').value).captcha, undefined);
    page.change('s-captcha-timeout', '100');
    await page.submit();
    assert.equal(page.calls.length, 0);
  } finally { page.dom.window.close(); }
});

test('proxy form removes redundant descriptions and legacy rotation controls', async () => {
  const page = await settingsPage(undefined, '', { settings: { outbound_proxy: { enabled: true, rotation_enabled: true, rotation_interval_sec: 45, source: 'manual', urls: ['http://127.0.0.1:7890'] } } });
  try {
    assert.equal(page.window.document.querySelector('#s-proxy-interval'), null);
    assert.equal(page.window.document.querySelector('#s-proxy-rotation'), null);
    assert.doesNotMatch(page.window.document.body.textContent, /持续使用当前节点|代理地址和订阅链接不会写入浏览器草稿|不保存设置，不发送账号令牌，不消耗模型额度|下载后校验|按需刷新，失败时继续使用仍在运行的旧节点|Node.js 与 jsdom 路径/);
    page.change('s-poll', '120');
    await page.submit();
    assert.equal(page.calls.find(call => call.method === 'PUT').body.outbound_proxy.rotation_interval_sec, undefined);
  } finally { page.dom.window.close(); }
});

test('proxy panels collapse without losing edits and named node selection is saved', async () => {
  const page = await settingsPage();
  try {
    const document = page.window.document, panel = document.querySelector('#proxy-panel');
    assert.equal(panel.open, false);
    panel.querySelector('summary').click();
    assert.equal(panel.open, true);
    assert.equal(page.window.settingsDirty, false);
    page.change('s-proxy-enabled', true);
    page.change('s-proxy-urls', 'http://one:80\nhttp://two:80');
    document.querySelector('#s-proxy-load').click();
    await page.settle();
    assert.match(document.querySelector('#s-proxy-node-count').textContent, /2 个节点/);
    assert.equal(document.querySelector('#s-proxy-nodes img'), null);
    document.querySelector('input[name="proxy-node"][value="' + 'b'.repeat(64) + '"]').click();
    assert.equal(document.querySelector('#s-proxy-node').value, 'b'.repeat(64));
    const nodesBox = document.querySelector('#proxy-node-box');
    assert.equal(nodesBox.querySelector('summary'), null);
    panel.querySelector('summary').click();
    assert.equal(panel.open, false);
    await page.submit();
    const saved = page.calls.find(call => call.method === 'PUT').body.outbound_proxy;
    assert.equal(saved.preferred_node, 'b'.repeat(64));
    assert.equal(saved.subscription_filter, '');
    const source = document.querySelector('#s-proxy-source');
    source.value = 'subscription';
    source.dispatchEvent(new page.window.Event('change', { bubbles: true }));
    assert.equal(document.querySelector('#s-proxy-node').value, '');
    assert.equal(document.querySelectorAll('#s-proxy-nodes input').length, 1);
  } finally { page.dom.window.close(); }
});

test('saved preferred node loads and failed nodes cannot be selected', async () => {
  const page = await settingsPage(undefined, '', {
    settings: { outbound_proxy: { enabled: true, source: 'manual', urls: ['http://one:80', 'http://two:80'], preferred_node: 'b'.repeat(64) } },
    nodes: [{ id: 'a'.repeat(64), name: '失效节点', unavailable: true }, { id: 'b'.repeat(64), name: '指定节点', current: true }],
  });
  try {
    await page.settle();
    const document = page.window.document;
    assert.equal(document.querySelector('input[name="proxy-node"][value="' + 'b'.repeat(64) + '"]').checked, true);
    assert.equal(document.querySelector('input[name="proxy-node"][value="' + 'a'.repeat(64) + '"]').disabled, true);
    assert.match(document.querySelector('#s-proxy-nodes').textContent, /使用中/);
    assert.match(document.querySelector('#s-proxy-nodes').textContent, /已剔除/);
    page.change('s-poll', '120');
    await page.submit();
    assert.equal(page.calls.find(call => call.method === 'PUT').body.outbound_proxy.preferred_node, 'b'.repeat(64));
  } finally { page.dom.window.close(); }
});

test('editing a proxy source clears its node selection and rejects stale node results', async () => {
  let finish;
  const nodesGate = new Promise(resolve => { finish = resolve; });
  const page = await settingsPage(undefined, '', { nodesGate });
  try {
    page.change('s-proxy-urls', 'http://one:80');
    page.window.document.querySelector('#s-proxy-load').click();
    page.change('s-proxy-urls', 'http://different:80');
    finish();
    await page.settle();
    const document = page.window.document;
    assert.equal(document.querySelectorAll('#s-proxy-nodes input').length, 1);
    assert.equal(document.querySelector('#s-proxy-node').value, '');
    assert.match(document.querySelector('#s-proxy-node-status').textContent, /配置已修改/);
    assert.equal(document.querySelector('#s-proxy-load').disabled, false);
  } finally { page.dom.window.close(); }
});

test('Mihomo installs only on click and fills its path without automatically saving settings', async () => {
  const page = await settingsPage();
  try {
    assert.equal(page.calls.length, 0);
    page.change('s-proxy-source', 'subscription');
    await page.settle();
    assert.equal(page.calls.length, 0);
    const button = page.window.document.querySelector('#s-mihomo-install');
    button.click();
    assert.equal(button.disabled, true);
    await page.settle();
    assert.equal(page.calls.length, 1);
    assert.equal(page.calls[0].url, '/api/settings/proxy/mihomo/install');
    assert.equal(page.window.document.querySelector('#s-proxy-mihomo').value, 'C:/data/tools/mihomo.exe');
    assert.match(page.window.document.querySelector('#s-mihomo-status').textContent, /请保存设置/);
    assert.equal(page.window.settingsDirty, true);
    await page.submit();
    assert.equal(page.calls[1].body.outbound_proxy.mihomo_path, 'C:/data/tools/mihomo.exe');
  } finally { page.dom.window.close(); }
});

test('a failed Mihomo installation leaves settings untouched and allows retry', async () => {
  const page = await settingsPage(undefined, 'SHA-256 校验失败');
  try {
    const button = page.window.document.querySelector('#s-mihomo-install');
    button.click();
    await page.settle();
    assert.match(page.window.document.querySelector('#s-mihomo-status').textContent, /校验失败/);
    assert.equal(page.window.document.querySelector('#s-proxy-mihomo').value, 'mihomo');
    assert.equal(button.disabled, false);
    assert.equal(page.window.settingsDirty, false);
  } finally { page.dom.window.close(); }
});

test('an existing Mihomo is detected on load without saving or installing', async () => {
  const path = 'C:/already-installed/mihomo.exe';
  const page = await settingsPage(undefined, '', { mihomo: { installed: true, path } });
  try {
    const document = page.window.document;
    assert.equal(document.querySelectorAll('#settings-form > details[open]').length, 0);
    assert.match(document.querySelector('#s-mihomo-status').textContent, /已检测到可执行文件/);
    assert.ok(document.querySelector('#s-mihomo-status').textContent.includes(path));
    assert.equal(document.querySelector('#s-mihomo-install').disabled, true);
    assert.equal(page.window.settingsDirty, false);
    assert.equal(page.calls.length, 0);
  } finally { page.dom.window.close(); }
});

test('Mihomo draft paths are checked automatically and missing paths enable installation', async () => {
  const path = 'C:/custom directory/mihomo.exe';
  const page = await settingsPage(undefined, '', { mihomoPaths: { [path]: { installed: true, path } } });
  try {
    page.change('s-proxy-source', 'subscription');
    await page.settle();
    page.change('s-proxy-mihomo', path);
    await page.settle();
    const document = page.window.document;
    assert.equal(page.mihomoChecks.at(-1), path);
    assert.ok(document.querySelector('#s-mihomo-status').textContent.includes(path));
    assert.equal(document.querySelector('#s-mihomo-install').disabled, true);
    page.change('s-proxy-mihomo', 'C:/missing/mihomo.exe');
    await page.settle();
    assert.match(document.querySelector('#s-mihomo-status').textContent, /未找到 Mihomo/);
    assert.equal(document.querySelector('#s-mihomo-install').disabled, false);
    assert.equal(page.calls.length, 0);
  } finally { page.dom.window.close(); }
});

test('an outdated Mihomo detection cannot overwrite the current path status', async () => {
  let release;
  const oldPath = 'C:/old/mihomo.exe', newPath = 'C:/missing/mihomo.exe';
  const page = await settingsPage(undefined, '', {
    mihomoPaths: { [oldPath]: { installed: true, path: oldPath } },
    mihomoGates: { [oldPath]: new Promise(resolve => { release = resolve; }) },
  });
  try {
    page.change('s-proxy-mihomo', oldPath);
    page.change('s-proxy-mihomo', newPath);
    await page.settle();
    release();
    await page.settle();
    const document = page.window.document;
    assert.match(document.querySelector('#s-mihomo-status').textContent, /未找到 Mihomo/);
    assert.equal(document.querySelector('#s-mihomo-install').disabled, false);
    assert.equal(document.querySelector('#s-proxy-mihomo').value, newPath);
  } finally { release(); page.dom.window.close(); }
});

test('Node and jsdom are checked on load without saving settings', async () => {
  const page = await settingsPage();
  try {
    await page.settle();
    const document = page.window.document;
    assert.match(document.querySelector('#s-node-status').textContent, /可用 · v24\.18\.0/);
    assert.match(document.querySelector('#s-jsdom-status').textContent, /可用 · 26\.1\.0/);
    assert.deepEqual(page.runtimeChecks[0], { node_path: 'node', jsdom_path: '' });
    assert.equal(page.calls.length, 0);
    assert.equal(page.window.settingsDirty, false);
    assert.equal(document.querySelectorAll('#settings-form > details[open]').length, 0);
  } finally { page.dom.window.close(); }
});

test('runtime detection follows edited Node and jsdom paths without saving them', async () => {
  const page = await settingsPage(undefined, '', { runtimeStatus: async paths => ({ ok: true, node: { available: paths.node_path === 'node', message: '未找到 Node.js' }, jsdom: { available: false, message: '未找到 jsdom' } }) });
  try {
    page.change('s-captcha-node', 'D:/自定义 Node/node.exe');
    page.change('s-captcha-jsdom', 'D:/依赖目录');
    await page.settle();
    assert.deepEqual(page.runtimeChecks.at(-1), { node_path: 'D:/自定义 Node/node.exe', jsdom_path: 'D:/依赖目录' });
    assert.match(page.window.document.querySelector('#s-node-status').textContent, /未找到 Node.js/);
    assert.match(page.window.document.querySelector('#s-jsdom-status').textContent, /未找到 jsdom/);
    assert.equal(page.calls.length, 0);
  } finally { page.dom.window.close(); }
});

test('an old jsdom detection result cannot overwrite a newer path status', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const page = await settingsPage(undefined, '', { runtimeStatus: async paths => {
    if (paths.jsdom_path === 'old') await gate;
    return { ok: true, node: { available: true }, jsdom: { available: paths.jsdom_path !== 'missing', message: '未找到 jsdom' } };
  } });
  try {
    page.change('s-captcha-jsdom', 'old');
    page.change('s-captcha-jsdom', 'missing');
    await page.settle();
    release();
    await page.settle();
    assert.match(page.window.document.querySelector('#s-jsdom-status').textContent, /未找到 jsdom/);
  } finally { release(); page.dom.window.close(); }
});

test('large proxy test results show a collapsed summary and reset on each run', async () => {
  const proxyResults = Array.from({ length: 64 }, (_, i) => ({ name: '节点 ' + i, reachable: i % 2 === 0, status: i % 2 === 0 ? 401 : 0, elapsed_ms: 12, message: '连接失败或超时' }));
  const page = await settingsPage(undefined, '', { proxyResults });
  try {
    const document = page.window.document;
    document.querySelector('#s-proxy-test').click();
    await page.settle();
    let details = document.querySelector('#s-proxy-result details');
    assert.equal(details.open, false);
    assert.match(details.querySelector('summary').textContent, /64 个节点 · 可达 32 · 失败 32/);
    assert.equal(details.querySelectorAll('li').length, 64);
    details.querySelector('summary').click();
    assert.equal(details.open, true);
    document.querySelector('#s-proxy-test').click();
    await page.settle();
    details = document.querySelector('#s-proxy-result details');
    assert.equal(details.open, false);
  } finally { page.dom.window.close(); }
});

test('captcha policy descriptions follow restored drafts and saved selections', async () => {
  const page = await settingsPage({ 's-captcha-mode': 'always' });
  try {
    const hint = page.window.document.querySelector('#s-captcha-mode-hint');
    assert.match(hint.textContent, /每次模型请求前/);
    page.change('s-captcha-mode', 'off');
    assert.match(hint.textContent, /不运行验证 SDK/);
    page.change('s-captcha-mode', 'auto');
    assert.match(hint.textContent, /上游要求/);
    await page.submit();
    assert.equal(page.calls.find(call => call.method === 'PUT').body.http.captcha.mode, 'auto');
    assert.equal(page.window.document.querySelector('#s-save').textContent, '已保存');
    assert.equal(page.window.sessionStorage.getItem('z2a_settings_draft'), null);
  } finally { page.dom.window.close(); }
});

test('Mihomo target comes from the server platform and unsupported platforms cannot install', async t => {
  for (const [platform, label] of [['linux/amd64', 'Linux / x64'], ['linux/arm64', 'Linux / ARM64'], ['windows/amd64', 'Windows / x64'], ['windows/arm64', 'Windows / ARM64'], ['darwin/amd64', 'macOS / x64'], ['darwin/arm64', 'macOS / ARM64']]) {
    await t.test(platform, async () => {
      const page = await settingsPage(undefined, '', { mihomo: { platform } });
      try {
        assert.ok(page.window.document.querySelector('#s-mihomo-install').title.includes(label));
        assert.equal(page.window.document.querySelector('#s-mihomo-target'), null);
        assert.equal(page.calls.length, 0);
        assert.equal(page.window.document.querySelector('#s-mihomo-install').disabled, false);
      } finally { page.dom.window.close(); }
    });
  }
  const page = await settingsPage(undefined, '', { mihomo: { platform: 'linux/riscv64', supported: false } });
  try {
    assert.equal(page.window.document.querySelector('#s-mihomo-install').disabled, true);
    assert.match(page.window.document.querySelector('#s-mihomo-status').textContent, /手动安装/);
    page.window.document.querySelector('#s-mihomo-install').click();
    assert.equal(page.calls.length, 0);
  } finally { page.dom.window.close(); }
});

test('failed settings saves retain the draft and allow retry', async () => {
  const page = await settingsPage(undefined, '', { saveError: 'offline save failure' });
  try {
    page.change('s-poll', '120');
    await page.submit();
    assert.equal(page.window.settingsDirty, true);
    assert.equal(page.window.document.querySelector('#s-save').disabled, false);
    assert.match(page.window.document.querySelector('#settings-error').textContent, /offline save failure/);
    assert.equal(JSON.parse(page.window.sessionStorage.getItem('z2a_settings_draft'))['s-poll'], '120');
  } finally { page.dom.window.close(); }
});

test('edits during a pending save remain dirty and a second click cannot duplicate the request', async () => {
  let release;
  const page = await settingsPage(undefined, '', { saveGate: new Promise(resolve => { release = resolve; }) });
  try {
    page.change('s-poll', '120');
    await page.submit();
    assert.equal(page.window.document.querySelector('#s-save').disabled, true);
    await page.submit();
    assert.equal(page.calls.filter(call => call.method === 'PUT').length, 1);
    page.change('s-poll', '180');
    release();
    await page.settle();
    assert.equal(page.window.settingsDirty, true);
    assert.equal(page.window.document.querySelector('#s-save').disabled, false);
    assert.equal(JSON.parse(page.window.sessionStorage.getItem('z2a_settings_draft'))['s-poll'], '180');
  } finally { release(); page.dom.window.close(); }
});

test('unavailable browser draft storage does not prevent settings edits or saving', async () => {
  const page = await settingsPage();
  try {
    page.window.Storage.prototype.setItem = () => { throw new Error('quota exceeded'); };
    page.change('s-poll', '120');
    assert.match(page.window.document.querySelector('#s-save').title, /无法保存草稿/);
    await page.submit();
    assert.equal(page.window.settingsDirty, false);
  } finally { page.dom.window.close(); }
});

test('SDK rejects insecure origins and authenticated proxies before loading any dependency', async () => {
  const loadJSDOM = () => { throw new Error('must not load'); };
  assert.equal((await run({ ...sdkConfig, origin: 'http://example.test' }, { loadJSDOM })).code, 'captcha_origin_invalid');
  assert.equal((await run({ ...sdkConfig, proxy_url: 'http://user:password@localhost:8080' }, { loadJSDOM })).code, 'captcha_proxy_unsupported');
});

test('SDK cancellation closes the jsdom realm and removes process signal listeners', async () => {
  let window;
  const before = process.listenerCount('SIGINT');
  const pending = run(sdkConfig, { loadJSDOM: () => sdkLibrary('window.initAliyunCaptcha = () => {};', realm => { window = realm; }), delayMs: 0, timeoutMs: 1000 });
  process.emit('SIGINT');
  assert.equal((await pending).code, 'captcha_canceled');
  assert.equal(process.listenerCount('SIGINT'), before);
  assert.equal(window.document, undefined);
});
