const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('../captcha_node/node_modules/jsdom');
const source = fs.readFileSync(path.join(__dirname, '../src/web/claims-view.js'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, '../src/web/app.js'), 'utf8');

function clock(window) {
  let now = 0, sequence = 0;
  const timers = new Map();
  window.setTimeout = (callback, delay) => {
    const id = ++sequence;
    timers.set(id, { at: now + delay, callback });
    return id;
  };
  window.clearTimeout = id => timers.delete(id);
  return ms => {
    const until = now + ms;
    for (;;) {
      const next = [...timers].filter(([, timer]) => timer.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      const [id, timer] = next;
      timers.delete(id);
      now = timer.at;
      timer.callback();
    }
    now = until;
  };
}

function setup() {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'http://server.example:6789', runScripts: 'outside-only' });
  const w = dom.window, posted = [], widgets = [], started = [];
  const advance = clock(w);
  w.$ = (selector, root = w.document) => root.querySelector(selector);
  w.h = html => { const t = w.document.createElement('template'); t.innerHTML = html; return t.content.firstElementChild; };
  w.esc = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  w.eval('let openSelect = null; function enhanceSelects() {} function isImeComposing() { return false; }\n' +
    app.slice(app.indexOf('function modal('), app.indexOf('function confirmAction(')));
  w.api = async (url, options) => {
    if (options) { posted.push(options.body); return { ok: true }; }
    return { enabled: true, region: 'cn', prefix: 'offline', scene_id: 'test-scene' };
  };
  w.initAliyunCaptcha = options => {
    widgets.push(options);
    options.getInstance({ destroy() {}, startTracelessVerification() { started.push(options); } });
  };
  w.eval(source);
  return { dom, w, posted, widgets, started, advance };
}

test('claim starts official verification automatically and uses a fresh ticket per account', async () => {
  const { dom, w, posted, widgets, started, advance } = setup();
  try {
    for (const id of ['account-a', 'account-b']) {
      const result = w.submitClaim({ account_id: id, plan_id: 'activity-card' }, id);
      await new Promise(resolve => setImmediate(resolve));
      const widget = widgets.at(-1);
      advance(2000);
      assert.equal(started.length, widgets.length - 1, 'device collection must last more than two seconds');
      advance(100);
      assert.equal(started.length, widgets.length, 'verification starts without another click');
      assert.equal(posted.length, widgets.length - 1, 'no claim may be sent before verification');
      assert.equal(widget.SceneId, 'test-scene');
      assert.ok(w.document.querySelector(widget.element));
      assert.equal(widget.button, undefined);
      assert.equal(w.document.querySelectorAll('[data-cancel]').length, 1);
      widget.success('synthetic-ticket-' + id);
      assert.equal((await result).ok, true);
      advance(350);
    }
    assert.deepEqual(posted.map(body => body.captcha_verify_param), ['synthetic-ticket-account-a', 'synthetic-ticket-account-b']);
    assert.deepEqual(posted.map(body => body.captcha_region), ['cn', 'cn']);
    assert.equal(w.localStorage.length, 0);
    assert.equal(w.sessionStorage.length, 0);
  } finally { dom.window.close(); }
});

test('cancel, close, Escape and backdrop block late success before the close animation ends', async () => {
  for (const close of [
    w => w.document.querySelector('[data-cancel]').click(),
    w => w.document.querySelector('.modal-close').click(),
    w => w.document.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' })),
    w => w.document.querySelector('.modal-bg').click(),
  ]) {
    const { dom, w, posted, widgets, started, advance } = setup();
    try {
      const result = w.submitClaim({ account_id: 'a', plan_id: 'card' }, 'A');
      await new Promise(resolve => setImmediate(resolve));
      close(w);
      assert.ok(w.document.querySelector('.modal-bg.closing'), 'real modal remains mounted during its animation');
      widgets[0].success('late-ticket');
      assert.equal(await result, null);
      assert.equal(posted.length, 0);
      advance(2100);
      assert.equal(started.length, 0);
      assert.equal(w.document.querySelector('.modal-bg'), null);
    } finally { dom.window.close(); }
  }
});

test('a failure before the automatic start never triggers verification afterwards', async () => {
  const { dom, w, posted, widgets, started, advance } = setup();
  try {
    const result = w.submitClaim({ account_id: 'a', plan_id: 'card' }, 'A');
    await new Promise(resolve => setImmediate(resolve));
    widgets[0].fail({ verifyCode: 'F001' });
    assert.match(w.document.querySelector('[role=status]').textContent, /验证未通过/);
    advance(2100);
    assert.equal(started.length, 0);
    w.document.querySelector('[data-cancel]').click();
    assert.equal(await result, null);
    assert.equal(posted.length, 0);
  } finally { dom.window.close(); }
});

test('removing the dialog cancels verification and ignores late success', async () => {
  const { dom, w, posted, widgets } = setup();
  try {
    const result = w.submitClaim({ account_id: 'a', plan_id: 'card' }, 'A');
    await new Promise(resolve => setImmediate(resolve));
    w.document.body.replaceChildren();
    await new Promise(resolve => setImmediate(resolve));
    widgets[0].success('late-ticket');
    assert.equal(await result, null);
    assert.equal(posted.length, 0);
  } finally { dom.window.close(); }
});

test('initialization errors and timeouts send no claim and ignore late tickets', async () => {
  for (const failure of ['throw', 'reject', 'timeout']) {
    const { dom, w, posted, widgets, advance } = setup();
    if (failure === 'throw') w.initAliyunCaptcha = () => { throw new Error('failed'); };
    if (failure === 'reject') w.initAliyunCaptcha = () => Promise.reject(new Error('failed'));
    try {
      const result = w.submitClaim({ account_id: 'a', plan_id: 'card' }, 'A');
      const rejected = assert.rejects(result, failure === 'timeout' ? /超时/ : /初始化失败/);
      await new Promise(resolve => setImmediate(resolve));
      if (failure === 'timeout') advance(120000);
      await rejected;
      widgets[0]?.success('late-ticket');
      assert.equal(posted.length, 0);
    } finally { dom.window.close(); }
  }
});

test('disabled upstream verification allows a direct claim without a ticket', async () => {
  const { dom, w, posted, widgets } = setup();
  const api = w.api;
  w.api = (url, options) => options ? api(url, options) : Promise.resolve({ enabled: false });
  try {
    assert.equal((await w.submitClaim({ account_id: 'a', plan_id: 'card' }, 'A')).ok, true);
    assert.equal(widgets.length, 0);
    assert.equal(posted.length, 1);
    assert.equal(posted[0].captcha_verify_param, undefined);
  } finally { dom.window.close(); }
});
