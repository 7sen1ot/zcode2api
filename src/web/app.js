'use strict';
const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];
const h = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmtInt = (n) => n === null || n === undefined ? '—' : Number(n).toLocaleString('zh-CN');
const fmtQuota = (n) => {
  if (n === null || n === undefined || !Number.isFinite(Number(n))) return '—';
  const value = Number(n), unit = value >= 100000000 ? 100000000 : value >= 10000 ? 10000 : 1;
  return unit === 1 ? fmtInt(value) : `${(value / unit).toLocaleString('zh-CN', { maximumFractionDigits: 2 })}${unit === 10000 ? '万' : '亿'}`;
};
const fmtTime = (ts) => ts ? new Date(ts).toLocaleString('zh-CN', { hour12: false }) : '—';
const fmtCompactTime = (ts) => {
  if (!ts) return '时间未知';
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return '时间未知';
  const options = { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false };
  if (date.getFullYear() !== new Date().getFullYear()) options.year = 'numeric';
  return date.toLocaleString('zh-CN', options);
};
const fmtDur = (ms) => {
  if (ms === null || ms === undefined) return '—';
  if (ms < 0) return '已过期';
  const m = Math.floor(ms / 60000);
  if (m < 60) return `${m} 分钟`;
  const hr = Math.floor(m / 60);
  if (hr < 48) return `${hr} 小时 ${m % 60} 分`;
  return `${Math.floor(hr / 24)} 天`;
};

const fmtPlanRemaining = (ms) => {
  if (ms <= 0) return '已到期';
  const hours = Math.floor(ms / 3600000);
  if (hours < 48) return `${fmtDur(ms)}后到期`;
  const days = Math.floor(hours / 24), rest = hours % 24;
  return `${days} 天${rest ? ` ${rest} 小时` : ''}后到期`;
};

const ICONS = {
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5"/><path d="M12 7h.01"/>',
  dashboard: '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/>',
  users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><path d="M16 3.128a4 4 0 0 1 0 7.744"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><circle cx="9" cy="7" r="4"/>',
  gift: '<rect x="3" y="8" width="18" height="4" rx="1"/><path d="M12 8v13"/><path d="M19 12v7a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2v-7"/><path d="M7.5 8a2.5 2.5 0 0 1 0-5C11 3 12 8 12 8s1-5 4.5-5a2.5 2.5 0 0 1 0 5"/>',
  key: '<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/>',
  trending: '<path d="M16 7h6v6"/><path d="m22 7-8.5 8.5-5-5L2 17"/>',
  scroll: '<path d="M15 12h-5"/><path d="M15 8h-5"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3"/>',
  folder: '<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>',
  settings: '<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>',
  zap: '<path d="M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z"/>',
  pencil: '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/>',
  trash: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>',
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
  activity: '<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>',
  gauge: '<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
  layers: '<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>',
  timer: '<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>',
  plug: '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>',
  shield: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>',
  eye: '<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>',
  'eye-off': '<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/>',
};
const icon = (name, size = 16) =>
  `<svg class="lucide" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ''}</svg>`;

let TOKEN = sessionStorage.getItem('z2a_token') || localStorage.getItem('z2a_token') || '';
let RUNTIME = null;
let currentPage = 'dashboard';
const UI = globalThis.Z2AUI;
let settingsDirty = false;
let settingsSaving = false;
let routeURL = location.hash;

async function api(path, opts = {}) {
  const hasBody = opts.body !== undefined;
  const res = await fetch(path, {
    method: opts.method || 'GET',
    headers: {
      ...(hasBody ? { 'content-type': 'application/json' } : {}),
      ...(TOKEN ? { authorization: 'Bearer ' + TOKEN } : {}),
    },
    body: hasBody ? JSON.stringify(opts.body) : undefined,
  });
  if (res.status === 401) { showLogin(); throw new Error('未登录'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || data.msg || `HTTP ${res.status}`);
  return data;
}

function toast(msg, type = '') {
  const t = h(`<div class="toast" role="status">${icon(type === 'ok' ? 'shield' : 'zap', 14)}${esc(msg)}</div>`);
  document.body.appendChild(t);
  setTimeout(() => {
    t.classList.add('out');
    t.addEventListener('transitionend', () => t.remove(), { once: true });
    setTimeout(() => t.remove(), 400);
  }, 2500);
}

let composing = false;
let imeGuardUntil = 0;
const safariImeGuard = /^((?!chrome|chromium|crios|android|edg).)*safari/i.test(navigator.userAgent);
function isImeComposing(e) {
  return composing || e.isComposing || e.keyCode === 229 || (safariImeGuard && performance.now() < imeGuardUntil);
}
document.addEventListener('compositionstart', () => { composing = true; });
document.addEventListener('compositionend', () => { composing = false; imeGuardUntil = performance.now() + 500; });
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || !e.target.closest?.('form')) return;
  if (composing || e.isComposing || e.keyCode === 229) {
    e.stopPropagation();
  } else if (safariImeGuard && performance.now() < imeGuardUntil) {
    e.preventDefault();
    e.stopPropagation();
  }
}, true);
document.addEventListener('submit', (e) => {
  if (composing) { e.preventDefault(); e.stopImmediatePropagation(); }
}, true);

function showLogin() {
  $('#login-view').classList.remove('hidden');
  $('#app-view').classList.add('hidden');
}
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const card = $('.login-card');
  const btn = $('#login-form button[type=submit]');
  btn.classList.add('loading');
  try {
    const r = await api('/api/auth/login', { method: 'POST', body: { password: $('#login-password').value } });
    TOKEN = r.token;
    localStorage.setItem('z2a_token', TOKEN);
    $('#login-error').textContent = '';
    boot();
  } catch (err) {
    $('#login-error').textContent = err.message;
    card.classList.remove('shake');
    void card.offsetWidth;
    card.classList.add('shake');
  } finally { btn.classList.remove('loading'); }
});
$('#pw-toggle').addEventListener('click', () => {
  const input = $('#login-password');
  const btn = $('#pw-toggle');
  const show = input.type === 'password';
  input.type = show ? 'text' : 'password';
  btn.setAttribute('aria-pressed', String(show));
  btn.setAttribute('aria-label', show ? '隐藏密码' : '显示密码');
  btn.innerHTML = icon(show ? 'eye-off' : 'eye', 15);
});
document.addEventListener('click', async (e) => {
  const logout = e.target.closest('#logout-btn');
  if (logout) {
    if (!await mayLeaveSettings()) return;
    try { await api('/api/auth/logout', { method: 'POST' }); } catch { }
    TOKEN = ''; logCache = null; localStorage.removeItem('z2a_token'); sessionStorage.removeItem('z2a_token'); showLogin();
    return;
  }
  const themeBtn = e.target.closest('#theme-btn');
  if (themeBtn) {
    const root = document.documentElement;
    root.classList.remove('theme-swap');
    void root.offsetWidth;
    root.classList.add('theme-swap');
    setTheme(root.classList.contains('dark') ? 'light' : 'dark');
  }
});

function applyThemeBtn() {
  const label = $('#theme-btn-label');
  const theme = document.documentElement.classList.contains('dark') ? '深色' : '浅色';
  if (label) label.textContent = theme;
  $('#theme-btn')?.setAttribute('aria-label', `${theme}，切换亮/暗主题`);
}
function setTheme(mode) {
  if (mode === 'system') localStorage.removeItem('z2a_theme');
  else localStorage.setItem('z2a_theme', mode);
  const dark = mode === 'dark' || (mode === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.classList.toggle('dark', dark);
  applyThemeBtn();
}
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  if (!localStorage.getItem('z2a_theme')) setTheme('system');
});
applyThemeBtn();

function routeValues() {
  if (currentPage === 'dashboard') return { hours: window._dashboardHours && window._dashboardHours !== 'all' ? window._dashboardHours : '', days: window._dashboardDays === 30 ? 30 : '' };
  if (currentPage === 'accounts') { const f = window._accFilter || {}; return { q: f.q, status: f.status === 'all' ? '' : f.status, sort: f.sort === 'default' ? '' : f.sort, page: f.page > 1 ? f.page : '', size: f.size && f.size !== 25 ? f.size : '' }; }
  if (currentPage === 'usage') { const f = window._usageDetail || {}; return { hours: window._usageHours || 24, tab: f.tab || 'model', page: f.pages?.[f.tab] || 1 }; }
  if (currentPage === 'logs') return Z2ALogs.routeValues();
  return {};
}
function syncRoute(replace = true) {
  const hash = UI.routeHash(currentPage, routeValues());
  if (hash !== location.hash) history[replace ? 'replaceState' : 'pushState'](null, '', hash);
  routeURL = hash;
  document.title = `${PAGES.find(p => p[0] === currentPage)?.[1] || '控制台'} · zcode2api`;
}
function restoreRoute(route) {
  const q = route.query;
  const valid = (value, choices, fallback) => choices.includes(value) ? value : fallback;
  const page = Math.max(1, Math.min(100000, Math.floor(Number(q.page)) || 1));
  currentPage = route.page;
  if (currentPage === 'dashboard') { window._dashboardHours = valid(q.hours, ['all', '24', '168', '720'], 'all'); window._dashboardDays = Number(valid(q.days, ['7', '30'], '7')); }
  if (currentPage === 'accounts') window._accFilter = { q: (q.q || '').slice(0, 200), status: valid(q.status, ['active', 'needs_relogin', 'cooldown', 'expired', 'web_only'], 'all'), sort: valid(q.sort, ['remaining', 'expiring'], 'default'), page, size: Number(valid(q.size, ['25', '50', '100'], '25')) };
  if (currentPage === 'usage') { window._usageHours = Number(valid(q.hours, ['24', '168', '720'], '24')); const tab = valid(q.tab, ['model', 'key', 'account', 'status'], 'model'); window._usageDetail = { tab, pages: { [tab]: page } }; }
  if (currentPage === 'logs') Z2ALogs.restoreRoute(q);
}
async function mayLeaveSettings() {
  if (currentPage === 'settings' && settingsSaving) { toast('正在保存设置，请稍候'); return false; }
  return true;
}
async function navigateTo(page) {
  if (!PAGES.some(p => p[0] === page) || page === currentPage) return false;
  if (!await mayLeaveSettings()) return false;
  currentPage = page;
  delete $('#main').dataset.fp;
  syncRoute(false);
  renderNav();
  await render();
  window.scrollTo({ top: 0, behavior: 'instant' });
  return true;
}
window.addEventListener('hashchange', async () => {
  if (location.hash === '#main' || location.hash === routeURL) return;
  if (!await mayLeaveSettings()) { history.replaceState(null, '', routeURL); return; }
  restoreRoute(UI.readRoute(location.hash));
  delete $('#main').dataset.fp;
  syncRoute(); renderNav(); await render();
  window.scrollTo({ top: 0, behavior: 'instant' });
});
function renderNav() {
  const nav = $('#nav');
  const dock = document.createElement('div');
  dock.className = 'dock';
  for (const entry of PAGES) {
    if (entry === '|') { dock.appendChild(h('<span class="dock-sep"></span>')); continue; }
    const [id, label, ic] = entry;
    const b = h(`<button class="dock-item${id === currentPage ? ' active' : ''}" data-page="${id}" aria-label="${label}" ${id === currentPage ? 'aria-current="page"' : ''}>
      <span class="di">${icon(ic, 16)}</span><span class="tip">${label}</span>
    </button>`);
    b.addEventListener('click', () => navigateTo(id));
    dock.appendChild(b);
  }
  nav.innerHTML = '';
  nav.appendChild(dock);
  applyThemeBtn();
}

let pendingRender = null;
let renderTask = null;
let lastRefreshAt = 0;
function render(opts = {}) {
  if (renderTask) { pendingRender = opts; return renderTask; }
  renderTask = renderPage(opts).finally(async () => {
    renderTask = null;
    if (pendingRender) { const next = pendingRender; pendingRender = null; await render(next); }
  });
  return renderTask;
}
async function renderPage(opts = {}) {
  const refreshStartedAt = Date.now();
  if (currentPage !== 'logs') Z2ALogs.closeDetails();
  const main = $('#main');
  const page = currentPage;
  main.querySelector('.page-error')?.remove();
  const previous = main.dataset.page === page ? [...main.childNodes] : [];
  if (!previous.length) main.innerHTML = `${pagehead(PAGES.find(p => p[0] === page)?.[1] || '控制台', '正在加载…')}<div class="page-skeleton" aria-hidden="true"><div class="skeleton-block"></div><div class="skeleton-block"></div><div class="skeleton-block"></div></div>`;
  main.dataset.loading = '1';
  main.setAttribute('aria-busy', 'true');
  main.classList.toggle('no-anim', !!opts.swap);
  try {
    if (currentPage === 'dashboard') await pageDashboard(main);
    else if (currentPage === 'accounts') await pageAccounts();
    else if (currentPage === 'claims') await pageClaims(main);
    else if (currentPage === 'usage') await pageUsage(main);
    else if (currentPage === 'keys') await pageKeys(main);
    else if (currentPage === 'groups') await pageGroups(main);
    else if (currentPage === 'settings') await pageSettings(main);
    else if (currentPage === 'logs') await pageLogs(main, opts);
    enhanceSelects(main);
    enhanceTables(main);
    enhanceHints(main);
    main.dataset.page = page;
    if (page === currentPage) syncRoute();
  } catch (e) {
    if (page !== currentPage) return;
    if (opts.live) { Z2ALogs.refreshFailed(main); return; }
    if (previous.length) main.replaceChildren(...previous);
    else main.innerHTML = pagehead(PAGES.find(p => p[0] === page)?.[1] || '控制台');
    const notice = h(`<section class="page-error" role="alert">${icon('zap', 20)}<div><h2>${previous.length ? '刷新失败，已保留上次内容' : '暂时无法加载页面'}</h2><p>${esc(e.message)}</p></div><button type="button" class="btn" data-retry-page>${icon('refresh', 14)}重试</button></section>`);
    main.prepend(notice);
    $('[data-retry-page]', notice).addEventListener('click', () => render({ swap: true }));
  } finally {
    lastRefreshAt = refreshStartedAt;
    delete main.dataset.loading;
    main.setAttribute('aria-busy', 'false');
  }
}

function enhanceTables(root) {
  root.querySelectorAll('.tablecard:not(.account-table):not(.compact-account-table):not(.log-desktop) table').forEach(table => {
    const headers = [...table.querySelectorAll('tr:first-child th')].map(th => th.textContent.trim());
    if (headers.length < 4) return;
    table.closest('.tablecard').classList.add('responsive-table');
    table.querySelectorAll('tr').forEach(row => [...row.children].forEach((cell, index) => {
      if (cell.tagName === 'TD') cell.dataset.label = headers[index] || '';
    }));
  });
}
$('.skip-link')?.addEventListener('click', e => { e.preventDefault(); $('#main').tabIndex = -1; $('#main').focus(); });

const selectControls = new WeakMap();
let openSelect = null;
let selectId = 0;
function syncSelect(select) { selectControls.get(select)?.sync(); }
function enhanceSelects(root) {
  root.querySelectorAll('select:not([multiple])').forEach((select) => {
    if (selectControls.has(select)) return;
    const label = select.getAttribute('aria-label') || select.labels?.[0]?.childNodes[0]?.textContent.trim() || select.parentElement?.querySelector('b')?.textContent.trim() || '选择选项';
    const wrapper = document.createElement('span');
    wrapper.className = `select-control${select.classList.contains('round') ? ' select-control-round' : ''}`;
    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'select-trigger';
    trigger.setAttribute('role', 'combobox');
    trigger.setAttribute('aria-label', label);
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const list = document.createElement('div');
    list.className = 'select-list';
    list.id = `select-list-${++selectId}`;
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', label);
    list.hidden = true;
    if (typeof list.showPopover === 'function') list.setAttribute('popover', 'manual');
    trigger.setAttribute('aria-controls', list.id);
    select.before(wrapper);
    wrapper.append(select, trigger, list);
    select.classList.add('select-native');
    select.tabIndex = -1;
    select.setAttribute('aria-hidden', 'true');
    let active = select.selectedIndex;
    let typeahead = '', typedAt = 0;
    const enabled = () => [...select.options].map((o, i) => o.disabled ? -1 : i).filter((i) => i >= 0);
    const sync = () => {
      trigger.disabled = select.disabled;
      trigger.innerHTML = `<span class="select-value"></span><svg class="select-chevron" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>`;
      $('.select-value', trigger).textContent = select.selectedOptions[0]?.textContent || '请选择';
    };
    const paint = () => {
      list.replaceChildren();
      [...select.options].forEach((option, index) => {
        const item = document.createElement('div');
        item.id = `${list.id}-option-${index}`;
        item.className = 'select-option';
        item.setAttribute('role', 'option');
        item.setAttribute('aria-selected', String(index === select.selectedIndex));
        if (option.disabled) item.setAttribute('aria-disabled', 'true');
        if (index === active) item.classList.add('is-active');
        const text = document.createElement('span');
        text.textContent = option.textContent;
        item.appendChild(text);
        if (index === select.selectedIndex) item.insertAdjacentHTML('beforeend', '<span class="select-check" aria-hidden="true">✓</span>');
        item.addEventListener('pointerdown', (e) => e.preventDefault());
        item.addEventListener('click', () => { if (!option.disabled) choose(index); });
        list.appendChild(item);
      });
      if (active >= 0) {
        trigger.setAttribute('aria-activedescendant', `${list.id}-option-${active}`);
        const item = list.children[active];
        if (item && item.offsetTop < list.scrollTop) list.scrollTop = item.offsetTop;
        else if (item && item.offsetTop + item.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = item.offsetTop + item.offsetHeight - list.clientHeight;
      }
    };
    const close = () => {
      if (list.hasAttribute('popover') && !list.hidden) list.hidePopover();
      list.hidden = true;
      trigger.setAttribute('aria-expanded', 'false');
      trigger.removeAttribute('aria-activedescendant');
      if (openSelect?.trigger === trigger) openSelect = null;
    };
    const open = () => {
      if (select.disabled || !enabled().length) return;
      openSelect?.close();
      active = select.selectedIndex >= 0 && !select.options[select.selectedIndex].disabled ? select.selectedIndex : enabled()[0];
      list.hidden = false;
      if (list.hasAttribute('popover')) list.showPopover();
      trigger.setAttribute('aria-expanded', 'true');
      openSelect = { trigger, wrapper, close };
      paint();
      const rect = trigger.getBoundingClientRect(), height = Math.min(list.scrollHeight, 260);
      const up = innerHeight - rect.bottom < height + 12 && rect.top > height + 12;
      wrapper.classList.toggle('select-open-up', up);
      if (list.hasAttribute('popover')) {
        list.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - Math.max(rect.width, 180) - 8))}px`;
        list.style.top = `${up ? Math.max(8, rect.top - height - 6) : rect.bottom + 6}px`;
        list.style.width = `${Math.max(rect.width, 180)}px`;
      }
    };
    const choose = (index) => {
      const changed = select.selectedIndex !== index;
      select.selectedIndex = index;
      sync(); close(); trigger.focus();
      if (changed) { select.dispatchEvent(new Event('input', { bubbles: true })); select.dispatchEvent(new Event('change', { bubbles: true })); }
    };
    trigger.addEventListener('click', () => list.hidden ? open() : close());
    trigger.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !list.hidden) { e.preventDefault(); e.stopPropagation(); close(); return; }
      if (e.key === 'Tab') { close(); return; }
      if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
        e.preventDefault();
        if (list.hidden) { open(); return; }
        const indices = enabled(), current = indices.indexOf(active);
        active = e.key === 'Home' ? indices[0] : e.key === 'End' ? indices.at(-1) : indices[Math.max(0, Math.min(indices.length - 1, current + (e.key === 'ArrowDown' ? 1 : -1)))];
        paint(); return;
      }
      if ((e.key === 'Enter' || e.key === ' ') && !list.hidden) { e.preventDefault(); choose(active); return; }
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && !e.isComposing) {
        e.preventDefault();
        if (list.hidden) open();
        typeahead = performance.now() - typedAt > 700 ? e.key : typeahead + e.key;
        typedAt = performance.now();
        const match = enabled().find((i) => select.options[i].textContent.trim().toLocaleLowerCase().startsWith(typeahead.toLocaleLowerCase()));
        if (match !== undefined) { active = match; paint(); }
      }
    });
    select.addEventListener('change', sync);
    selectControls.set(select, { sync });
    sync();
  });
}
document.addEventListener('pointerdown', (e) => { if (openSelect && !openSelect.wrapper.contains(e.target)) openSelect.close(); });
document.addEventListener('scroll', (e) => { if (openSelect && !openSelect.wrapper.contains(e.target)) openSelect.close(); }, true);
window.addEventListener('resize', () => openSelect?.close());

function modal(title, contentEl, onClose, options = {}) {
  const bg = h('<div class="modal-bg"></div>');
  const box = h(`<div class="modal" role="dialog" aria-modal="true" aria-label="${esc(title)}" tabindex="-1"><div class="modal-header"><h2>${esc(title)}</h2><button type="button" class="modal-close" aria-label="关闭弹窗">×</button></div><div class="modal-body"></div></div>`);
  $('.modal-body', box).appendChild(contentEl);
  const trigger = document.activeElement;
  let closing = false;
  const close = (afterClose) => {
    if (closing || contentEl.dataset.submitting === 'true' || !document.body.contains(bg)) return;
    closing = true;
    onClose?.();
    if (openSelect && box.contains(openSelect.wrapper)) openSelect.close();
    document.removeEventListener('keydown', onKey);
    bg.classList.add('closing');
    const finish = () => {
      if (!bg.isConnected) return;
      bg.remove();
      if (afterClose) afterClose();
      else if (!document.querySelector('.modal-bg') && trigger?.isConnected) trigger.focus();
    };
    bg.addEventListener('transitionend', (event) => { if (event.target === bg) finish(); });
    setTimeout(finish, 350);
  };
  const onKey = (e) => {
    if (options.externalFocus && !box.contains(e.target)) return;
    if (e.key === 'Escape' && !isImeComposing(e)) { e.preventDefault(); close(); return; }
    if (e.key !== 'Tab') return;
    const focusable = [...box.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])')].filter((el) => el.getClientRects().length);
    if (!focusable.length) { e.preventDefault(); box.focus(); return; }
    const first = focusable[0], last = focusable[focusable.length - 1];
    if (e.shiftKey && (document.activeElement === first || !box.contains(document.activeElement))) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && (document.activeElement === last || !box.contains(document.activeElement))) { e.preventDefault(); first.focus(); }
  };
  bg.appendChild(box);
  $('.modal-close', box).addEventListener('click', () => close());
  bg.addEventListener('click', (e) => { if (e.target === bg) close(); });
  document.addEventListener('keydown', onKey);
  document.body.appendChild(bg);
  contentEl.querySelectorAll('.kv').forEach(row => {
    const heading = row.querySelector(':scope > b'), control = row.querySelector('input:not([type="hidden"]), select, textarea');
    if (!heading || !control?.id) return;
    const label = document.createElement('label');
    label.htmlFor = control.id;
    label.textContent = heading.textContent;
    heading.replaceWith(label);
  });
  enhanceSelects(contentEl);
  (contentEl.querySelector('input:not(:disabled):not([type="hidden"]), textarea:not(:disabled), button:not(:disabled):not(.info-trigger)') || box).focus();
  return { close };
}

function confirmAction(title, message, confirmLabel = '删除', danger = true) {
  return new Promise((resolve) => {
    const form = h(`<div class="confirm-content"><p>${esc(message)}</p><div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="button" class="btn ${danger ? 'danger-solid' : 'primary'}" data-confirm>${esc(confirmLabel)}</button></div></div>`);
    const dialog = modal(title, form);
    let settled = false;
    const finish = (result) => { if (settled) return; settled = true; dialog.close(); resolve(result); };
    $('[data-cancel]', form).addEventListener('click', () => finish(false));
    $('[data-confirm]', form).addEventListener('click', () => finish(true));
    const observer = new MutationObserver(() => { if (!form.isConnected) { observer.disconnect(); finish(false); } });
    observer.observe(document.body, { childList: true });
  });
}

function pagehead(title, desc = '', actionsHtml = '') {
  return `<div class="pagehead fade-up">
    <div class="page-title"><h1 class="tt">${esc(title)}</h1>${desc ? infoTip(desc, title) : ''}</div>
    ${actionsHtml ? `<div class="acts">${actionsHtml}</div>` : ''}
  </div>`;
}

let hintSequence = 0;
function infoTip(text, label = '说明') {
  const id = `hint-${++hintSequence}`;
  return `<span class="info-tip"><button type="button" class="info-trigger" aria-label="${esc(label)}说明" aria-describedby="${id}">${icon('info', 14)}</button><span class="help-tooltip" id="${id}" role="tooltip">${esc(text)}</span></span>`;
}

let activeHint = null;
function closeHint() {
  if (activeHint?.matches(':popover-open')) activeHint.hidePopover();
  activeHint = null;
}
function showHint(trigger) {
  const tip = trigger.parentElement.querySelector('.help-tooltip');
  if (!tip || typeof tip.showPopover !== 'function' || activeHint === tip) return;
  closeHint();
  tip.setAttribute('popover', 'manual');
  tip.showPopover();
  activeHint = tip;
  const rect = trigger.getBoundingClientRect(), size = tip.getBoundingClientRect();
  tip.style.left = `${Math.max(12, Math.min(rect.left, innerWidth - size.width - 12))}px`;
  tip.style.top = `${rect.bottom + size.height + 8 < innerHeight ? rect.bottom + 6 : Math.max(12, rect.top - size.height - 6)}px`;
}
document.addEventListener('pointerover', e => { const trigger = e.target.closest('.info-trigger'); if (trigger) showHint(trigger); });
document.addEventListener('focusin', e => { if (e.target.matches('.info-trigger')) showHint(e.target); });
document.addEventListener('pointerout', e => {
  const wrapper = e.target.closest('.info-tip');
  if (wrapper && !wrapper.contains(e.relatedTarget) && !wrapper.contains(document.activeElement)) closeHint();
});
document.addEventListener('focusout', e => { if (e.target.matches('.info-trigger') && !e.target.parentElement.contains(e.relatedTarget)) closeHint(); });
document.addEventListener('scroll', closeHint, true);
document.addEventListener('pointerdown', e => { if (!e.target.closest('.info-tip')) closeHint(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape' && activeHint) { closeHint(); e.preventDefault(); e.stopImmediatePropagation(); } }, true);

function enhanceHints(root) {
  root.querySelectorAll('.settings-panel label.f').forEach(label => {
    const hint = label.querySelector(':scope > small:not([role="status"])');
    if (!hint || label.querySelector('.field-label')) return;
    const text = label.firstChild;
    if (text?.nodeType !== Node.TEXT_NODE || !text.textContent.trim()) return;
    const targetID = label.htmlFor || label.querySelector('input, select, textarea')?.id;
    if (!targetID) return;
    const controls = [...label.childNodes].filter(node => node !== text && node !== hint);
    const group = document.createElement('div');
    group.className = 'field-group f';
    const heading = document.createElement('span');
    heading.className = 'field-label';
    const tip = h(infoTip('', text.textContent.trim()));
    const placeholder = $('.help-tooltip', tip);
    hint.classList.add('help-tooltip');
    hint.setAttribute('role', 'tooltip');
    if (!hint.id) hint.id = placeholder.id;
    $('.info-trigger', tip).setAttribute('aria-describedby', hint.id);
    placeholder.replaceWith(hint);
    label.before(group);
    label.classList.remove('f');
    label.htmlFor = targetID;
    label.replaceChildren(text);
    heading.append(label, tip);
    group.append(heading, ...controls);
  });
}

function statcard(label, value, { tone = 'neutral', hint = '', icon: ic = '', description = '' } = {}) {
  return `<div class="statcard">
    <div class="head"><span class="t">${esc(label)}${description ? infoTip(description, label) : ''}</span>${ic ? `<span class="ico tone-${tone}">${icon(ic, 13)}</span>` : ''}</div>
    <div class="stat tone-${tone}">${value}</div>
    ${hint ? `<div class="d">${esc(hint)}</div>` : ''}
  </div>`;
}

function growBars(scope) {
  (scope || document).querySelectorAll('.bar:not(.grown)').forEach((bar) => {
    bar.classList.add('grown');
    const i = bar.firstElementChild;
    if (!i) return;
    const target = i.style.width;
    i.style.width = '0%';
    requestAnimationFrame(() => requestAnimationFrame(() => { i.style.width = target; }));
  });
}

function accountPagerMarkup(page, sizes) {
  return `<nav class="account-pagination" aria-label="账号列表分页"><span class="account-page-range">${page.start}–${page.end} / ${fmtInt(page.total)} 个账号</span><label class="account-page-size">每页<select data-account-size aria-label="每页账号数">${sizes.map(size => `<option value="${size}" ${page.pageSize === size ? 'selected' : ''}>${size} 条</option>`).join('')}</select></label><div class="account-page-controls"><button type="button" class="btn" data-account-page="1" aria-label="账号列表首页" ${page.page === 1 ? 'disabled' : ''}>«</button><button type="button" class="btn" data-account-page="${page.page - 1}" aria-label="账号列表上一页" ${page.page === 1 ? 'disabled' : ''}>‹</button><span>${page.page} / ${page.pages}</span><button type="button" class="btn" data-account-page="${page.page + 1}" aria-label="账号列表下一页" ${page.page === page.pages ? 'disabled' : ''}>›</button><button type="button" class="btn" data-account-page="${page.pages}" aria-label="账号列表末页" ${page.page === page.pages ? 'disabled' : ''}>»</button></div></nav>`;
}

function bindAccountPager(root, state, paint) {
  root.addEventListener('click', event => {
    const button = event.target.closest('[data-account-page]');
    if (!button || button.disabled) return;
    const name = button.getAttribute('aria-label');
    state.page = Number(button.dataset.accountPage); paint(); syncRoute();
    const next = [...root.querySelectorAll('[data-account-page]')].find(b => b.getAttribute('aria-label') === name && !b.disabled);
    (next || root.querySelector('.account-pagination .select-trigger'))?.focus({ preventScroll: true });
  });
  root.addEventListener('change', event => {
    if (!event.target.matches('[data-account-size]')) return;
    state.size = Number(event.target.value); state.page = 1; paint(); syncRoute();
    root.querySelector('.account-pagination .select-trigger')?.focus({ preventScroll: true });
  });
}

function accountNameMarkup(a, action = 'detail') {
  const name = a.platform_name || '未获取官方名称';
  const officialID = a.customer_id == null || a.customer_id === '' ? '未获取官方 ID' : String(a.customer_id);
  const family = a.family === 'zai' ? 'Z.ai' : a.family === 'bigmodel' ? 'BigModel' : 'ZCode';
  return `<button type="button" class="account-name-button" data-act="${action}" data-id="${esc(a.id)}" title="${esc(name)} · ${esc(officialID)}"><span class="avatar">${esc(Array.from(a.platform_name || '?')[0])}</span><span class="account-identity"><span class="account-row-name">${esc(name)}</span><span class="account-row-id">${esc(officialID)}</span></span><span class="account-row-family">${family}</span></button>`;
}

async function pageDashboard(main) {
  const hours = window._dashboardHours || 'all';
  const days = window._dashboardDays || 7;
  const d = await api('/api/dashboard?' + new URLSearchParams({ hours, days }));
  if (hours !== (window._dashboardHours || 'all') || days !== (window._dashboardDays || 7)) return;
  const usage = d.usage_period || d.usage_total || {};
  const fp = JSON.stringify([d.summary, d.busy_24h, d.cache_24h, d.cache_trend_24h?.byHour, d.daily_usage?.byHour, d.daily_usage?.byModel, usage, hours, days, Math.floor(Date.now() / 60000)]);
  if (main.dataset.fp === fp) return;
  main.dataset.fp = fp;
  const cached = Number(d.cache_24h?.cached_tokens) || 0, cacheInput = Number(d.cache_24h?.input_tokens) || 0;
  const cacheRate = cacheInput > 0 ? (cached / cacheInput * 100).toFixed(1) + '%' : '—';
  const busyPct = d.busy_24h.total ? (d.busy_24h.busy / d.busy_24h.total * 100).toFixed(1) + '%' : '—';
  main.innerHTML = `
    ${pagehead('仪表盘', '', `<span class="updated-at">${icon('refresh', 13)}${new Date().toLocaleTimeString('zh-CN', { hour12: false })}</span>`)}
    <section class="dashboard-overview" aria-label="额度与账号池概览">
      <div class="quota-hero">
        <div class="dashboard-usage-heading"><span class="eyebrow">总使用额度 ${infoTip('网关调用的输入与输出 Token 合计，按所选时间范围统计。', '使用额度')}</span><select id="dashboard-hours" class="round" aria-label="用量统计时间范围"><option value="all">全部时间</option><option value="24">近 24 小时</option><option value="168">近 7 天</option><option value="720">近 30 天</option></select></div>
        <div class="hero-number">${fmtInt(usage.total_tokens)}</div>
        <div class="dashboard-usage-breakdown"><div><span>输入</span><strong>${fmtInt(usage.input_tokens)}</strong></div><div><span>输出</span><strong>${fmtInt(usage.output_tokens)}</strong></div></div>
      </div>
      <div class="pool-health">
        <div class="section-heading"><h2>账号池状态</h2><span class="badge ${d.summary.activeCount ? 'ok' : 'err'}">${d.summary.activeCount ? '有可用账号' : '无可用账号'}</span></div>
        <div class="health-number">${d.summary.activeCount}<small>/ ${d.summary.count} 个账号可用</small></div>
        <button type="button" class="text-action" data-open-page="accounts">管理账号 ${icon('users', 14)}</button>
      </div>
    </section>
    <section class="dashboard-secondary" aria-label="额度与账号指标">
      ${statcard('剩余额度', `<span title="${esc(fmtInt(d.summary.totalRemaining))} Token">${fmtQuota(d.summary.totalRemaining)}<small> Token</small></span>`, { icon: 'layers', description: '账号上游有效额度的剩余余额合计。' })}
      ${statcard('24 小时缓存命中率', cacheRate, { icon: 'layers', hint: `${fmtQuota(cached)} / ${fmtQuota(cacheInput)} 输入 Token`, description: '近 24 小时缓存读取 Token ÷ 输入 Token；输入已包含缓存读取，不含缓存写入。' })}
      ${statcard('24 小时忙碌率', busyPct, { tone: Number.parseFloat(busyPct) >= 30 ? 'warning' : 'neutral', icon: 'gauge', hint: `${fmtInt(d.busy_24h.busy)} / ${fmtInt(d.busy_24h.total)} 次网关请求` })}
    </section>
    <section class="dashboard-chart-card"><header class="dashboard-chart-heading"><h2>近 24 小时缓存命中率趋势</h2>${infoTip('按服务本地整点汇总：缓存读取 Token ÷ 输入 Token。总 Token 为输入与输出之和。首尾为部分小时；无输入的时段不标注命中率。', '缓存趋势')}</header><div id="dashboard-cache" class="dashboard-chart"></div></section>
    <section class="dashboard-chart-card"><header class="dashboard-chart-heading"><h2>每日 Token 用量</h2><select id="dashboard-days" class="round" aria-label="每日用量时间范围"><option value="7">近 7 天</option><option value="30">近 30 天</option></select></header><div id="dashboard-daily" class="dashboard-chart"></div></section>`;

  const dailyRange = $('#dashboard-days', main);
  dailyRange.value = String(days);
  dailyRange.addEventListener('change', () => { window._dashboardDays = Number(dailyRange.value); delete main.dataset.fp; syncRoute(); render({ swap: true }); });
  renderCacheTrendChart($('#dashboard-cache', main), d.cache_trend_24h);
  renderDailyTokenChart($('#dashboard-daily', main), d.daily_usage);
  const range = $('#dashboard-hours', main);
  range.value = hours;
  range.addEventListener('change', () => {
    window._dashboardHours = range.value;
    delete main.dataset.fp;
    syncRoute();
    render({ swap: true });
  });
  main.querySelectorAll('[data-open-page]').forEach(button => button.addEventListener('click', () => {
    if (button.dataset.openPage === 'accounts') window._accFilter = { q: '', status: 'all', sort: 'default' };
    navigateTo(button.dataset.openPage);
  }));
  growBars(main);
}

window._accFilter = window._accFilter || { q: '', status: 'all', sort: 'default' };
const ACC_STATUS = { active: '可用', cooldown: '冷却中', needs_relogin: '需授权', unverified: '待验证', web_only: '仅网页', expired: '已过期' };

function filterAccounts(accounts, groups, filter) {
  const query = (filter.q || '').trim().toLowerCase();
  const result = accounts.filter(a => {
    if (filter.status === 'needs_relogin' ? !['needs_relogin', 'unverified'].includes(a.status) : filter.status !== 'all' && a.status !== filter.status) return false;
    return !query || `${a.label} ${a.platform_name || ''} ${a.customer_id || ''} ${(a.capabilities || []).join(' ')} ${groups[a.group_id] || ''}`.toLowerCase().includes(query);
  });
  if (filter.sort === 'remaining') result.sort((a, b) => (b.remaining ?? -1) - (a.remaining ?? -1));
  if (filter.sort === 'expiring') result.sort((a, b) => (a.card_expires_at || Infinity) - (b.card_expires_at || Infinity));
  return result;
}

function accountQuotaMarkup(account) {
  const buckets = account.quota_buckets || [], pendingBuckets = account.pending_buckets || [];
  const live = buckets.map(bucket => `<div class="quota-entry">
    <div class="quota-entry-top"><span>${esc(bucket.name)}</span><strong title="${esc(fmtInt(bucket.remaining))} Token">${fmtQuota(bucket.remaining)}</strong></div>
    <div class="quota-entry-meta"><span>总额 ${fmtQuota(bucket.total)}</span><span title="${esc(fmtTime(bucket.expires_at))}">${bucket.expires_at ? esc(fmtCompactTime(bucket.expires_at)) + ' 到期' : '到期时间未知'}</span></div>
  </div>`).join('');
  const pending = pendingBuckets.map(bucket => `<div class="quota-entry quota-entry-pending">
    <div class="quota-entry-top"><span>${esc(bucket.name)} <em>待生效</em></span><strong title="${esc(fmtInt(bucket.total))} Token">${fmtQuota(bucket.total)}</strong></div>
    <div class="quota-entry-meta"><span>已领取</span><span title="${esc(fmtTime(bucket.effective_at))}">${bucket.effective_at > Date.now() ? `${esc(fmtCompactTime(bucket.effective_at))} 生效` : '等待余额刷新'}</span></div>
  </div>`).join('');
  const remaining = account.remaining ?? (buckets.length ? buckets.reduce((sum, b) => sum + (Number(b.remaining) || 0), 0) : null);
  return `<div class="quota-summary"><strong title="${esc(fmtInt(remaining))} Token">${fmtQuota(remaining)}</strong><span>可用 Token</span>${pendingBuckets.length ? `<span class="badge">${pendingBuckets.length} 项待生效</span>` : ''}</div>
    ${live || pending ? `<details class="quota-details"><summary>查看 ${buckets.length + pendingBuckets.length} 项额度</summary><div class="quota-ledger">${live}${pending}</div></details>` : `<p class="quiet-message">${account.has_zcode_jwt ? account.last_balance ? '暂无生效额度' : '等待余额查询' : '网页账号不参与推理'}</p>`}`;
}

async function pageAccounts() {
  const [accounts, groups] = await Promise.all([api('/api/accounts'), api('/api/groups')]);
  window._accCache = accounts;
  renderAccPage(accounts, groups);
}
function renderAccPage(accounts, groups) {
  const main = $('#main');
  const gmap = Object.fromEntries(groups.map((g) => [g.id, g.name]));
  const f = window._accFilter;
  const statusCounts = {};
  for (const a of accounts) statusCounts[a.status] = (statusCounts[a.status] || 0) + 1;
  const attention = (statusCounts.needs_relogin || 0) + (statusCounts.unverified || 0);
  const remaining = accounts.reduce((sum, a) => sum + (Number(a.remaining) || 0), 0);
  const pending = accounts.reduce((sum, a) => sum + (Number(a.pending_total) || 0), 0);
  const pill = (v, label) => `<button type="button" data-st="${v}" aria-pressed="${f.status === v}" class="${f.status === v ? 'active' : ''}">${label}<span class="cnt">${v === 'all' ? accounts.length : v === 'needs_relogin' ? attention : statusCounts[v] || 0}</span></button>`;
  main.innerHTML = `
  ${pagehead('账号管理', '', `<button class="btn" id="accounts-refresh">${icon('refresh', 14)}刷新</button><button class="btn primary" id="add-btn">${icon('plus', 14)}添加账号</button>`)}
  <section class="overview-strip account-overview" aria-label="账号概览">
    <div><span>当前可用额度</span><strong title="${esc(fmtInt(remaining))} token">${fmtQuota(remaining)}<small> token</small></strong></div>
    <div><span>已领取 · 待生效</span><strong title="${esc(fmtInt(pending))} token">${fmtQuota(pending)}<small> token</small></strong></div>
    <div><span>账号状态</span><strong>${statusCounts.active || 0}<small> / ${accounts.length} 可用</small></strong>${attention ? `<p class="warn-text">${attention} 个账号需处理</p>` : ''}</div>
  </section>
  <div class="management-toolbar">
    <div class="searchbox">${icon('search', 14)}<input id="acc-q" aria-label="搜索账号" placeholder="搜索账号、ID、模型或分组" value="${esc(f.q)}"></div>
    <select id="acc-sort" class="round" aria-label="账号排序"><option value="default">默认顺序</option><option value="remaining">可用额度优先</option><option value="expiring">最近到期优先</option></select>
    <span id="acc-match-count" class="result-count" aria-live="polite"></span>
  </div>
  <nav class="status-pills account-filters" id="acc-status" aria-label="按账号状态筛选">${pill('all', '全部')}${pill('active', '可用')}${pill('needs_relogin', '需处理')}${pill('cooldown', '冷却中')}${pill('expired', '已过期')}${pill('web_only', '仅网页')}</nav>
  <section><div id="acc-table"></div></section>`;
  $('#add-btn').addEventListener('click', () => addAccountModal());
  $('#accounts-refresh').addEventListener('click', () => render({ swap: true }));
  $('#acc-q').addEventListener('input', (e) => { f.q = e.target.value; f.page = 1; renderAccTable(); syncRoute(); });
  $('#acc-sort').value = f.sort || 'default';
  $('#acc-sort').addEventListener('change', (e) => { f.sort = e.target.value; f.page = 1; renderAccTable(); syncRoute(); });
  $('#acc-status').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-st]');
    if (!b) return;
    f.status = b.dataset.st; f.page = 1;
    $$('#acc-status button').forEach((x) => { x.classList.toggle('active', x === b); x.setAttribute('aria-pressed', String(x === b)); });
    renderAccTable();
    syncRoute();
  });
  renderAccTable();
  bindAccountPager($('#acc-table'), f, renderAccTable);

  function renderAccTable() {
    const list = filterAccounts(accounts, gmap, f);
    $('#acc-match-count').textContent = `${list.length} / ${accounts.length} 个账号`;
    const tbl = $('#acc-table');
    if (!list.length) { f.page = 1; tbl.innerHTML = `<div class="management-empty">${icon('users', 24)}<h2>${accounts.length ? '没有匹配的账号' : '添加第一个账号'}</h2><p>${accounts.length ? '尝试其他关键词，或切换账号状态。' : '通过 OAuth 授权或导入本机凭据开始使用。'}</p></div>`; return; }

    const page = UI.paginate(list, f.page, f.size || 25);
    f.page = page.page;
    tbl.innerHTML = `<p class="account-scroll-hint">窄屏可左右滚动查看各列，点击账号名称查看完整信息。</p><div class="tablecard account-table compact-account-table" tabindex="0" role="region" aria-label="账号管理横向列表"><table><colgroup><col class="account-name-col"><col style="width:95px"><col style="width:150px"><col style="width:115px"><col style="width:145px"><col style="width:100px"><col style="width:138px"></colgroup><thead>
      <tr><th scope="col">账号</th><th scope="col">状态</th><th scope="col" class="align-right">可用额度 <span class="table-hint">Token</span></th><th scope="col" class="align-right" title="来自上游余额快照差额，可能包含外部客户端请求">24 小时消耗</th><th scope="col">最近到期</th><th scope="col">分组</th><th scope="col">操作</th></tr></thead><tbody>
      ${page.items.map(a => `<tr data-acc-id="${esc(a.id)}">
        <td>${accountNameMarkup(a)}</td>
        <td><span class="badge ${esc(a.status)}" title="${esc(a.last_error || '')}">● ${esc(ACC_STATUS[a.status] || a.status)}</span></td>
        <td class="align-right"><span class="account-row-quota"><strong title="${esc(fmtInt(a.remaining))} Token">${fmtQuota(a.remaining)}</strong><button type="button" class="text-action" data-act="detail" data-id="${esc(a.id)}" aria-label="查看${esc(a.platform_name || '未获取官方名称')}的额度明细">明细</button>${(a.pending_buckets || []).length ? `<span class="account-pending-dot" role="img" title="${fmtQuota(a.pending_total)} Token 待生效" aria-label="有额度待生效"></span>` : ''}</span></td>
        <td class="align-right" title="${esc(fmtInt(a.delta24h))} Token">${fmtQuota(a.delta24h)}</td>
        <td class="account-row-expiry" title="${esc(a.card_expires_at ? fmtTime(a.card_expires_at) + '，' + fmtPlanRemaining(a.card_expires_at - Date.now()) : '到期时间未知')}">${a.card_expires_at ? esc(fmtCompactTime(a.card_expires_at)) : '未知'}</td>
        <td><span class="account-row-group" title="${esc(gmap[a.group_id] || '待分组')}">${esc(gmap[a.group_id] || '待分组')}</span></td>
        <td><div class="account-action-row">
          <button class="ibtn" data-act="renew" data-id="${esc(a.id)}" aria-label="续期" title="更新账号凭据">${icon('refresh', 14)}</button>
          <button class="ibtn" data-act="verify" data-id="${esc(a.id)}" aria-label="验证连通性" title="验证连通性">${icon('zap', 14)}</button>
          <button class="ibtn" data-act="edit" data-id="${esc(a.id)}" aria-label="编辑账号" title="编辑账号">${icon('pencil', 14)}</button>
          <button class="ibtn account-delete" data-act="del" data-id="${esc(a.id)}" aria-label="删除账号" title="删除账号">${icon('trash', 14)}</button>
        </div></td></tr>`).join('')}</tbody></table></div>${accountPagerMarkup(page, [25, 50, 100])}`;
    enhanceSelects(tbl);
    tbl.onclick = onAccountAction;
  }
}

function btnLoading(btn, on) { btn.classList.toggle('loading', !!on); }

async function onAccountAction(e) {
  const btn = e.target.closest('button[data-act]');
  if (!btn || btn.disabled) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'copy') {
    try { await navigator.clipboard.writeText(id); toast('账号 ID 已复制', 'ok'); }
    catch { toast('复制失败，请手动选择账号 ID', 'err'); }
    return;
  }

  if (btn.dataset.act === 'detail') {
    const a = (window._accCache || []).find(item => item.id === id);
    if (!a) { toast('账号信息已更新，请刷新列表', 'err'); return; }
    const form = h(`<div class="account-detail"><dl><div><dt>账号名称</dt><dd>${esc(a.platform_name || '未获取官方名称')}</dd></div><div><dt>备注</dt><dd>${esc(a.label)}</dd></div><div><dt>账号 ID</dt><dd>${esc(a.customer_id || '未获取')}${a.customer_id ? `<button type="button" class="text-action" data-act="copy" data-id="${esc(a.customer_id)}">复制</button>` : ''}</dd></div><div><dt>状态</dt><dd>${esc(ACC_STATUS[a.status] || a.status)}</dd></div>${a.has_web_jwt ? `<div><dt>网页会话到期</dt><dd>${esc(fmtTime(a.web_cookie_expires_at))}</dd></div>` : ''}<div><dt>最近额度卡到期</dt><dd>${esc(fmtTime(a.card_expires_at))}</dd></div></dl>${a.last_error ? `<p class="inline-notice error">${esc(a.last_error)}</p>` : ''}<section class="account-detail-quota"><h3>模型额度明细</h3>${accountQuotaMarkup(a)}</section></div>`);
    const details = form.querySelector('.quota-details'); if (details) details.open = true;
    form.addEventListener('click', onAccountAction);
    modal('账号详情', form);
    return;
  }
  if (btn.dataset.act === 'del') {
    if (!await confirmAction('删除账号', '确定删除该账号？凭据与快照将一并清除。')) return;
    btn.disabled = true;
    try {
      const r = await api(`/api/accounts/${id}`, { method: 'DELETE' });
      toast(r.ok ? '已删除' : (r.error || '删除失败'), r.ok ? 'ok' : 'err');
      if (r.ok) render();
    } catch (err) { toast('删除失败：' + err.message, 'err'); btn.disabled = false; }
  } else if (btn.dataset.act === 'renew') {
    const accounts = await api('/api/accounts');
    const a = accounts.find((item) => item.id === id);
    if (!a) return toast('账号不存在', 'err');
    addAccountModal(a);
  } else if (btn.dataset.act === 'verify') {
    btn.disabled = true;
    btnLoading(btn, true);
    try {
      const r = await api(`/api/accounts/${id}/verify`, { method: 'POST' });
      toast(`验证：${r.status}${r.reason ? '（' + r.reason + '）' : ''}`, r.ok ? 'ok' : 'err');
      render();
    } catch (err) { toast('验证失败：' + err.message, 'err'); }
    btnLoading(btn, false);
    btn.disabled = false;
  } else if (btn.dataset.act === 'edit') {
    const [accounts, groups] = await Promise.all([api('/api/accounts'), api('/api/groups')]);
    const a = accounts.find((x) => x.id === id);
    const form = h(`<div>
      <div class="kv" style="margin-bottom:12px"><b>备注</b><input id="e-label" value="${esc(a.label)}"></div>
      <div class="kv" style="margin-bottom:12px"><b>分组</b><select id="e-group">
        <option value="">待分组</option>
        ${groups.map((g) => `<option value="${esc(g.id)}" ${a.group_id === g.id ? 'selected' : ''}>${esc(g.name)}</option>`).join('')}
      </select></div>
      <div class="kv" style="margin-bottom:6px"><b>出站代理</b><input id="e-proxy" placeholder="http://127.0.0.1:7890（留空跟随全局设置）" value="${esc(a.proxy_url || '')}"></div>
      <p class="muted" style="margin:0 0 12px">该账号全部出站（推理+平台轮询）走此代理；保存后对下一次 HTTP 请求生效。</p>
      <div class="row"><button class="btn primary" id="e-save">保存</button></div></div>`);
    const m = modal('编辑账号', form);
    $('#e-save', form).addEventListener('click', async () => {
      try {
        await api(`/api/accounts/${id}`, { method: 'PATCH', body: { label: $('#e-label', form).value, group_id: $('#e-group', form).value || null, proxy_url: $('#e-proxy', form).value.trim() || null } });
        toast('已保存', 'ok'); m.close(); render();
      } catch (err) { toast('保存失败：' + err.message, 'err'); }
    });
  }
}

function addAccountModal(target = null) {
  const form = h(`<div>
    <section class="import-option">
      <div class="section-heading"><h2>从客户端导入</h2>${infoTip('读取运行服务的机器上的 ZCode 客户端凭据，并验证账号。', '客户端导入')}</div>
      <div class="row"><button class="btn primary" id="i-local">导入账号</button></div>
    </section>
    <section class="import-option">
      <h2>账号授权</h2>
      <div class="row">
        <select id="i-provider" aria-label="账号平台"><option value="bigmodel">国内 BigModel</option><option value="zai">国际 Z.ai</option></select>
        <button class="btn" id="i-oauth">开始授权</button>
      </div>
      <div id="oauth-box"></div><div id="oauth-pending"></div>
    </section>
    <details class="import-advanced"><summary>导入网页会话</summary><div class="import-credentials">
      <p class="inline-notice">仅用于资料与活动查询，不可用于余额查询或推理。</p>
      <label class="dialog-field">网页 JWT<input id="i-cookie" autocomplete="off" spellcheck="false" placeholder="粘贴 bigmodel_token_production 的值"></label>
      <label class="dialog-field">会话到期时间（选填）<input id="i-cookie-expiry" type="datetime-local" title="按浏览器显示填写；留空时尝试读取 JWT 中的到期时间"></label>
      <div class="row"><button class="btn" id="i-cookie-btn">导入会话</button></div>
    </div></details></div>`);
  const m = modal(target ? '更新账号凭据：' + (target.platform_name || target.customer_id || '未获取官方名称') : '添加账号', form);
  if (target) {
    $('#i-local', form).closest('section').remove();
    $('#i-provider', form).value = target.family;
    $('#i-provider', form).disabled = true;
    syncSelect($('#i-provider', form));
    $('#i-cookie-btn', form).textContent = '更新网页会话';
    form.insertAdjacentHTML('afterbegin', '<p class="dialog-intro">重新授权将更新此账号凭据；验证不会延长上游会话有效期。</p>');
  }
  if (!target)
  $('#i-local', form).addEventListener('click', async (e) => {
    const btn = e.target.closest('button');
    btnLoading(btn, true);
    try {
      const r = await api('/api/accounts/import-local', { method: 'POST', body: {} });
      if (r.ok) { toast(`已导入 ${r.imported} 个账号`, 'ok'); m.close(); render(); }
      else toast(firstReason(r), 'err');
    } catch (err) { toast(err.message, 'err'); }
    btnLoading(btn, false);
  });
  const startOAuth = async () => {
    const btn = $('#i-oauth', form);
    btnLoading(btn, true);
    try {
      const r = await api('/api/accounts/oauth/start', { method: 'POST', body: { provider: $('#i-provider', form).value, account_id: target?.id } });
      showOAuthFlow(r);
    } catch (err) { toast(err.message, 'err'); }
    btnLoading(btn, false);
  };
  $('#i-oauth', form).addEventListener('click', startOAuth);
  let watchTimer;
  const stopWatch = () => { if (watchTimer) { clearInterval(watchTimer); watchTimer = null; } };
  function showOAuthFlow(r) {
    stopWatch();
    const box = $('#oauth-box', form);
    const expiresAt = Number(r.expires_at) || 0;
    box.innerHTML = `
      <div class="row" style="gap:8px;margin:0 0 10px">
        <button class="btn primary" id="oauth-open">${icon('plug', 14)}打开授权页面</button>
        <button class="btn" id="oauth-copy">复制链接</button>
      </div>
      <p class="muted" style="margin:0 0 12px;font-size:12px">登录成功后自动添加账号。链接有效期 <b id="oauth-expire" class="mono">${expiresAt ? fmtDur(expiresAt - Date.now()) : '—'}</b></p>
      <div class="row" style="gap:10px;align-items:center">
        <span class="muted" id="oauth-status" style="display:inline-flex;align-items:center;gap:6px"><span class="spinner"></span>等待授权…</span>
        <button class="btn" id="oauth-checknow" style="height:26px;padding:0 10px;font-size:12px">我已完成登录，立即检查</button>
      </div>`;
    $('#oauth-open', box).addEventListener('click', () => window.open(r.authorize_url, '_blank', 'noopener'));
    $('#oauth-copy', box).addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(r.authorize_url); toast('授权链接已复制', 'ok'); } catch { toast('复制失败，请从授权按钮的标签页地址栏手动复制', 'err'); }
    });
    $('#oauth-checknow', box).addEventListener('click', () => check());
    const setStage = (text, busy = true) => {
      const el = $('#oauth-status', box);
      if (el) el.innerHTML = `${busy ? '<span class="spinner"></span>' : ''}${esc(text)}`;
    };
    async function check() {
      if (!document.body.contains(box)) { stopWatch(); return; }
      try {
        const status = await api(`/api/accounts/oauth/status/${encodeURIComponent(r.flow_id)}`);
        if (!document.body.contains(box)) { stopWatch(); return; }
        if (status.status === 'done') return showOAuthDone(status);
        if (['failed', 'expired', 'cancelled'].includes(status.status)) return showOAuthFail(status);
        setStage(status.status === 'exchanging' ? '处理授权结果…' : (status.error || '等待授权…'));
        const exp = $('#oauth-expire', box);
        if (exp && expiresAt) exp.textContent = fmtDur(expiresAt - Date.now());
      } catch (err) { setStage('状态查询失败：' + err.message, false); }
    }
    function showOAuthDone(status) {
      stopWatch();
      const x = status.result || {};
      const alreadyToasted = window._oauthSeen.has(r.flow_id);
      noteFlowSeen(r.flow_id);
      const accId = status.account_id || x.id || null;
      box.innerHTML = `
        <div class="oauth-result ${x.verified ? 'ok' : 'warn'}">
          <p class="${x.verified ? 'ok-text' : 'error'}" style="margin:0 0 4px;font-weight:600">${x.verified ? '✓ 登录成功，账号已加入账号库' : '✓ 登录成功，但验证未通过'}</p>
          <p class="muted" style="margin:0 0 10px;font-size:12px">${x.updated ? '已有账号的凭据已更新' : '新账号已建档并开始额度轮询'}${x.verified ? '，balance 验证通过' : `，原因：${esc(String(x.reason || x.status || '未知'))}`}</p>
          <div class="row" style="gap:8px">
            <button class="btn primary" id="oauth-done">完成</button>
            ${!x.updated && accId ? '<button class="btn" id="oauth-view">查看账号</button>' : ''}
          </div>
        </div>`;
      if (!alreadyToasted) {
        toast(x.updated ? `账号凭据已更新${x.verified ? '，验证通过' : '，验证未通过'}` : (x.verified ? '登录成功，账号已加入账号库' : '账号已入库，但验证未通过'), x.verified ? 'ok' : 'err');
      }
      $('#oauth-done', box).addEventListener('click', () => { m.close(); render(); });
      $('#oauth-view', box)?.addEventListener('click', () => { m.close(); viewAccount(accId); });
    }
    function showOAuthFail(status) {
      stopWatch();
      noteFlowSeen(r.flow_id);
      const msg = status.error || { failed: '授权失败', expired: '授权链接已过期', cancelled: '授权已取消' }[status.status] || status.status;
      box.innerHTML = `
        <div class="oauth-result warn">
          <p class="error" style="margin:0 0 8px;font-weight:600">授权未完成：${esc(String(msg))}</p>
          <div class="row" style="gap:8px"><button class="btn" id="oauth-restart">重新开始授权</button></div>
        </div>`;
      $('#oauth-restart', box).addEventListener('click', startOAuth);
    }
    watchTimer = setInterval(check, 2000);
    check();
  }
  api('/api/accounts/oauth/pending').then((flows) => {
    const matching = flows.filter((x) => target ? x.target_id === target.id : !x.target_id);
    const pending = $('#oauth-pending', form);
    if (!matching.length) return;
    pending.innerHTML = matching.map((f, i) => `<button class="btn" data-flow-index="${i}">继续未完成的 ${esc(f.provider)} 授权</button>`).join(' ');
    pending.addEventListener('click', (e) => { const b = e.target.closest('[data-flow-index]'); if (b) showOAuthFlow(matching[Number(b.dataset.flowIndex)]); });
  }).catch(() => {});
  $('#i-cookie-btn', form).addEventListener('click', async (e) => {
    const btn = e.target.closest('button');
    btnLoading(btn, true);
    try {
      const r = await api(target ? `/api/accounts/${target.id}/refresh-cookie` : '/api/accounts/import-cookie', { method: 'POST', body: { webJwt: $('#i-cookie', form).value.trim(), family: $('#i-provider', form).value, expiresAt: $('#i-cookie-expiry', form).value ? new Date($('#i-cookie-expiry', form).value).getTime() : null } });
      if (r.ok) { toast(target ? '网页会话已更新' : '已导入（web_only 受限账号）', 'ok'); m.close(); render(); }
      else toast(r.error || r.reason || '导入失败', 'err');
    } catch (err) { toast(err.message, 'err'); }
    btnLoading(btn, false);
  });
}

function firstReason(r) {
  if (r.error) return r.error;
  if (r.results?.length) return r.results.map((x) => x.reason || (x.ok ? '成功' : '失败')).join('; ');
  return '导入失败';
}

window._oauthSeen = window._oauthSeen || new Set();
function noteFlowSeen(flowId) {
  if (!flowId || window._oauthSeen.has(flowId)) return;
  window._oauthSeen.add(flowId);
  api('/api/accounts/oauth/ack', { method: 'POST', body: { flow_ids: [flowId] } }).catch(() => {});
}
function startOauthWatcher() {
  if (window._oauthWatchTimer) clearInterval(window._oauthWatchTimer);
  window._oauthWatchTimer = setInterval(async () => {
    if (!TOKEN || document.hidden || document.querySelector('.modal-bg')) return;
    let w;
    try { w = await api('/api/accounts/oauth/watch'); } catch { return; }
    for (const f of w.notified || []) {
      if (window._oauthSeen.has(f.flow_id)) continue;
      noteFlowSeen(f.flow_id);
      const x = f.result || {};
      if (f.status === 'done') {
        toast((x.updated ? '账号凭据已更新' : '登录成功，新账号已加入账号库') + (x.verified ? '（验证通过）' : `（验证未通过：${x.reason || x.status || ''}）`), x.verified ? 'ok' : 'err');
        if (['dashboard', 'accounts'].includes(currentPage) && $('#main').dataset.loading !== '1') render({ swap: true }).catch(() => {});
      } else {
        toast(`${f.provider} 授权未完成：${f.error || f.status}`, 'err');
      }
    }
  }, 5000);
}

async function viewAccount(id) {
  const accounts = window._accCache || await api('/api/accounts');
  const account = accounts.find(item => item.id === id);
  window._highlightAccId = id;
  window._accFilter = { q: account?.customer_id == null ? '' : String(account.customer_id), status: 'all', sort: 'default' };
  if (currentPage === 'accounts') { syncRoute(); await render(); }
  else if (!await navigateTo('accounts')) return;
  const row = document.querySelector(`#acc-table tr[data-acc-id="${CSS.escape(id)}"]`);
  if (row) {
    row.classList.add('hl');
    row.scrollIntoView({ block: 'center' });
    setTimeout(() => row.classList.remove('hl'), 4000);
  }
}

function policyLabel(policy) {
  return ({ balanced: '均衡调度', expiring_first: '临期优先', remaining_desc: '剩余额度优先' })[policy] || policy || '未指定';
}

function renderUsageChart(root, stats, options = {}) {
  const timeline = UI.usageTimeline(stats), rows = timeline.rows;
  let selected = rows.findLastIndex(row => row.reqs > 0);
  if (selected < 0) selected = rows.length - 1;
  const renderChart = () => {
    const failed = !options.tokens && window._trendMetric === 'failed';
    const value = row => options.tokens ? row.tokens : failed ? Math.max(0, row.reqs - row.ok) : row.reqs;
    const max = Math.max(1, ...rows.map(value));
    const offset = Math.abs(timeline.offset), zone = `UTC${timeline.offset >= 0 ? '+' : '−'}${String(Math.floor(offset / 60)).padStart(2, '0')}:${String(offset % 60).padStart(2, '0')}`;
    const dateLabel = row => row.bucket.slice(5).replace('T', ' ').replace(':00:00', ':00');
    const description = row => options.tokens ? `${dateLabel(row)}${row.partial ? '（部分时段）' : ''} · ${fmtQuota(row.tokens)} Token` : `${dateLabel(row)}${row.partial ? '（部分时段）' : ''} · ${fmtInt(row.reqs)} 次请求 · ${fmtInt(Math.max(0, row.reqs - row.ok))} 次未成功 · ${fmtQuota(row.tokens)} Token`;
    const header = options.tokens ? '' : `<div class="usage-chart-head"><div><h3>${failed ? '未成功请求' : '请求量'}</h3><p>按${stats.time_unit === 'day' ? '天' : '小时'}统计 · ${zone}</p></div><label class="chart-metric">指标<select id="trend-metric" aria-label="趋势指标"><option value="requests">请求量</option><option value="failed">未成功请求</option></select></label></div>`;
    root.innerHTML = `${header}
      <div class="chart-frame"><div class="chart-scale" aria-hidden="true"><span>${options.tokens ? fmtQuota(max) : fmtInt(max)}</span><span>${options.tokens ? fmtQuota(Math.round(max / 2)) : fmtInt(Math.round(max / 2))}</span><span>0</span></div><div class="chart-plot" style="--chart-count:${rows.length}" role="group" aria-label="连续时间轴上的${options.tokens ? '每日 Token 用量' : failed ? '未成功请求量' : '请求量'}">${rows.map((row, i) => `<button type="button" class="chart-column${i === selected ? ' selected' : ''}${failed ? ' failed' : ''}" data-trend-index="${i}" tabindex="${i === selected ? '0' : '-1'}" aria-label="${esc(description(row))}" aria-pressed="${i === selected}"><span class="chart-bar" style="height:${value(row) / max * 100}%"></span><span class="chart-dot"></span></button>`).join('')}</div></div>
      <div class="chart-axis" aria-hidden="true"><span>${dateLabel(rows[0])}</span><span>${dateLabel(rows[Math.floor(rows.length / 2)])}</span><span>${dateLabel(rows.at(-1))}</span></div>
      <p class="chart-selection" role="status" aria-live="polite">${description(rows[selected])}</p>${options.tokens ? '' : `<p class="quiet-message">${rows.some(row => row.reqs) ? '' : '所选范围内暂无请求。'}无记录的时段按 0 次展示；范围首尾可能仅包含部分时段。悬停、聚焦或点击柱形查看详情。</p>`}`;
    if (!options.tokens) $('#trend-metric', root).value = failed ? 'failed' : 'requests';
    if (!options.tokens) $('#trend-metric', root).addEventListener('change', e => { window._trendMetric = e.target.value; renderChart(); enhanceSelects(root); $('#trend-metric', root)?.parentElement.querySelector('.select-trigger')?.focus(); });
    const show = event => {
      const button = event.target.closest('[data-trend-index]');
      if (!button) return;
      selected = Number(button.dataset.trendIndex);
      root.querySelectorAll('[data-trend-index]').forEach((b, i) => { b.classList.toggle('selected', i === selected); b.setAttribute('aria-pressed', String(i === selected)); b.tabIndex = i === selected ? 0 : -1; });
      $('.chart-selection', root).textContent = description(rows[selected]);
    };
    root.onmouseover = show; root.onfocusin = show; root.onclick = show;
    root.onkeydown = event => {
      if (!event.target.matches('[data-trend-index]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const index = event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, selected + (event.key === 'ArrowRight' ? 1 : -1)));
      root.querySelector(`[data-trend-index="${index}"]`)?.focus();
    };
  };
  renderChart();
}

async function pageUsage(main) {
  const s = await api('/api/usage/stats?hours=' + (window._usageHours || 24));
  const sum = (field) => s.byHour.reduce((total, row) => total + (Number(row[field]) || 0), 0);
  const requests = sum('reqs'), success = sum('ok'), input = sum('tin'), output = sum('tout');
  const categories = {
    model: { label: '模型', columns: ['模型', '请求数', 'Token 用量'], rows: s.byModel.map((r) => `<tr><td>${esc(r.model || '未知')}</td><td>${fmtInt(r.reqs)}</td><td class="mono">${fmtInt((r.tin || 0) + (r.tout || 0))}</td></tr>`) },
    key: { label: '密钥', columns: ['密钥', '请求数', 'Token 用量'], rows: s.byKey.map((r) => `<tr><td>${esc(r.name || r.key_id || '匿名')}</td><td>${fmtInt(r.reqs)}</td><td class="mono">${fmtInt(r.tokens || 0)}</td></tr>`) },
    account: { label: '账号', columns: ['账号', '请求数', '成功', '忙碌', 'Token 用量'], rows: s.byAccount.map((r) => `<tr><td>${esc(r.label || r.account_id || '未分配')}</td><td>${fmtInt(r.reqs)}</td><td>${fmtInt(r.ok || 0)}</td><td>${fmtInt(r.busy || 0)}</td><td class="mono">${fmtInt(r.tokens || 0)}</td></tr>`) },
    status: { label: '状态', columns: ['状态', '次数'], rows: s.byStatus.map((r) => `<tr><td>${esc(({ success: '成功', busy: '忙碌', error: '失败' })[r.status] || r.status || '未知')}</td><td>${fmtInt(r.n)}</td></tr>`) },
  };
  const detailState = window._usageDetail || (window._usageDetail = { tab: 'model', pages: {} });
  if (!categories[detailState.tab]) detailState.tab = 'model';
  main.innerHTML = `
  ${pagehead('用量统计', '统计本项目网关的请求记录，不包含客户端直接发起的请求。',
    `<select id="u-hours" class="round" aria-label="统计时间范围"><option value="24">近 24 小时</option><option value="168">近 7 天</option><option value="720">近 30 天</option></select><button class="btn" id="usage-refresh">${icon('refresh', 14)}刷新</button>`)}
  <section><div class="cards multicol usage-summary">
    ${statcard('请求数', fmtInt(requests), { tone: 'neutral', icon: 'activity' })}
    ${statcard('成功率', requests ? `${(success / requests * 100).toFixed(1)}%` : '—', { tone: 'success', icon: 'shield', hint: `${fmtInt(success)} 次成功` })}
    ${statcard('Token 用量', fmtInt(input + output), { tone: 'neutral', icon: 'zap', hint: `输入 ${fmtInt(input)} · 输出 ${fmtInt(output)}` })}
    ${statcard('首 Token 时间 P50 / P95', s.ttft ? `${fmtInt(s.ttft.p50)}<small> / ${fmtInt(s.ttft.p95)} ms</small>` : '—', { icon: 'timer', hint: '仅统计成功请求' })}
  </div></section>
  <section><div class="usage-heading"><h2>${icon('trending', 15)}请求趋势</h2></div><div id="usage-chart" class="usage-chart"></div></section>
  <section><h2>${icon('layers', 15)} 用量明细</h2><div id="usage-detail"></div></section>`;
  renderUsageChart($('#usage-chart', main), s);
  const detail = $('#usage-detail', main);
  const renderDetail = (focusTab = false) => {
    const { tab, pages } = detailState;
    const current = categories[tab];
    const total = current.rows.length, pageCount = Math.max(1, Math.ceil(total / 10));
    const page = Math.max(1, Math.min(pages[tab] || 1, pageCount));
    pages[tab] = page;
    const rows = current.rows.slice((page - 1) * 10, page * 10);
    const pageNumbers = [];
    for (let i = 1; i <= pageCount; i++) {
      if (i === 1 || i === pageCount || Math.abs(i - page) <= 2) pageNumbers.push(i);
      else if (pageNumbers.at(-1) !== '…') pageNumbers.push('…');
    }
    detail.innerHTML = `<nav class="section-tabs usage-tabs" role="tablist" aria-label="用量明细分类">${Object.entries(categories).map(([id, group]) => `<button type="button" class="tab${id === tab ? ' active' : ''}" role="tab" id="usage-tab-${id}" aria-selected="${id === tab}" aria-controls="usage-detail-panel" tabindex="${id === tab ? '0' : '-1'}" data-usage-tab="${id}">${group.label}<span class="cnt">${fmtInt(group.rows.length)}</span></button>`).join('')}</nav>
      <div class="usage-panel" role="tabpanel" id="usage-detail-panel" aria-labelledby="usage-tab-${tab}">
        ${rows.length ? `<div class="tablecard"><table><thead><tr>${current.columns.map((col) => `<th>${col}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>` : '<p class="usage-empty">所选分类暂无数据</p>'}
        <div class="pager usage-pager" aria-label="用量明细分页"><button type="button" data-usage-page="prev" aria-label="上一页" ${page === 1 ? 'disabled' : ''}>‹</button>${pageNumbers.map((n) => n === '…' ? '<span aria-hidden="true">…</span>' : `<button type="button" data-usage-page="${n}" aria-label="第 ${n} 页" ${n === page ? 'class="cur" aria-current="page"' : ''}>${n}</button>`).join('')}<button type="button" data-usage-page="next" aria-label="下一页" ${page === pageCount ? 'disabled' : ''}>›</button><span class="pinfo">共 ${fmtInt(total)} 条</span></div>
      </div>`;
    enhanceTables(detail);
    syncRoute();
    if (focusTab) detail.querySelector(`[data-usage-tab="${tab}"]`)?.focus();
  };
  detail.addEventListener('click', (e) => {
    const tab = e.target.closest('[data-usage-tab]');
    if (tab && tab.dataset.usageTab !== detailState.tab) { detailState.tab = tab.dataset.usageTab; renderDetail(true); return; }
    const button = e.target.closest('[data-usage-page]');
    if (button && !button.disabled) {
      const action = button.dataset.usagePage;
      detailState.pages[detailState.tab] = action === 'next' ? detailState.pages[detailState.tab] + 1 : action === 'prev' ? detailState.pages[detailState.tab] - 1 : Number(action);
      renderDetail();
      detail.querySelector(`[data-usage-page="${detailState.pages[detailState.tab]}"]`)?.focus();
    }
  });
  detail.addEventListener('keydown', (e) => {
    if (!e.target.matches('[data-usage-tab]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    const ids = Object.keys(categories), index = ids.indexOf(detailState.tab);
    detailState.tab = e.key === 'Home' ? ids[0] : e.key === 'End' ? ids.at(-1) : ids[(index + (e.key === 'ArrowRight' ? 1 : -1) + ids.length) % ids.length];
    renderDetail(true);
  });
  renderDetail();
  $('#u-hours').value = String(window._usageHours || 24);
  $('#u-hours').addEventListener('change', (e) => { window._usageHours = Number(e.target.value); detailState.pages = {}; render(); });
  $('#usage-refresh').addEventListener('click', () => render());
}

async function pageKeys(main) {
  const keys = await api('/api/keys');
  main.innerHTML = `
  ${pagehead('API 密钥', '',
    `<button class="btn primary" id="add-key">${icon('plus', 14)}新建密钥</button>`)}
  <div class="integration-note"><div class="example-line"><code>Authorization: Bearer &lt;密钥&gt;</code> → <code>POST http://127.0.0.1:${RUNTIME?.gateway_port || 6780}/v1/chat/completions</code>（OpenAI）或 <code>/v1/messages</code>（Anthropic）<button type="button" class="text-action" id="copy-gw">复制网关地址</button></div></div>
  <section style="animation-delay:50ms"><div id="key-table" class="fade-up"></div></section>`;
  $('#key-table').innerHTML = keys.length ? `<div class="tablecard"><table>
    <tr><th>名称</th><th>尾号</th><th>配额</th><th>已用</th><th>分组链</th><th>默认档位</th><th>白名单</th><th>状态</th><th>操作</th></tr>
    ${keys.map((k) => `<tr>
      <td><b style="font-size:13px">${esc(k.name)}</b><br><span class="muted" style="font-size:11px">${fmtTime(k.created_at)}</span></td>
      <td class="mono">…${esc(k.key_tail)}</td>
      <td class="mono">${k.quota_limit === null ? '不限' : fmtInt(k.quota_limit)}</td>
      <td class="mono">${fmtInt(k.used_units)}</td>
      <td style="font-size:12px">${esc((k.group_order || []).join(' → ') || '默认链')}</td>
      <td>${k.default_level ? `<span class="badge">${esc(({ low: '低', high: '高', max: '最高' })[k.default_level] || k.default_level)}</span>` : '<span class="muted">跟随全局</span>'}</td>
      <td style="font-size:11px">${k.model_whitelist.length ? k.model_whitelist.map((m) => `<span class="badge sm">${esc(m)}</span>`).join(' ') : '全部模型'}${k.ip_whitelist.length ? '<br>IP: ' + esc(k.ip_whitelist.join(',')) : ''}</td>
      <td>${k.enabled ? '<span class="ok-text">● 启用</span>' : '<span class="muted">禁用</span>'}</td>
      <td><div class="row" style="gap:6px;justify-content:flex-end"><button class="btn" style="height:28px;padding:0 10px;font-size:12px" data-edit="${esc(k.id)}">编辑</button><button class="btn danger" style="height:28px;padding:0 10px;font-size:12px" data-del="${esc(k.id)}">删除</button></div></td></tr>`).join('')}
  </table></div>` : '<p class="muted">还没有密钥。</p>';
  $('#key-table').addEventListener('click', async (e) => {
    const del = e.target.closest('button[data-del]');
    const edit = e.target.closest('button[data-edit]');    if (del) {
      if (!await confirmAction('删除密钥', '确定删除该密钥？使用此密钥的客户端将无法继续访问。')) return;
      try {
        await api(`/api/keys/${del.dataset.del}`, { method: 'DELETE' });
        toast('已删除', 'ok');
        render();
      } catch (err) { toast('删除失败：' + err.message, 'err'); }
    } else if (edit) {
      const k = keys.find((x) => x.id === edit.dataset.edit);
      const form = h(`<div>
        <div class="kv"><b>名称</b><input id="k-name" value="${esc(k.name)}"></div>
        <div class="kv"><b>配额(总token)</b><input id="k-quota" type="number" value="${k.quota_limit ?? ''}" placeholder="留空不限"></div>
        <div class="kv"><b>分组链</b><input id="k-chain" value="${esc((k.group_order || []).join(','))}" placeholder="g_xxx,g_yyy（逗号分隔，空=默认链）"></div>
        <div class="kv"><b>默认思考档位</b><select id="k-level">
          <option value="" ${!k.default_level ? 'selected' : ''}>跟随全局设置</option>
          <option value="low" ${k.default_level === 'low' ? 'selected' : ''}>low（最快）</option>
          <option value="high" ${k.default_level === 'high' ? 'selected' : ''}>high（深思）</option>
          <option value="max" ${k.default_level === 'max' ? 'selected' : ''}>max</option>
        </select></div>
        <div class="kv"><b>模型白名单</b><input id="k-models" value="${esc(k.model_whitelist.join(','))}" placeholder="逗号分隔，空=全部"></div>
        <div class="kv"><b>IP 白名单</b><input id="k-ips" value="${esc(k.ip_whitelist.join(','))}" placeholder="逗号分隔，支持 1.2.3.*，空=不限"></div>
        <div class="kv"><b>黏住分钟</b><input id="k-sticky" type="number" value="${k.sticky_minutes}"></div>
        <div class="kv"><b>启用</b><input id="k-enabled" type="checkbox" ${k.enabled ? 'checked' : ''}></div>
        <div class="row" style="margin-top:12px"><button class="btn primary" id="k-save">保存</button></div></div>`);
      const m = modal('编辑密钥', form);
      $('#k-save', form).addEventListener('click', async () => {
        const chain = $('#k-chain', form).value.split(',').map((s) => s.trim()).filter(Boolean);
        try {
          await api(`/api/keys/${k.id}`, { method: 'PATCH', body: {
            name: $('#k-name', form).value,
            quota_limit: $('#k-quota', form).value === '' ? null : Number($('#k-quota', form).value),
            group_order: chain.length ? chain : null,
            default_level: $('#k-level', form).value || null,
            model_whitelist: $('#k-models', form).value.split(',').map((s) => s.trim()).filter(Boolean),
            ip_whitelist: $('#k-ips', form).value.split(',').map((s) => s.trim()).filter(Boolean),
            sticky_minutes: Number($('#k-sticky', form).value) || 0,
            enabled: $('#k-enabled', form).checked,
          } });
          toast('已保存', 'ok'); m.close(); render();
        } catch (err) { toast('保存失败：' + err.message, 'err'); }
      });
    }
  });
  $('#copy-gw').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(`http://127.0.0.1:${RUNTIME?.gateway_port}/v1`);
      toast('网关地址已复制', 'ok');
    } catch { toast('复制失败，请手动复制', 'err'); }
  });
  $('#add-key').addEventListener('click', () => {
    const form = h(`<form class="dialog-form"><p class="dialog-intro">为使用网关的客户端创建独立密钥，方便管理用量与访问权限。</p><label class="dialog-field">密钥名称<input name="name" maxlength="80" placeholder="例如：Claude Code" required></label><p class="dialog-error error" role="alert"></p><div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary">创建密钥</button></div></form>`);
    const m = modal('新建密钥', form);
    $('[data-cancel]', form).addEventListener('click', () => m.close());
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const name = form.elements.name.value.trim();
      if (!name) { $('.dialog-error', form).textContent = '请输入密钥名称。'; return; }
      const btn = $('button[type=submit]', form);
      if (btn.disabled) return;
      btn.disabled = true;
      form.dataset.submitting = 'true';
      btnLoading(btn, true);
      try {
        const r = await api('/api/keys', { method: 'POST', body: { name } });
        render();
        delete form.dataset.submitting;
        m.close(() => {
          const result = h(`<div class="secret-result"><p class="dialog-intro">密钥只显示一次，请立即复制并妥善保存。</p><pre class="log">${esc(r.secret)}</pre><div class="modal-actions"><button type="button" class="btn primary" data-copy>复制密钥</button></div></div>`);
          modal('密钥已创建', result);
          $('[data-copy]', result).addEventListener('click', async () => {
            try { await navigator.clipboard.writeText(r.secret); toast('已复制', 'ok'); }
            catch { toast('复制失败，请手动选择并复制密钥', 'err'); }
          });
        });
      } catch (err) { $('.dialog-error', form).textContent = '创建失败：' + err.message; }
      finally { delete form.dataset.submitting; btn.disabled = false; btnLoading(btn, false); }
    });
  });
}

async function pageGroups(main) {
  const groups = await api('/api/groups');
  main.innerHTML = `
  ${pagehead('分组调度链', '按分组顺序选择账号池，再根据各组策略选择账号。',
    `<button class="btn primary" id="add-group">${icon('plus', 14)}新建分组</button>`)}
  <section id="group-list" style="animation-delay:50ms">
  ${groups.length ? `<div class="tablecard fade-up"><table><tr><th>名称</th><th>角色</th><th>账号类型</th><th>调度策略</th><th>临期窗口</th><th>操作</th></tr>
    ${groups.map((g) => `<tr><td><div class="cell-name"><span class="avatar">${esc((g.name || '?').slice(0, 1))}</span><span class="nm"><b>${esc(g.name)}</b></span></div></td>
      <td><span class="badge ${g.role === 'primary' ? 'active' : ''}">${esc(({ primary: '主力', backup: '备用' })[g.role] || g.role)}</span></td><td>${esc(({ zai: 'Z.ai', bigmodel: 'BigModel' })[g.family_filter] || g.family_filter || '全部类型')}</td>
      <td>${esc(policyLabel(g.policy))}</td><td>${g.urgent_window_min} 分钟</td>
      <td><div class="row" style="justify-content:flex-end"><button class="btn danger" style="height:28px;padding:0 10px;font-size:12px" data-del="${esc(g.id)}">删除</button></div></td></tr>`).join('')}</table></div>`
    : '<p class="muted">暂无分组。</p>'}</section>`;
  $('#add-group').addEventListener('click', () => {
    const form = h(`<form class="dialog-form"><p class="dialog-intro">设置账号池的角色与调度顺序，创建后可将账号加入分组。</p><label class="dialog-field">分组名称<input name="name" maxlength="80" placeholder="例如：默认池" required></label><div class="dialog-grid"><label class="dialog-field">角色<select name="role"><option value="primary">主力</option><option value="backup">备用</option></select></label><label class="dialog-field">账号类型<select name="family"><option value="">全部类型</option><option value="bigmodel">国内 BigModel</option><option value="zai">国际 Z.ai</option></select></label></div><label class="dialog-field">调度策略<select name="policy"><option value="balanced">均衡调度</option><option value="expiring_first">临期优先</option><option value="remaining_desc">剩余额度优先</option></select></label><p class="dialog-error error" role="alert"></p><div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary">创建分组</button></div></form>`);
    const m = modal('新建分组', form);
    $('[data-cancel]', form).addEventListener('click', () => m.close());
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const name = form.elements.name.value.trim();
      if (!name) { $('.dialog-error', form).textContent = '请输入分组名称。'; return; }
      const btn = $('button[type=submit]', form);
      if (btn.disabled) return;
      btn.disabled = true;
      form.dataset.submitting = 'true';
      btnLoading(btn, true);
      try {
        const r = await api('/api/groups', { method: 'POST', body: { name, role: form.elements.role.value, policy: form.elements.policy.value, family_filter: form.elements.family.value || null } });
        if (!r.ok) throw new Error(r.error || '创建失败');
        delete form.dataset.submitting;
        m.close(); toast('分组已创建', 'ok'); render();
      } catch (err) { $('.dialog-error', form).textContent = '创建失败：' + err.message; }
      finally { delete form.dataset.submitting; btn.disabled = false; btnLoading(btn, false); }
    });
  });
  $('#group-list').addEventListener('click', async (e) => {
    const del = e.target.closest('button[data-del]');
    if (!del) return;
    if (!await confirmAction('删除分组', '确定删除该分组？分组内仍有账号时无法删除。')) return;
    try {
      const r = await api(`/api/groups/${del.dataset.del}`, { method: 'DELETE' });
      toast(r.ok ? '已删除' : r.error, r.ok ? 'ok' : 'err');
      render();
    } catch (err) { toast('删除失败：' + err.message, 'err'); }
  });
}

async function pageSettings(main) {
  const [s, mihomo] = await Promise.all([api('/api/settings'), api('/api/settings/proxy/mihomo').catch(() => ({ unavailable: true }))]);
  const [mihomoOS, mihomoArch] = (mihomo.platform || '').split('/');
  const mihomoTarget = mihomo.platform ? `${({ windows: 'Windows', linux: 'Linux', darwin: 'macOS' })[mihomoOS] || mihomoOS} / ${({ amd64: 'x64', arm64: 'ARM64' })[mihomoArch] || mihomoArch}` : '';
  const httpParams = { ...s.http };
  const versionIntervalSeconds = Number(s.zcode_version_check_interval_sec) || 432000;
  const versionIntervalUnit = versionIntervalSeconds % 86400 === 0 ? 86400 : versionIntervalSeconds % 3600 === 0 ? 3600 : 60;
  const captcha = { mode: 'off', node_path: 'node', jsdom_path: '', simulate_canvas_webgl: true, timeout_ms: 30000, ticket_ttl_ms: 75000, ...s.http.captcha };
  const proxy = { enabled: false, source: 'manual', urls: [], subscription_url: '', preferred_node: '', mihomo_path: 'mihomo', refresh_interval_sec: 3600, ...s.outbound_proxy };
  delete httpParams.client_version;
  delete httpParams.captcha;
  main.innerHTML = `${pagehead('设置', '',
    '<button type="submit" form="settings-form" class="btn primary settings-save" id="s-save" aria-describedby="settings-state" disabled>保存设置</button>')}
    <p id="settings-state" class="visually-hidden" role="status" aria-live="polite"></p>
    <form id="settings-form" class="settings-form" novalidate>
      <p id="settings-error" class="inline-notice error hidden" role="alert"></p>
      ${settingsUpdateMarkup()}
      <details class="settings-section formcard" id="poll-panel"><summary class="settings-section-summary"><span><strong>额度轮询</strong></span></summary>
      <div class="settings-panel settings-section-body settings-section-content"><div class="settings-fields">
        <label class="f">轮询间隔（秒）<input id="s-poll" type="number" min="1" step="1" required value="${s.poll_interval_sec}"><small>两次余额检查之间的基础间隔</small></label>
        <label class="f">随机抖动（秒）<input id="s-jitter" type="number" min="0" step="1" required value="${s.poll_jitter_sec}"><small>分散多个账号的请求时间</small></label>
        <label class="f">轮询并发<input id="s-conc" type="number" min="1" step="1" required value="${s.poll_concurrency}"><small>同时检查余额的账号数量</small></label>
      </div></div></details>
      <details class="settings-section formcard" id="captcha-panel"><summary class="settings-section-summary"><span><strong>上游安全验证</strong></span></summary>
      <div class="settings-panel settings-section-body settings-section-content"><div class="network-settings-fields">
        <div class="settings-fields"><label class="f">推理验证策略<select id="s-captcha-mode" aria-describedby="s-captcha-mode-hint"><option value="off">关闭推理验证</option><option value="auto">按上游要求验证</option><option value="always">每次推理前验证</option></select><small id="s-captcha-mode-hint" aria-live="polite"></small></label>
          <label class="f">验证超时（毫秒）<input id="s-captcha-timeout" type="number" min="5000" max="120000" step="1" required value="${captcha.timeout_ms}"></label>
          <label class="f">预取票据有效期（毫秒）<input id="s-captcha-ttl" type="number" min="0" max="90000" step="1" required value="${captcha.ticket_ttl_ms}"><small>连续验证成功后按需预取；0 表示关闭预取</small></label></div>
        <label class="settings-check"><input id="s-captcha-canvas" type="checkbox" ${captcha.simulate_canvas_webgl ? 'checked' : ''}><span>启用浏览器接口模拟<small>为 SDK 补充 Canvas、WebGL 等兼容接口；不保证通过验证。</small></span></label>
        <div class="settings-subsection"><div class="settings-fields">
          <label class="f">Node.js 可执行文件<input id="s-captcha-node" value="${esc(captcha.node_path)}" placeholder="node"><small id="s-node-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></label>
          <label class="f">jsdom 所在目录<input id="s-captcha-jsdom" value="${esc(captcha.jsdom_path)}" placeholder="留空使用程序根目录下的 captcha_node"><small id="s-jsdom-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></label>
        </div></div>
      </div></div></details>
      <details class="settings-section formcard" id="proxy-panel"><summary class="settings-section-summary"><span><strong>出站代理</strong></span><span class="pill" id="s-proxy-summary">${proxy.enabled ? '已启用' : '未启用'}</span></summary>
      <div class="settings-panel settings-section-body"><div class="network-settings-fields">
        <div class="settings-fields"><label class="settings-check"><input id="s-proxy-enabled" type="checkbox" ${proxy.enabled ? 'checked' : ''}><span>启用全局代理<small>关闭后全局使用直连</small></span></label>
          <label class="f">代理来源<select id="s-proxy-source"><option value="manual">HTTP / HTTPS 地址列表</option><option value="subscription">节点订阅（需要 Mihomo）</option></select></label></div>
        <div id="proxy-manual-fields"><label class="f">代理地址（每行一个）<textarea id="s-proxy-urls" class="code-input" rows="3" spellcheck="false" placeholder="http://127.0.0.1:7890&#10;http://user:password@proxy.example:8080">${esc(proxy.urls.join('\n'))}</textarea><small>仅支持 HTTP/HTTPS 代理；订阅链接请切换到“节点订阅”。</small></label></div>
        <div id="proxy-subscription-fields" class="network-settings-fields">
          <label class="f">HTTPS 订阅地址<input id="s-proxy-subscription" type="text" autocomplete="off" spellcheck="false" value="${esc(proxy.subscription_url)}" placeholder="https://provider.example/subscribe?token=…"><small>支持 VLESS TCP/WS、Hysteria2、HTTP/HTTPS，最多 64 个节点。</small></label>
          <div class="settings-fields"><div class="mihomo-field"><div class="mihomo-field-heading"><label for="s-proxy-mihomo">Mihomo 可执行文件</label><span class="mihomo-field-actions"><a class="text-action" href="https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.33" target="_blank" rel="noopener noreferrer">手动下载</a><button type="button" class="btn proxy-small-button" id="s-mihomo-install" ${mihomo.installed || mihomo.supported === false ? 'disabled' : ''} title="${esc(mihomoTarget ? `安装到服务端 ${mihomoTarget}` : '安装 Mihomo')}">${mihomo.installed ? '已安装' : '安装 Mihomo'}</button></span></div><input id="s-proxy-mihomo" value="${esc(proxy.mihomo_path)}" placeholder="mihomo"><small id="s-mihomo-status" role="status" aria-live="polite">${mihomo.installed ? '已检测到可执行文件' : mihomo.supported === false ? '当前平台请手动安装 Mihomo' : mihomo.unavailable ? '暂时无法检测安装状态' : '可填写已有路径或点击安装'}</small></div>
            <label class="f"><span class="proxy-field-heading">订阅刷新间隔（秒）</span><input id="s-proxy-refresh" type="number" min="60" max="86400" step="1" value="${proxy.refresh_interval_sec}"></label></div>
        </div>
        <input id="s-proxy-node" type="hidden" value="${esc(proxy.preferred_node)}">
        <section class="proxy-node-box" id="proxy-node-box" aria-labelledby="s-proxy-nodes-heading"><div class="proxy-node-heading"><h3 id="s-proxy-nodes-heading">代理节点</h3><span id="s-proxy-node-count">未加载</span></div><div class="proxy-node-content">
          <div class="proxy-test-actions"><small id="s-proxy-node-status" role="status" aria-live="polite">加载节点后可指定代理，选择后保存生效。</small><button type="button" class="btn proxy-small-button" id="s-proxy-load">加载节点</button></div>
          <div id="s-proxy-nodes" class="proxy-node-list" role="radiogroup" aria-label="使用的代理节点"></div>
        </div></section>
        <div class="proxy-test-actions"><button type="button" class="btn" id="s-proxy-test">检测当前代理配置</button></div>
        <div id="s-proxy-result" class="proxy-test-results hidden" role="status" aria-live="polite"></div>
      </div></div></details>
      <details class="settings-section formcard" id="gateway-panel"><summary class="settings-section-summary"><span><strong>网关与模型</strong></span></summary>
      <div class="settings-panel settings-section-body settings-section-content"><div class="network-settings-fields">
        <label class="f">默认思考强度<select id="s-level" required aria-describedby="s-level-hint"><option value="low">低（low）</option><option value="high">高（high）</option><option value="max">最高（max）</option></select><small id="s-level-hint">优先级：客户端 → 密钥 → 此处设置。低强度仍开启思考；客户端关闭思考时不覆盖。始终思考的模型可能返回 1210，请在客户端开启思考。</small></label>
        <div class="settings-subsection"><div class="advanced-fields">
          <div class="settings-fields settings-version-schedule">
            <div class="settings-version-actions">
              <label class="settings-check" title="按设置间隔检查官方稳定版，同时更新推理、额度与领卡版本；获取失败时保留当前版本。关闭后可手动设置。"><input id="s-zcode-version-auto" type="checkbox" ${s.zcode_version_auto_update ? 'checked' : ''}><span>自动跟随 ZCode 稳定版</span></label>
              <button type="button" class="btn proxy-small-button" id="s-zcode-version-sync">立即同步</button>
            </div>
            <div class="field-group f"><label for="s-zcode-version-interval">版本检查间隔</label><div class="settings-version-interval" title="从上次检查时间起计算，保存后立即生效；重启不会重复检查。"><input id="s-zcode-version-interval" type="number" min="1" step="any" required value="${versionIntervalSeconds / versionIntervalUnit}"><select id="s-zcode-version-unit" aria-label="版本检查间隔单位"><option value="60">分钟</option><option value="3600">小时</option><option value="86400">天</option></select></div></div>
          </div>
          <div class="settings-fields">
            <label class="f">额度与领卡 API 版本<input id="s-ver" value="${esc(s.app_version)}" pattern="[0-9]+\\.[0-9]+\\.[0-9]+" title="请输入三段数字版本，例如 3.17.2" required><small>用于管理请求的版本标识，不改变设备指纹或上游验证规则。</small></label>
            <label class="f">推理 API 版本<input id="s-http-version" value="${esc(s.http.client_version || '3.17.2')}" pattern="[0-9]+\\.[0-9]+\\.[0-9]+" title="请输入三段数字版本，例如 3.17.2" required><small>对应 http.client_version，仅调整推理请求的版本标识。</small></label>
          </div>
          <div class="settings-code-grid">
            <div class="code-field"><label class="f" for="s-aliases">模型别名（JSON 对象）</label><textarea id="s-aliases" class="code-input" spellcheck="false" rows="6" aria-describedby="s-aliases-hint">${esc(JSON.stringify(s.aliases, null, 2))}</textarea><p id="s-aliases-hint" class="json-hint" data-for="s-aliases" aria-live="polite"></p></div>
            <div class="code-field"><label class="f" for="s-retry">重试参数（JSON 对象）</label><textarea id="s-retry" class="code-input" spellcheck="false" rows="6" aria-describedby="s-retry-hint">${esc(JSON.stringify(s.retry, null, 2))}</textarea><p id="s-retry-hint" class="json-hint" data-for="s-retry" aria-live="polite"></p></div>
            <div class="code-field code-field-wide"><label class="f" for="s-http">HTTP 超时（JSON 对象，毫秒）<small>total_timeout_ms 为 0 表示不限制整轮时长；client_version 使用上方“推理 API 版本”设置。</small></label><textarea id="s-http" class="code-input" spellcheck="false" rows="5" aria-describedby="s-http-hint">${esc(JSON.stringify(httpParams, null, 2))}</textarea><p id="s-http-hint" class="json-hint" data-for="s-http" aria-live="polite"></p></div>
          </div>
        </div></div></div></div></details>
    </form>`;
  const form = $('#settings-form'), save = $('#s-save'), status = $('#settings-state');
  void mountSettingsUpdate(main);
  const captchaTTLField = $('#s-captcha-ttl');
  const validateCaptchaTTL = () => {
    const value = Number(captchaTTLField.value);
    captchaTTLField.setCustomValidity(value === 0 || (Number.isInteger(value) && value >= 30000 && value <= 90000) ? '' : '请输入 0（关闭预取）或 30000 到 90000 的整数');
  };
  captchaTTLField.addEventListener('input', validateCaptchaTTL);
  $('#s-level').value = s.default_reasoning_level || 'low';
  $('#s-captcha-mode').value = captcha.mode;
  $('#s-proxy-source').value = proxy.source;
  const versionAuto = $('#s-zcode-version-auto'), versionSync = $('#s-zcode-version-sync');
  const versionInterval = $('#s-zcode-version-interval'), versionUnit = $('#s-zcode-version-unit');
  versionUnit.value = String(versionIntervalUnit);
  let versionSyncing = false;
  const syncVersionFields = () => {
    const automatic = versionAuto.checked;
    $('#s-ver').disabled = automatic; $('#s-http-version').disabled = automatic;
    const unit = Number(versionUnit.value);
    versionInterval.min = String(60 / unit); versionInterval.max = String(31536000 / unit);
    syncSelect(versionUnit);
    versionSync.disabled = !automatic || !s.zcode_version_auto_update || settingsDirty || settingsSaving || versionSyncing;
    const checked = s.zcode_version_status?.checked_at;
    versionSync.title = s.zcode_version_status?.error || (checked ? '最近检查：' + new Date(checked).toLocaleString() + '\n下次检查：' + new Date(Number(checked) + (Number(s.zcode_version_check_interval_sec) || 432000) * 1000).toLocaleString() : '查询 ZCode 官方稳定版清单');
  };
  const ids = ['s-poll', 's-jitter', 's-conc', 's-level', 's-ver', 's-http-version', 's-zcode-version-auto', 's-zcode-version-interval', 's-zcode-version-unit', 's-aliases', 's-retry', 's-http', 's-captcha-mode', 's-captcha-timeout', 's-captcha-ttl', 's-captcha-canvas', 's-captcha-node', 's-captcha-jsdom', 's-proxy-enabled', 's-proxy-source', 's-proxy-urls', 's-proxy-subscription', 's-proxy-node', 's-proxy-mihomo', 's-proxy-refresh'];
  const values = () => Object.fromEntries(ids.map(id => [id, $('#' + id).type === 'checkbox' ? $('#' + id).checked : $('#' + id).value]));
  const apply = data => ids.forEach(id => { if (Object.hasOwn(data, id)) { const input = $('#' + id); if (input.type === 'checkbox') input.checked = !!data[id]; else input.value = data[id]; syncSelect(input); } });
  let fingerprint = JSON.stringify(values()), savedAt = '';
  const draftKey = 'z2a_settings_draft';
  const updateDirty = () => {
    settingsDirty = JSON.stringify(values()) !== fingerprint;
    save.disabled = !settingsDirty || settingsSaving;
    save.textContent = settingsSaving ? '正在保存…' : savedAt && !settingsDirty ? '已保存' : '保存设置';
    save.setAttribute('aria-busy', String(settingsSaving));
    status.textContent = settingsSaving ? '正在保存…' : settingsDirty ? '有未保存修改 · 普通参数草稿已保留' : savedAt || '所有设置已保存';
    try { if (settingsDirty) { const draft = values(); delete draft['s-proxy-urls']; delete draft['s-proxy-subscription']; sessionStorage.setItem(draftKey, JSON.stringify(draft)); } else sessionStorage.removeItem(draftKey); }
    catch { if (settingsDirty) status.textContent = '有未保存修改 · 浏览器暂时无法保存草稿，请及时保存'; }
    save.title = status.textContent;
    syncVersionFields();
  };
  versionSync.addEventListener('click', async () => {
    if (versionSync.disabled) return;
    versionSyncing = true; btnLoading(versionSync, true); syncVersionFields();
    try {
      const response = await api('/api/settings/zcode-version/sync', { method: 'POST', body: {} });
      if (response.ok === false) throw new Error(response.error || '同步失败');
      if (!form.isConnected) return;
      s.app_version = response.app_version; s.http.client_version = response.client_version; s.zcode_version_status = response.status;
      if (versionAuto.checked) { $('#s-ver').value = response.app_version; $('#s-http-version').value = response.client_version; }
      const saved = JSON.parse(fingerprint);
      saved['s-ver'] = response.app_version; saved['s-http-version'] = response.client_version;
      fingerprint = JSON.stringify(saved);
      toast('已同步 ZCode ' + response.app_version, 'ok');
    } catch (error) { if (form.isConnected) toast('版本同步失败：' + error.message, 'err'); }
    finally { versionSyncing = false; if (form.isConnected) { btnLoading(versionSync, false); updateDirty(); } }
  });
  try {
    const draft = JSON.parse(sessionStorage.getItem(draftKey) || 'null');
    if (draft && typeof draft === 'object' && !Array.isArray(draft)) {
      try {
        const oldHttp = JSON.parse(draft['s-http']);
        if (oldHttp && Object.hasOwn(oldHttp, 'client_version') && !Object.hasOwn(draft, 's-http-version')) {
          draft['s-http-version'] = String(oldHttp.client_version); delete oldHttp.client_version; draft['s-http'] = JSON.stringify(oldHttp, null, 2);
        }
        if (oldHttp?.captcha) {
          for (const [field, id] of Object.entries({ mode: 's-captcha-mode', timeout_ms: 's-captcha-timeout', simulate_canvas_webgl: 's-captcha-canvas', node_path: 's-captcha-node', jsdom_path: 's-captcha-jsdom', ticket_ttl_ms: 's-captcha-ttl' })) {
            if (Object.hasOwn(oldHttp.captcha, field) && !Object.hasOwn(draft, id)) draft[id] = oldHttp.captcha[field];
          }
          delete oldHttp.captcha; draft['s-http'] = JSON.stringify(oldHttp, null, 2);
        }
      } catch { }
      apply(draft);
    }
  } catch { }
  const validateJSON = () => {
    let valid = true;
    for (const id of ['s-aliases', 's-retry', 's-http']) {
      const box = $('#' + id), hint = $(`.json-hint[data-for="${id}"]`);
      try {
        const parsed = JSON.parse(box.value);
        if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('请输入 JSON 对象，而非数组或其他值');
        if (id === 's-http' && Object.hasOwn(parsed, 'client_version')) throw new Error('请在上方“推理 API 版本”字段设置 client_version，并从此处移除该键');
        if (id === 's-http' && Object.hasOwn(parsed, 'captcha')) throw new Error('请在上方“上游安全验证”设置验证码，并从此处移除 captcha');
        box.classList.remove('invalid'); box.setAttribute('aria-invalid', 'false'); hint.textContent = ''; hint.classList.remove('error');
      } catch (error) {
        valid = false; box.classList.add('invalid'); box.setAttribute('aria-invalid', 'true'); hint.textContent = '配置格式错误：' + error.message; hint.classList.add('error');
      }
    }
    return valid;
  };
  const outboundValues = (data = values()) => ({ enabled: data['s-proxy-enabled'], source: data['s-proxy-source'], urls: [...new Set(data['s-proxy-urls'].split(/\r?\n/).map(x => x.trim()).filter(Boolean))], subscription_url: data['s-proxy-subscription'].trim(), subscription_filter: '', preferred_node: data['s-proxy-node'], mihomo_path: data['s-proxy-mihomo'].trim() || 'mihomo', refresh_interval_sec: Number(data['s-proxy-refresh']) || 3600 });
  const runtimeFields = { node: $('#s-node-status'), jsdom: $('#s-jsdom-status') };
  const captchaRuntimePaths = () => ({ node_path: $('#s-captcha-node').value.trim() || 'node', jsdom_path: $('#s-captcha-jsdom').value.trim() });
  let captchaRuntimeRequest = 0, captchaRuntimeTimer;
  const runtimePending = () => Object.values(runtimeFields).forEach(note => { note.textContent = '检测中…'; note.classList.remove('ok-text', 'warn-text'); note.title = ''; });
  const checkCaptchaRuntime = async () => {
    clearTimeout(captchaRuntimeTimer);
    if (!form.isConnected) return;
    const paths = captchaRuntimePaths(), snapshot = JSON.stringify(paths), request = ++captchaRuntimeRequest;
    runtimePending();
    try {
      const info = await api('/api/settings/captcha/runtime?node_path=' + encodeURIComponent(paths.node_path) + '&jsdom_path=' + encodeURIComponent(paths.jsdom_path));
      if (!form.isConnected || request !== captchaRuntimeRequest || snapshot !== JSON.stringify(captchaRuntimePaths())) return;
      if (info.ok === false) throw new Error(info.error || '检测失败');
      for (const [key, note] of Object.entries(runtimeFields)) {
        const state = info[key] || {};
        note.textContent = state.available ? `可用${state.version ? ' · ' + (key === 'node' ? 'v' : '') + state.version : ''}` : state.message || '不可用';
        note.title = state.path || ''; note.classList.toggle('ok-text', !!state.available); note.classList.toggle('warn-text', !state.available);
      }
    } catch (error) {
      if (form.isConnected && request === captchaRuntimeRequest) Object.values(runtimeFields).forEach(note => { note.textContent = '检测失败'; note.title = error.message; note.classList.add('warn-text'); });
    }
  };
  $('#captcha-panel').addEventListener('toggle', e => { if (form.isConnected && e.currentTarget.open) void checkCaptchaRuntime(); });
  const mihomoField = $('#s-proxy-mihomo'), mihomoButton = $('#s-mihomo-install'), mihomoNote = $('#s-mihomo-status');
  const mihomoPath = () => mihomoField.value.trim() || 'mihomo';
  let mihomoRequest = 0, mihomoTimer, mihomoInstalling = false, lastMihomo = mihomo;
  const showMihomoStatus = (info, note = '') => {
    lastMihomo = info;
    mihomoButton.disabled = mihomoInstalling || !!info.installed || info.supported === false;
    mihomoButton.textContent = info.installed ? '已检测到' : '安装 Mihomo';
    mihomoNote.textContent = note || (info.unavailable ? '暂时无法检测 Mihomo，请稍后重新展开代理面板。' : info.installed ? `已检测到可执行文件${info.path ? '：' + info.path : ''}` : info.supported === false ? '未找到 Mihomo，当前平台请手动安装。' : '未找到 Mihomo，可填写已有路径或点击安装。');
  };
  const checkMihomo = async () => {
    clearTimeout(mihomoTimer);
    if (!form.isConnected || mihomoInstalling) return;
    const path = mihomoPath(), request = ++mihomoRequest;
    mihomoButton.disabled = true; mihomoButton.textContent = '检测中…'; mihomoNote.textContent = '正在检测 Mihomo 是否存在…';
    try {
      const info = await api('/api/settings/proxy/mihomo?path=' + encodeURIComponent(path));
      if (!form.isConnected || request !== mihomoRequest || path !== mihomoPath()) return;
      if (info.ok === false) throw new Error(info.error || '检测失败');
      showMihomoStatus(info);
    } catch (error) {
      if (form.isConnected && request === mihomoRequest && path === mihomoPath()) showMihomoStatus({ ...mihomo, installed: false, unavailable: true }, '检测失败：' + error.message);
    }
  };
  showMihomoStatus(mihomo);
  $('#proxy-panel').addEventListener('toggle', e => { if (form.isConnected && e.currentTarget.open && form.querySelector('#s-proxy-source').value === 'subscription') void checkMihomo(); });
  let proxyNodes = [], nodeRequest = 0;
  const nodeSource = () => { const p = outboundValues(); return JSON.stringify([p.source, p.urls, p.subscription_url]); };
  const renderProxyNodes = () => {
    const selected = $('#s-proxy-node').value;
    $('#s-proxy-nodes').innerHTML = `<label class="proxy-node-option"><input type="radio" name="proxy-node" value="" ${!selected ? 'checked' : ''}><span><strong>自动选择</strong><small>从可用节点中选择，正常时保持使用</small></span></label>` + proxyNodes.map(node => `<label class="proxy-node-option ${node.unavailable || node.blocked ? 'unavailable' : ''}"><input type="radio" name="proxy-node" value="${esc(node.id)}" ${selected === node.id ? 'checked' : ''} ${node.unavailable || node.blocked ? 'disabled' : ''}><span><strong>${esc(node.name)}</strong><small>${esc(node.protocol || 'HTTP / HTTPS')}</small></span>${node.current ? '<span class="proxy-node-badge current">使用中</span>' : node.unavailable ? '<span class="proxy-node-badge">连接故障</span>' : node.blocked ? '<span class="proxy-node-badge">上游拦截</span>' : ''}</label>`).join('');
    $('#s-proxy-node-count').textContent = proxyNodes.length ? `${proxyNodes.length} 个节点` : '暂无节点';
  };
  const loadProxyNodes = async () => {
    const button = $('#s-proxy-load'), status = $('#s-proxy-node-status'), options = outboundValues(), source = nodeSource(), request = ++nodeRequest;
    button.disabled = true; btnLoading(button, true); status.textContent = '正在加载节点…';
    try {
      const response = await api('/api/settings/proxy/nodes', { method: 'POST', body: { outbound_proxy: options } });
      if (!form.isConnected || request !== nodeRequest) return;
      if (response.ok === false) throw new Error(response.error || '加载失败');
      if (source !== nodeSource()) { status.textContent = '配置已修改，请重新加载节点。'; return; }
      proxyNodes = response.nodes || []; renderProxyNodes(); button.textContent = '刷新节点';
      status.textContent = $('#s-proxy-node').value && !proxyNodes.some(node => node.id === $('#s-proxy-node').value) ? '指定节点已不在列表中，将使用其他可用节点。' : proxyNodes.length ? '保存后生效；网关拦截或连接故障时切换，账号风控拒绝不切换代理。' : '请先填写代理地址或有效的订阅地址。';
    } catch (error) { if (request === nodeRequest && form.isConnected) status.textContent = '加载失败：' + error.message; }
    finally { if (request === nodeRequest) { button.disabled = false; btnLoading(button, false); } }
  };
  renderProxyNodes();
  $('#s-proxy-load').addEventListener('click', loadProxyNodes);
  $('#s-proxy-nodes').addEventListener('change', e => {
    if (e.target.name !== 'proxy-node') return;
    $('#s-proxy-node').value = e.target.value; updateDirty();
    $('#s-proxy-node-status').textContent = '节点选择已修改，请保存设置。';
    $('#s-proxy-result').classList.add('hidden');
  });
  const captchaModeHints = {
    off: '直接发送模型请求，不运行验证 SDK。',
    auto: '上游要求模型请求验证时，才运行 SDK 获取票据。',
    always: '上游启用验证场景时，每次模型请求前都获取票据。',
  };
  const syncNetworkFields = () => {
    $('#s-captcha-mode-hint').textContent = captchaModeHints[$('#s-captcha-mode').value] || '';
    const subscription = $('#s-proxy-source').value === 'subscription', enabled = $('#s-proxy-enabled').checked;
    $('#proxy-manual-fields').hidden = subscription;
    $('#proxy-subscription-fields').hidden = !subscription;
    $('#s-proxy-urls').required = enabled && !subscription;
    $('#s-proxy-urls').disabled = subscription;
    for (const id of ['s-proxy-subscription', 's-proxy-mihomo', 's-proxy-refresh']) $('#' + id).disabled = !subscription;
    $('#s-proxy-subscription').required = enabled && subscription;
    $('#s-proxy-refresh').required = subscription;
    $('#s-proxy-summary').textContent = enabled ? '已启用' : '未启用';
  };
  const resetProxyNodeSelection = () => {
    $('#s-proxy-node').value = ''; proxyNodes = []; renderProxyNodes();
    $('#s-proxy-node-status').textContent = '配置已修改，请加载节点后选择。';
  };
  form.addEventListener('input', e => { if (ids.includes(e.target.id)) {
    if (['s-proxy-urls', 's-proxy-subscription'].includes(e.target.id)) resetProxyNodeSelection();
    if (e.target.id === 's-proxy-mihomo') {
      clearTimeout(mihomoTimer); mihomoRequest++;
      if (!mihomoInstalling) { mihomoButton.disabled = true; mihomoNote.textContent = '正在检测 Mihomo 是否存在…'; mihomoTimer = setTimeout(checkMihomo, 250); }
    }
    if (['s-captcha-node', 's-captcha-jsdom'].includes(e.target.id)) {
      clearTimeout(captchaRuntimeTimer); captchaRuntimeRequest++; runtimePending(); captchaRuntimeTimer = setTimeout(checkCaptchaRuntime, 250);
    }
    validateJSON(); syncNetworkFields(); updateDirty(); if (e.target.id.startsWith('s-proxy-')) $('#s-proxy-result').classList.add('hidden');
  } });
  form.addEventListener('change', e => { if (ids.includes(e.target.id)) {
    if (e.target.id === 's-proxy-source') { resetProxyNodeSelection(); $('#s-proxy-result').classList.add('hidden'); if ($('#s-proxy-source').value === 'subscription') void checkMihomo(); }
    if (e.target.id === 's-proxy-mihomo') void checkMihomo();
    if (['s-captcha-node', 's-captcha-jsdom'].includes(e.target.id)) void checkCaptchaRuntime();
    syncNetworkFields(); updateDirty();
  } });
  $('#s-mihomo-install').addEventListener('click', async () => {
    const button = $('#s-mihomo-install'), result = $('#s-mihomo-status');
    clearTimeout(mihomoTimer); mihomoRequest++; mihomoInstalling = true;
    const originalPath = mihomoField.value;
    button.disabled = true; btnLoading(button, true); result.textContent = '正在下载并校验 Mihomo，最长约 3 分钟…';
    let installed = false, note = '';
    try {
      const response = await api('/api/settings/proxy/mihomo/install', { method: 'POST', body: {} });
      if (response.ok === false || !response.path) throw new Error(response.error || '安装失败');
      installed = true;
      if (!form.isConnected) return;
      if (mihomoField.value !== originalPath) return;
      mihomoField.value = response.path;
      $('#s-proxy-result').classList.add('hidden');
      updateDirty();
      lastMihomo = { ...mihomo, ...response, installed: true };
      note = `已安装 ${response.version}，路径已填入，请保存设置。`;
    } catch (error) { note = '安装失败：' + error.message; }
    finally {
      mihomoInstalling = false; btnLoading(button, false);
      const changedPath = installed ? mihomoField.value !== lastMihomo.path : mihomoField.value !== originalPath;
      if (changedPath) void checkMihomo();
      else showMihomoStatus(lastMihomo, note);
    }
  });
  $('#s-proxy-test').addEventListener('click', async () => {
    const button = $('#s-proxy-test'), result = $('#s-proxy-result'), options = outboundValues(), tested = JSON.stringify(options);
    button.disabled = true; btnLoading(button, true); result.classList.remove('hidden'); result.textContent = '正在连接并检测出口，请稍候…';
    try {
      const response = await api('/api/settings/proxy/test', { method: 'POST', body: { outbound_proxy: options } });
      if (tested !== JSON.stringify(outboundValues())) { result.textContent = '配置已修改，请重新检测。'; return; }
      const rows = response.results || [], reachable = rows.filter(row => row.reachable).length;
      result.innerHTML = `<details class="proxy-test-details"><summary><strong>检测结果</strong><span>${rows.length} 个节点 · 可达 ${reachable} · 失败 ${rows.length - reachable}</span></summary><div class="proxy-test-body"><ul>${rows.map(row => `<li><strong>${esc(row.name)}</strong><span class="${row.reachable ? 'ok-text' : 'warn-text'}">${esc(row.reachable ? '出口可达' : row.message)}${row.status ? ` · HTTP ${Number(row.status)}` : ''} · ${Number(row.elapsed_ms)} ms</span></li>`).join('')}</ul></div></details>`;
    } catch (error) { result.textContent = '检测失败：' + error.message; }
    finally { button.disabled = false; btnLoading(button, false); }
  });
  validateJSON(); validateCaptchaTTL(); syncNetworkFields(); updateDirty();
  void checkCaptchaRuntime();
  if (mihomoPath() !== (proxy.mihomo_path.trim() || 'mihomo')) void checkMihomo();
  const initialProxy = outboundValues();
  if (initialProxy.source === 'subscription' ? initialProxy.subscription_url : initialProxy.urls.length) void loadProxyNodes();
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (settingsSaving || !settingsDirty) return;
    const error = $('#settings-error'); error.classList.add('hidden');
    if (!validateJSON()) {
      const invalid = $('.invalid', form), panel = invalid?.closest('details');
      if (panel) panel.open = true;
      invalid?.focus(); return;
    }
    const invalidField = form.querySelector(':invalid');
    for (let section = invalidField?.closest('details'); section; section = section.parentElement?.closest('details')) section.open = true;
    if (!form.reportValidity()) return;
    const submitted = values();
    settingsSaving = true; btnLoading(save, true); updateDirty();
    try {
      const response = await api('/api/settings', { method: 'PUT', body: {
        poll_interval_sec: Number(submitted['s-poll']), poll_jitter_sec: Number(submitted['s-jitter']), poll_concurrency: Number(submitted['s-conc']),
        default_reasoning_level: submitted['s-level'], app_version: submitted['s-ver'], zcode_version_auto_update: submitted['s-zcode-version-auto'], zcode_version_check_interval_sec: Math.round(Number(submitted['s-zcode-version-interval']) * Number(submitted['s-zcode-version-unit'])),
        inference_driver: 'http', http: { ...JSON.parse(submitted['s-http']), client_version: submitted['s-http-version'], captcha: { mode: submitted['s-captcha-mode'], node_path: submitted['s-captcha-node'].trim() || 'node', jsdom_path: submitted['s-captcha-jsdom'].trim(), simulate_canvas_webgl: submitted['s-captcha-canvas'], timeout_ms: Number(submitted['s-captcha-timeout']), ticket_ttl_ms: Number(submitted['s-captcha-ttl']) } }, outbound_proxy: outboundValues(submitted), aliases: JSON.parse(submitted['s-aliases']), retry: JSON.parse(submitted['s-retry']),
      } });
      if (response.ok === false) throw new Error(response.error || '保存失败');
      s.zcode_version_auto_update = submitted['s-zcode-version-auto'];
      s.zcode_version_check_interval_sec = Math.round(Number(submitted['s-zcode-version-interval']) * Number(submitted['s-zcode-version-unit']));
      fingerprint = JSON.stringify(submitted);
      savedAt = '已保存并生效 · ' + new Date().toLocaleTimeString('zh-CN', { hour12: false });
      toast('设置已保存并生效', 'ok');
    } catch (err) { error.textContent = '保存失败，修改已保留：' + err.message; error.classList.remove('hidden'); }
    finally { settingsSaving = false; btnLoading(save, false); updateDirty(); }
  });
}

async function pageLogs(main, options = {}) {
  await Z2ALogs.render(main, { api, esc, icon, pagehead, fmtTime, fmtCompactTime, fmtInt, policyLabel, rerender: () => render({ swap: true }) }, options);
}
async function boot() {
  if (!TOKEN) return showLogin();
  try {
    RUNTIME = await api('/api/runtime-info');
  } catch {
    TOKEN = ''; localStorage.removeItem('z2a_token');
    return showLogin();
  }
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  restoreRoute(UI.readRoute(location.hash));
  syncRoute();
  renderNav();
  const ports = `web:${RUNTIME.web_port} · gateway:${RUNTIME.gateway_port}`;
  const pl2 = $('#ports-label2');
  if (pl2) pl2.textContent = ports;
  render();
  startOauthWatcher();
  if (window._refreshTimer) clearInterval(window._refreshTimer);
  window._refreshTimer = setInterval(() => {
    if (!TOKEN || document.hidden || $('#app-view').classList.contains('hidden')) return;
    const period = ['dashboard', 'accounts', 'logs'].includes(currentPage) ? 1000 : 0;
    if (!period || Date.now() - lastRefreshAt < period) return;
    const main = $('#main');
    if (document.querySelector('.modal-bg') || main.dataset.loading === '1' || openSelect) return;
    if (currentPage === 'accounts' && (main.contains(document.activeElement) || main.querySelector('details[open]') || openSelect)) return;
    if (currentPage === 'dashboard' && (main.querySelector('.dashboard-chart:hover') || main.contains(document.activeElement) || activeHint)) return;
    if (currentPage === 'logs' && (!Z2ALogs.canAutoRefresh(main) || openSelect || activeHint)) return;
    render({ swap: true, live: currentPage === 'logs' }).catch(() => {});
  }, 1000);
}
boot();
