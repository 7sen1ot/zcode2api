'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('../captcha_node/node_modules/jsdom');
const source = fs.readFileSync(path.join(__dirname, '../src/web/logs-view.js'), 'utf8');

async function logPage(options = {}) {
  const dom = new JSDOM('<main></main>', { url: 'http://localhost/#/logs', runScripts: 'outside-only' });
  const { window } = dom, main = window.document.querySelector('main'), calls = [];
  window.eval(source);
  const rows = Array.from({ length: 65 }, (_, index) => ({ id: 65-index, ts: 1800000000000-index, request_id: `request-${65-index}`, model: 'GLM', status: index === 64 ? 'failed' : index === 63 ? 'canceled' : 'success', error_code: index === 64 ? '3008' : '', error_message: index === 64 ? '账号并发已满' : '', account_id: 'account-id', key_id: 'key-id', tokens_in: 1, tokens_out: 2, ...options.row }));
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  let task = Promise.resolve();
  const helpers = {
    api: async raw => {
      calls.push(raw);
      if (options.gate && raw.startsWith('/api/usage')) await options.gate;
      const query = new URL(raw, 'http://localhost').searchParams;
      let filtered = rows;
      if (query.get('status') === 'failed') filtered = filtered.filter(row => row.status === 'failed');
      if (query.get('status') === 'canceled') filtered = filtered.filter(row => row.status === 'canceled');
      if (query.get('error_code')) filtered = filtered.filter(row => row.error_code === query.get('error_code'));
      const page = Math.min(Math.max(1, Number(query.get('page'))), Math.max(1, Math.ceil(filtered.length/20)));
      return { items: filtered.slice((page-1)*20, page*20), total: filtered.length, page, pages: Math.max(1, Math.ceil(filtered.length/20)), page_size: 20, snapshot: 65, as_of: 1800000000000, summary: { total: filtered.length, success: 63, busy: 0, failed: 1, canceled: 1, tokens: 195 }, filters: { models: [{ value: 'GLM', label: 'GLM' }], errors: [{ value: '3008', label: '账号并发已满（3008）' }], policies: [{ value: 'round_robin', label: 'round_robin' }], actors: [{ value: 'admin', label: 'admin' }], actions: [{ value: 'login.fail', label: 'login.fail' }], ips: [{ value: '127.0.0.1', label: '127.0.0.1' }] } };
    },
    esc, icon: () => '', fmtTime: String, fmtCompactTime: String, fmtInt: value => String(value ?? '—'), policyLabel: value => value || '—', pagehead: (title, desc, actions) => `<h1>${title}</h1><p>${desc}</p>${actions}`,
    rerender: () => { task = window.Z2ALogs.render(main, helpers); return task; },
  };
  await helpers.rerender();
  const settle = async () => { await new Promise(resolve => setImmediate(resolve)); await task; };
  const change = async (name, value) => {
    const input = main.querySelector(`[name="${name}"]`);
    input.value = value;
    input.dispatchEvent(new window.Event('change', { bubbles: true }));
    await settle();
  };
  const click = async selector => { main.querySelector(selector).click(); await settle(); };
  return { dom, window, main, calls, change, click, settle, render: helpers.rerender };
}

test('调用日志每页 20 条，分页保持筛选和查询快照，刷新重新读取最新记录', async () => {
  const page = await logPage();
  try {
    assert.equal(page.main.querySelectorAll('.log-desktop tbody tr').length, 20);
    assert.match(page.main.textContent, /第 1–20 条 \/ 共 65 条/);
    assert.match(page.main.querySelector('#lf-error_code').textContent, /账号并发已满/);
    await page.click('[aria-label="下一页"]');
    let query = new URL(page.calls.at(-1), 'http://localhost').searchParams;
    assert.equal(query.get('page'), '2');
    assert.equal(query.get('snapshot'), '65');
    assert.match(page.main.textContent, /第 21–40 条/);
    await page.change('status', 'failed');
    query = new URL(page.calls.at(-1), 'http://localhost').searchParams;
    assert.equal(query.get('status'), 'failed');
    assert.equal(query.get('page'), '1');
    assert.equal(query.has('snapshot'), false);
    assert.equal(page.main.querySelectorAll('.log-desktop tbody tr').length, 1);
    assert.match(page.main.querySelector('.log-desktop').textContent, /账号并发已满/);
    await page.click('#log-refresh');
    query = new URL(page.calls.at(-1), 'http://localhost').searchParams;
    assert.equal(query.get('status'), 'failed');
    assert.equal(query.has('snapshot'), false);
  } finally { page.dom.window.close(); }
});

test('调度与审计独立筛选，切换分区和重置不会覆盖其他分区条件', async () => {
  const page = await logPage();
  try {
    await page.change('error_code', '3008');
    await page.click('[data-ltab="sched"]');
    await page.change('outcome', 'unpicked');
    assert.match(page.calls.at(-1), /outcome=unpicked/);
    assert.doesNotMatch(page.calls.at(-1), /error_code/);
    await page.change('policy', 'round_robin');
    await page.click('[data-ltab="audit"]');
    await page.change('actor', 'admin');
    await page.change('action', 'login.fail');
    await page.change('ip', '127.0.0.1');
    assert.match(page.calls.at(-1), /actor=admin/);
    assert.match(page.calls.at(-1), /action=login.fail/);
    assert.match(page.calls.at(-1), /ip=127.0.0.1/);
    assert.doesNotMatch(page.calls.at(-1), /outcome/);
    await page.click('[data-ltab="sched"]');
    assert.equal(page.main.querySelector('#lf-outcome').value, 'unpicked');
    await page.click('#lf-reset');
    assert.doesNotMatch(page.calls.at(-1), /outcome|policy/);
    await page.click('[data-ltab="logs"]');
    assert.equal(page.main.querySelector('#lf-error_code').value, '3008');
  } finally { page.dom.window.close(); }
});

test('自定义日期范围验证、指定页跳转和取消状态正常工作', async () => {
  const page = await logPage();
  try {
    page.main.querySelector('#log-page-jump input').value = '4';
    page.main.querySelector('#log-page-jump').dispatchEvent(new page.window.Event('submit', { bubbles: true, cancelable: true }));
    await page.settle();
    assert.equal(page.main.querySelectorAll('.log-desktop tbody tr').length, 5);
    await page.change('hours', '24');
    await page.change('since', '2026-01-02T00:00:00');
    assert.equal(page.main.querySelector('#lf-hours').value, 'all');
    const before = page.calls.length;
    await page.change('until', '2026-01-01T00:00:00');
    assert.equal(page.calls.length, before);
    assert.match(page.main.querySelector('#lf-error').textContent, /结束时间/);
    await page.click('#lf-reset');
    await page.change('status', 'canceled');
    assert.match(page.main.querySelector('.log-desktop').textContent, /已取消/);
  } finally { page.dom.window.close(); }
});

test('中文输入法组合期间不发起搜索，完成输入后保留关键词', async () => {
  const page = await logPage();
  try {
    const input = page.main.querySelector('#lf-q');
    input.focus();
    input.dispatchEvent(new page.window.CompositionEvent('compositionstart', { bubbles: true }));
    input.value = '账号并发';
    input.dispatchEvent(new page.window.Event('input', { bubbles: true }));
    await new Promise(resolve => setTimeout(resolve, 350));
    assert.equal(page.calls.length, 1);
    input.dispatchEvent(new page.window.CompositionEvent('compositionend', { bubbles: true }));
    await new Promise(resolve => setTimeout(resolve, 350));
    await page.settle();
    assert.equal(new URL(page.calls.at(-1), 'http://localhost').searchParams.get('q'), '账号并发');
    assert.equal(page.main.ownerDocument.activeElement.id, 'lf-q');
  } finally { page.dom.window.close(); }
});

test('日志链接还原所有筛选条件，旧失败状态兼容，巨大页数只生成有限分页按钮', async () => {
  const page = await logPage();
  try {
    page.window.Z2ALogs.restoreRoute({ tab: 'audit', page: '12', q: '登录 & 查询', actor: 'admin', action: 'login.fail', ip: '127.0.0.1', since: '2026-01-01T00:00', snapshot: '99', as_of: '1800000000000' });
    const route = page.window.Z2ALogs.routeValues();
    assert.equal(route.page, 12);
    assert.equal(route.actor, 'admin');
    assert.equal(route.q, '登录 & 查询');
    const query = new URL(page.window.Z2ALogs.queryFor('audit'), 'http://localhost').searchParams;
    assert.equal(query.get('snapshot'), '99');
    assert.ok(Number(query.get('since')) > 0);
    page.window.Z2ALogs.restoreRoute({ tab: 'logs', status: 'error' });
    assert.equal(page.window.Z2ALogs.routeValues().status, 'failed');
    assert.ok(page.window.Z2ALogs.pageNumbers(5000000, 10000000).length < 12);
  } finally { page.dom.window.close(); }
});

test('日志、错误内容和筛选选项转义上游文本', async () => {
  const unsafe = '<img src=x onerror=alert(1)>';
  const page = await logPage({ row: { account_label: unsafe, error_message: unsafe, request_id: unsafe, trace_id: unsafe } });
  try {
    assert.equal(page.main.querySelector('img'), null);
    assert.match(page.main.innerHTML, /&lt;img/);
  } finally { page.dom.window.close(); }
});

test('详情浮层独立于日志行，切换记录、外部点击和 Escape 均可正确关闭', async () => {
  const page = await logPage();
  try {
    const triggers = page.main.querySelectorAll('[data-log-detail]');
    const panel = page.main.querySelector('#log-detail-popover');
    assert.equal(panel.hidden, true);
    assert.equal(panel.closest('table'), null);
    await page.click('[data-log-detail="0"]');
    assert.equal(panel.hidden, false);
    assert.equal(triggers[0].getAttribute('aria-expanded'), 'true');
    assert.match(panel.textContent, /request-65/);
    panel.dispatchEvent(new page.window.Event('pointerdown', { bubbles: true }));
    assert.equal(panel.hidden, false);
    await page.click('[data-log-detail="1"]');
    assert.equal(triggers[0].getAttribute('aria-expanded'), 'false');
    assert.equal(triggers[1].getAttribute('aria-expanded'), 'true');
    assert.match(panel.textContent, /request-64/);
    page.window.document.dispatchEvent(new page.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    assert.equal(panel.hidden, true);
    assert.equal(page.window.document.activeElement, triggers[1]);
    await page.click('[data-log-detail="0"]');
    page.main.querySelector('h1').dispatchEvent(new page.window.Event('pointerdown', { bubbles: true }));
    assert.equal(panel.hidden, true);
    await page.click('[data-log-detail="0"]');
    await page.render();
    assert.equal(page.main.querySelector('#log-detail-popover').hidden, true);
  } finally { page.window.Z2ALogs.closeDetails(); page.dom.window.close(); }
});

test('更多筛选按钮同步展开状态、收起文案和已选数量', async () => {
  const page = await logPage();
  try {
    const details = page.main.querySelector('#log-advanced');
    const trigger = details.querySelector('summary');
    assert.equal(trigger.getAttribute('aria-expanded'), 'false');
    trigger.click();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(trigger.getAttribute('aria-expanded'), 'true');
    assert.match(trigger.textContent, /收起筛选/);
    await page.change('model', 'GLM');
    assert.equal(page.main.querySelector('.log-filter-count').textContent, '1');
    const updated = page.main.querySelector('#log-advanced > summary');
    updated.click();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(updated.getAttribute('aria-expanded'), 'false');
    assert.match(updated.textContent, /更多筛选/);
    assert.equal(page.main.querySelector('#lf-model').value, 'GLM');
  } finally { page.dom.window.close(); }
});

test('调度候选使用独立浮层，展示账号及中文跳过原因并转义账号名称', async () => {
  const page = await logPage({ row: { candidates: JSON.stringify([
    { account: 'a', label: '已选账号', picked: true, skip: null },
    { account: 'b', label: '<img src=x onerror=alert(1)>', picked: false, skip: 'upstream_busy' },
    { account: 'c', picked: false, skip: 'quota_remaining=0' },
    { account: 'd', picked: false, skip: 'status=needs_relogin' },
  ]) } });
  try {
    await page.click('[data-ltab="sched"]');
    assert.equal(page.main.querySelector('tbody details'), null);
    assert.match(page.main.querySelector('[data-log-detail="0"]').textContent, /候选 4/);
    await page.click('[data-log-detail="0"]');
    const panel = page.main.querySelector('#log-detail-popover');
    assert.equal(panel.closest('table'), null);
    assert.match(panel.textContent, /已选中/);
    assert.match(panel.textContent, /账号并发已满/);
    assert.match(panel.textContent, /账号额度不足/);
    assert.match(panel.textContent, /需要重新登录/);
    assert.equal(panel.querySelectorAll('.log-candidate-item').length, 4);
    assert.equal(panel.querySelector('img'), null);
    assert.equal(panel.querySelector('pre'), null);
    page.window.Z2ALogs.closeDetails();
    assert.equal(panel.hidden, true);
  } finally { page.window.Z2ALogs.closeDetails(); page.dom.window.close(); }
});

test('历史候选为空或格式损坏时显示明确说明', async () => {
  for (const [value, message] of [['[]', '此轮没有候选账号'], ['invalid json', '候选信息格式无法识别']]) {
    const page = await logPage({ row: { candidates: value } });
    try {
      await page.click('[data-ltab="sched"]');
      await page.click('[data-log-detail="0"]');
      assert.match(page.main.querySelector('#log-detail-popover').textContent, new RegExp(message));
    } finally { page.window.Z2ALogs.closeDetails(); page.dom.window.close(); }
  }
});
