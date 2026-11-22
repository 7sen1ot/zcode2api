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
  check: '<path d="m5 12 4 4L19 6"/>',
  copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>',
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
  import: '<path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/><path d="m10 17 5-5-5-5"/><path d="M15 12H3"/>',
  export: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  'chevron-left': '<path d="m15 18-6-6 6-6"/>',
  'chevron-right': '<path d="m9 18 6-6-6-6"/>',
  search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
  activity: '<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>',
  layers: '<path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"/><path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65"/><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65"/>',
  timer: '<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>',
  plug: '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>',
  shield: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>',
  eye: '<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>',
  'eye-off': '<path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/>',
};
const icon = (name, size = 16) =>
  `<svg class="lucide" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ''}</svg>`;

const TOKEN_KEY = 'z2a_token';
let TOKEN = localStorage.getItem(TOKEN_KEY) || sessionStorage.getItem(TOKEN_KEY) || '';
saveToken(TOKEN);
let RUNTIME = null;
let currentPage = 'dashboard';
let keySecretDialog = null;
const UI = globalThis.Z2AUI;
let settingsDirty = false;
let settingsSaving = false;
let routeURL = location.hash;

function saveToken(token) {
  TOKEN = token || '';
  if (TOKEN) localStorage.setItem(TOKEN_KEY, TOKEN);
  else localStorage.removeItem(TOKEN_KEY);
  sessionStorage.removeItem(TOKEN_KEY);
}

window.addEventListener('storage', event => {
  if (event.storageArea !== localStorage || (event.key !== TOKEN_KEY && event.key !== null)) return;
  const token = localStorage.getItem(TOKEN_KEY) || '';
  if (token === TOKEN) return;
  keySecretDialog?.close();
  TOKEN = token;
  if (TOKEN) void boot();
  else showLogin();
});

async function api(path, { token = TOKEN, body, method = 'GET', reauthenticate = true, ...options } = {}) {
  const hasBody = body !== undefined;
  const res = await fetch(path, {
    ...options, method,
    headers: {
      ...(hasBody ? { 'content-type': 'application/json' } : {}),
      ...(token ? { authorization: 'Bearer ' + token } : {}),
    },
    body: hasBody ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 401 && reauthenticate && token === TOKEN && token === (localStorage.getItem(TOKEN_KEY) || '')) {
      saveToken('');
      showLogin();
    }
    throw Object.assign(new Error(data.error || data.msg || `HTTP ${res.status}`), { status: res.status, code: data.code, error_reason: data.error_reason, error_detail: data.error_detail, http_status: data.http_status });
  }
  return data;
}

function toast(msg, type = '') {
  Z2AEffects.toast(msg, type);
}

async function copyText(text, success = '已复制', failure = '复制失败，请手动复制', button = null) {
  try {
    await navigator.clipboard.writeText(text);
    if (!Z2AEffects.copyFeedback(button, success)) toast(success, 'ok');
    return true;
  } catch {
    if (failure) toast(failure, 'err');
    return false;
  }
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
  keySecretDialog?.close();
  $('#login-view').classList.remove('hidden');
  $('#app-view').classList.add('hidden');
}
window.addEventListener('pagehide', () => keySecretDialog?.close());
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const card = $('.login-card');
  const btn = $('#login-form button[type=submit]');
  btn.classList.add('loading');
  try {
    const r = await api('/api/auth/login', { token: '', reauthenticate: false, method: 'POST', body: { password: $('#login-password').value } });
    saveToken(r.token);
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
    saveToken(''); showLogin();
    return;
  }
});

const themeToggle = createPixelThemeToggle($('#theme-btn'), {
  checked: !document.documentElement.classList.contains('dark'),
  onChange: checked => setTheme(checked ? 'light' : 'dark'),
});
function applyThemeBtn() {
  const button = $('#theme-btn');
  const dark = document.documentElement.classList.contains('dark');
  themeToggle.setChecked(!dark);
  button.title = dark ? '切换为浅色主题' : '切换为深色主题';
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
  if (currentPage === 'dashboard') return {
    hours: window._dashboardHours && window._dashboardHours !== 'all' ? window._dashboardHours : '',
  };
  if (currentPage === 'accounts') { const f = window._accFilter || {}; return { q: f.q, status: f.status === 'all' ? '' : f.status, sort: f.sort === 'default' ? '' : f.sort, page: f.page > 1 ? f.page : '', size: f.size && f.size !== 25 ? f.size : '' }; }
  if (currentPage === 'usage') { const f = window._usageDetail || {}; return { hours: window._usageHours || 24, tab: f.tab || 'model', page: f.pages?.[f.tab] || 1 }; }
  if (currentPage === 'logs') return Z2ALogs.routeValues();
  if (currentPage === 'settings') return { section: settingsSection };
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
  settingsSection = currentPage === 'settings' ? readSettingsSection(q.section) : '';
  if (currentPage === 'dashboard') {
    window._dashboardHours = valid(q.hours, ['all', '24', '168', '720'], 'all');
  }
  if (currentPage === 'accounts') window._accFilter = { q: (q.q || '').slice(0, 200), status: valid(q.status, ['active', 'needs_relogin', 'cooldown', 'expired'], 'all'), sort: valid(q.sort, ['remaining', 'expiring'], 'default'), page, size: Number(valid(q.size, ['25', '50', '100'], '25')) };
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
  settingsSection = '';
  delete $('#main').dataset.fp;
  syncRoute(false);
  renderNav();
  await render();
  window.scrollTo({ top: 0, behavior: 'instant' });
  return true;
}
window.addEventListener('hashchange', async () => {
  if (location.hash === '#main' || location.hash === routeURL) return;
  const route = UI.readRoute(location.hash);
  if (currentPage === 'settings' && route.page === 'settings') {
    settingsSection = readSettingsSection(route.query.section);
    syncRoute();
    if (showSettingsSection?.({ focus: true })) return;
  }
  if (!await mayLeaveSettings()) { history.replaceState(null, '', routeURL); return; }
  restoreRoute(route);
  delete $('#main').dataset.fp;
  syncRoute(); renderNav(); await render();
  window.scrollTo({ top: 0, behavior: 'instant' });
});
function renderNav() {
  const nav = $('#nav');
  if (!nav.firstElementChild) {
    nav.innerHTML = `<div class="dock">${PAGES.map(entry => {
      if (entry === '|') return '<span class="dock-sep" aria-hidden="true"></span>';
      const [id, label, symbol] = entry;
      return `<button type="button" class="dock-item" data-page="${id}" aria-label="${label}">
        <span class="di">${icon(symbol, 16)}</span><span class="tip" aria-hidden="true">${label}</span>
      </button>`;
    }).join('')}</div>`;
  }
  for (const item of $$('.dock-item', nav)) {
    const active = item.dataset.page === currentPage;
    item.classList.toggle('active', active);
    if (active) item.setAttribute('aria-current', 'page');
    else item.removeAttribute('aria-current');
  }
}

function isPlainLinkClick(e) {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey;
}

$('#app-view').addEventListener('click', async e => {
  const item = e.target.closest('.dock-item, .toolbar-brand');
  if (!item || (item.matches('a') && !isPlainLinkClick(e))) return;
  e.preventDefault();
  if (await navigateTo(item.dataset.page)) {
    const heading = $('#main h1');
    if (heading) { heading.tabIndex = -1; heading.focus({ preventScroll: true }); }
  }
});

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
  const main = $('#main');
  const page = currentPage;
  const previousMinHeight = main.style.minHeight;
  const preserveHeight = main.dataset.page === page && main.hasChildNodes();
  // Chart measurement forces layout during a rebuild. Keep the document from
  // shrinking in that interval, which would clamp the browser's scroll offset.
  if (preserveHeight) main.style.minHeight = `${main.getBoundingClientRect().height}px`;
  if (openSelection && main.contains(openSelection.wrapper)) openSelection.close();
  if (activeHint && main.contains(activeHint)) closeHint();
  main.querySelector('.page-error')?.remove();
  const previous = main.dataset.page === page ? [...main.childNodes] : [];
  if (!previous.length) main.innerHTML = `${pagehead(PAGES.find(p => p[0] === page)?.[1] || '控制台')}<span class="visually-hidden" role="status">正在加载…</span>${Z2AEffects.skeleton(page)}`;
  main.dataset.loading = '1';
  main.setAttribute('aria-busy', 'true');
  main.classList.toggle('no-anim', !!opts.swap);
  try {
    if (currentPage === 'dashboard') await pageDashboard(main);
    else if (currentPage === 'accounts') await pageAccounts(main);
    else if (currentPage === 'claims') await pageClaims(main);
    else if (currentPage === 'usage') await pageUsage(main);
    else if (currentPage === 'keys') await pageKeys(main);
    else if (currentPage === 'groups') await pageGroups(main);
    else if (currentPage === 'settings') await pageSettings(main);
    else if (currentPage === 'logs') await pageLogs(main, opts);
    enhanceSelects(main);
    enhanceTables(main);
    main.querySelectorAll('.section-tabs').forEach(Z2AEffects.tabs);
    if (!previous.length) Z2AEffects.reveal(main);
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
    if (preserveHeight) main.style.minHeight = previousMinHeight;
    lastRefreshAt = refreshStartedAt;
    delete main.dataset.loading;
    main.setAttribute('aria-busy', 'false');
  }
}

function enhanceTables(root) {
  root.querySelectorAll('.tablecard:not(.account-table):not(.compact-account-table):not(.log-desktop):not(.group-table) table').forEach(table => {
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
let openSelection = null;
let selectId = 0;
function syncSelect(select) { selectControls.get(select)?.sync(); }

// Both menus and hints stay inside the visible viewport, including short
// windows and the on-screen keyboard. Dimensions come from the shared tokens.
function positionFloatingPanel(panel, trigger, matchWidth = false) {
  panel.style.width = panel.style.minWidth = panel.style.maxHeight = panel.style.maxWidth = '';
  const styles = getComputedStyle(panel), rect = trigger.getBoundingClientRect();
  const edge = parseFloat(styles.getPropertyValue('--popover-edge'));
  const gap = parseFloat(styles.getPropertyValue('--popover-gap'));
  const viewport = window.visualViewport;
  const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
  const width = viewport?.width || innerWidth, height = viewport?.height || innerHeight;
  const availableWidth = Math.max(0, width - edge * 2);
  const availableHeight = Math.max(0, height - edge * 2);
  const naturalWidth = panel.getBoundingClientRect().width;
  panel.style.maxWidth = `${availableWidth}px`;
  const preferredWidth = matchWidth === 'exact' ? rect.width : matchWidth ? Math.max(rect.width, naturalWidth) : naturalWidth;
  if (matchWidth === 'exact') panel.style.minWidth = '0';
  panel.style.width = `${Math.min(availableWidth, preferredWidth)}px`;
  const preferredHeight = panel.getBoundingClientRect().height;
  const below = Math.max(0, Math.min(availableHeight, top + height - rect.bottom - gap - edge));
  const above = Math.max(0, Math.min(availableHeight, rect.top - top - gap - edge));
  const up = below < preferredHeight && above > below;
  panel.style.maxHeight = `${Math.min(preferredHeight, up ? above : below)}px`;
  const size = panel.getBoundingClientRect();
  panel.style.left = `${Math.max(left + edge, Math.min(rect.left, left + width - size.width - edge))}px`;
  panel.style.top = `${Math.max(top + edge, Math.min(up ? rect.top - size.height - gap : rect.bottom + gap, top + height - size.height - edge))}px`;
}

function enhanceSelects(root) {
  root.querySelectorAll('select:not([multiple])').forEach((select) => {
    if (selectControls.has(select)) { syncSelect(select); return; }
    // A help button can precede the select inside an implicit label, making
    // select.labels empty. Retain that visible label when enhancing it.
    const labels = [...new Set([...select.labels, select.closest('label')].filter(Boolean))];
    const labelContent = labels[0]?.cloneNode(true);
    labelContent?.querySelectorAll('select, input, button, small, .info-tip').forEach(node => node.remove());
    const label = select.getAttribute('aria-label') || labelContent?.textContent.trim() || '选择选项';
    const wrapper = document.createElement('span');
    wrapper.className = `select-control${select.classList.contains('round') ? ' control-comfortable' : ''}`;
    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'select-trigger control-trigger';
    trigger.setAttribute('role', 'combobox');
    trigger.setAttribute('aria-label', label);
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const list = document.createElement('div');
    list.className = 'select-list';
    list.id = `select-list-${++selectId}`;
    trigger.id = `${list.id}-trigger`;
    trigger.innerHTML = '<span class="select-value"></span>';
    const value = $('.select-value', trigger);
    labels.forEach(label => { label.htmlFor = trigger.id; });
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
    const isDisabled = option => option.disabled || option.parentElement.matches('optgroup:disabled');
    const enabled = () => [...select.options].map((o, i) => isDisabled(o) ? -1 : i).filter((i) => i >= 0);
    const scrollActiveOption = () => {
      const item = list.children[active];
      if (!item) return;
      if (item.offsetTop < list.scrollTop) list.scrollTop = item.offsetTop;
      else if (item.offsetTop + item.offsetHeight > list.scrollTop + list.clientHeight) {
        list.scrollTop = item.offsetTop + item.offsetHeight - list.clientHeight;
      }
    };
    const sync = () => {
      trigger.disabled = select.matches(':disabled');
      value.textContent = select.selectedOptions[0]?.textContent || '请选择';
      for (const name of ['aria-labelledby', 'aria-describedby', 'aria-invalid']) {
        if (select.hasAttribute(name)) trigger.setAttribute(name, select.getAttribute(name));
        else trigger.removeAttribute(name);
      }
      trigger.setAttribute('aria-required', String(select.required));
      if (trigger.disabled) close();
      else if (!list.hidden) { active = select.selectedIndex; paint(); }
    };
    const paint = () => {
      list.replaceChildren();
      [...select.options].forEach((option, index) => {
        const item = document.createElement('div');
        item.id = `${list.id}-option-${index}`;
        item.className = 'select-option';
        item.setAttribute('role', 'option');
        item.setAttribute('aria-selected', String(index === select.selectedIndex));
        if (isDisabled(option)) item.setAttribute('aria-disabled', 'true');
        if (index === active) item.classList.add('is-active');
        const text = document.createElement('span');
        text.textContent = option.textContent;
        item.appendChild(text);
        if (index === select.selectedIndex) item.insertAdjacentHTML('beforeend', '<span class="select-check" aria-hidden="true">✓</span>');
        item.addEventListener('pointerdown', (e) => e.preventDefault());
        item.addEventListener('click', e => { e.preventDefault(); if (!isDisabled(option)) choose(index); });
        list.appendChild(item);
      });
      if (active >= 0) {
        trigger.setAttribute('aria-activedescendant', `${list.id}-option-${active}`);
        scrollActiveOption();
      }
    };
    const close = () => {
      if (list.hasAttribute('popover') && list.matches(':popover-open')) list.hidePopover();
      list.hidden = true;
      if (list.parentElement !== wrapper) wrapper.appendChild(list);
      trigger.setAttribute('aria-expanded', 'false');
      trigger.removeAttribute('aria-activedescendant');
      if (openSelection?.trigger === trigger) openSelection = null;
    };
    const open = () => {
      if (select.matches(':disabled') || !enabled().length) return;
      openSelection?.close();
      closeHint();
      active = enabled().includes(select.selectedIndex) ? select.selectedIndex : enabled()[0];
      list.hidden = false;
      if (list.hasAttribute('popover')) list.showPopover();
      else {
        const styles = getComputedStyle(wrapper);
        for (const name of ['--control-height', '--control-font-size', '--control-padding', '--control-gap']) list.style.setProperty(name, styles.getPropertyValue(name));
        document.body.appendChild(list);
      }
      trigger.setAttribute('aria-expanded', 'true');
      openSelection = { trigger, wrapper, list, close };
      paint();
      positionFloatingPanel(list, trigger, select.hasAttribute('data-match-width') ? 'exact' : true);
      scrollActiveOption();
    };
    const choose = (index) => {
      if (!enabled().includes(index) || select.matches(':disabled')) return;
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
      if (e.key.length === 1 && e.key !== ' ' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.isComposing) {
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
document.addEventListener('pointerdown', (e) => { if (openSelection && !openSelection.wrapper.contains(e.target) && !openSelection.list.contains(e.target)) openSelection.close(); });
document.addEventListener('scroll', (e) => { if (openSelection && !openSelection.list.contains(e.target)) openSelection.close(); }, true);
document.addEventListener('focusin', e => { if (openSelection && !openSelection.wrapper.contains(e.target) && !openSelection.list.contains(e.target)) openSelection.close(); });
const closeFloatingPanels = () => { openSelection?.close(); closeHint(); };
window.addEventListener('resize', closeFloatingPanels);
window.visualViewport?.addEventListener('resize', closeFloatingPanels);

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
    if (openSelection && box.contains(openSelection.wrapper)) openSelection.close();
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
    const focusable = [...box.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], summary, [tabindex]:not([tabindex="-1"])')].filter((el) => el.tabIndex >= 0 && el.getClientRects().length);
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
    const dialog = modal(title, form, () => resolve(false));
    $('[data-cancel]', form).addEventListener('click', () => dialog.close());
    $('[data-confirm]', form).addEventListener('click', () => { resolve(true); dialog.close(); });
  });
}

function pagehead(title, actionsHtml = '') {
  return `<div class="pagehead fade-up">
    <h1 class="tt">${esc(title)}</h1>
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
  positionFloatingPanel(tip, trigger);
}
document.addEventListener('pointerover', e => { const trigger = e.target.closest('.info-trigger'); if (trigger) showHint(trigger); });
document.addEventListener('focusin', e => { if (e.target.matches('.info-trigger')) showHint(e.target); });
document.addEventListener('pointerout', e => {
  const wrapper = e.target.closest('.info-tip');
  if (wrapper && !wrapper.contains(e.relatedTarget) && !wrapper.contains(document.activeElement)) closeHint();
});
document.addEventListener('focusout', e => { if (e.target.matches('.info-trigger') && !e.target.parentElement.contains(e.relatedTarget)) closeHint(); });
document.addEventListener('scroll', e => { if (activeHint && !activeHint.contains(e.target)) closeHint(); }, true);
document.addEventListener('pointerdown', e => { if (!e.target.closest('.info-tip')) closeHint(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape' && activeHint) { closeHint(); e.preventDefault(); e.stopImmediatePropagation(); } }, true);

function statcard(label, value, { tone = 'neutral', hint = '', icon: ic = '', ratio = null } = {}) {
  return `<div class="statcard${ratio !== null ? ' statcard-ratio' : ''}">
    <div class="head"><span class="t">${esc(label)}</span>${ic ? `<span class="ico tone-${tone}">${icon(ic, 13)}</span>` : ''}</div>
    <div class="stat-value-row"><div class="stat tone-${tone}">${value}</div>${ratio !== null ? `<svg class="metric-ring" viewBox="0 0 48 48" aria-hidden="true"><circle class="metric-ring-track" cx="24" cy="24" r="19"/><circle class="metric-ring-value${ratio > 0 ? '' : ' is-empty'}" cx="24" cy="24" r="19" pathLength="100" stroke-dasharray="${Math.max(0, Math.min(100, ratio))} 100"/></svg>` : ''}</div>
    ${hint ? `<div class="d">${esc(hint)}</div>` : ''}
  </div>`;
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

const ACCOUNT_FAMILY_LABELS = { bigmodel: '国内账号', zai: '国际账号' };
const AVATAR_HUES = [245, 168, 304, 15, 75, 205];

function loginProviderLabel(provider) {
  return ({ zai_cn: 'Z.ai 国内', zai: 'Z.ai 国际', bigmodel: 'BigModel（旧版）' })[provider] || 'ZCode';
}

function avatarMarkup(name, id) {
  let hash = 0;
  for (const character of String(id || name || '')) {
    hash = (Math.imul(hash, 31) + character.codePointAt(0)) >>> 0;
  }
  const hue = AVATAR_HUES[hash % AVATAR_HUES.length];
  const initial = Array.from(name || '?')[0];
  return `<span class="avatar" style="--avatar-hue:${hue}" aria-hidden="true">${esc(initial)}</span>`;
}

function accountNameMarkup(a, action = 'detail') {
  const name = a.platform_name || '未获取官方名称';
  const officialID = a.customer_id == null || a.customer_id === '' ? '未获取官方 ID' : String(a.customer_id);
  const family = loginProviderLabel(a.login_provider || a.family);
  return `<button type="button" class="account-name-button" data-act="${action}" data-id="${esc(a.id)}" title="${esc(name)} · ${esc(officialID)}">${avatarMarkup(a.platform_name, a.id)}<span class="account-identity"><span class="account-row-name">${esc(name)}</span><span class="account-row-id">${esc(officialID)}</span></span><span class="account-row-family">${family}</span></button>`;
}

async function pageDashboard(main) {
  const hours = window._dashboardHours || 'all';
  const d = await api('/api/dashboard?' + new URLSearchParams({ hours }));
  if (currentPage !== 'dashboard' || hours !== (window._dashboardHours || 'all')) return;
  const usage = d.usage_period || d.usage_total || {};
  const fp = JSON.stringify([d.summary, d.busy_24h, d.cache_24h, d.cache_trend_24h?.byHour, usage, hours, Math.floor(Date.now() / 60000)]);
  if (main.dataset.fp === fp) return;
  main.dataset.fp = fp;
  const cached = Number(d.cache_24h?.cached_tokens) || 0, cacheInput = Number(d.cache_24h?.input_tokens) || 0;
  const cacheRate = cacheInput > 0 ? (cached / cacheInput * 100).toFixed(1) + '%' : '—';
  const busyPct = d.busy_24h.total ? (d.busy_24h.busy / d.busy_24h.total * 100).toFixed(1) + '%' : '—';
  const input = Math.max(0, Number(usage.input_tokens) || 0), output = Math.max(0, Number(usage.output_tokens) || 0);
  const tokenTotal = input + output, inputPercent = tokenTotal ? input / tokenTotal * 100 : 0;
  main.innerHTML = `
    ${pagehead('仪表盘', `<span class="updated-at">${icon('refresh', 13)}${new Date().toLocaleTimeString('zh-CN', { hour12: false })}</span>`)}
    <section class="dashboard-overview" aria-label="额度与账号池概览">
      <div class="quota-hero">
        <div class="dashboard-usage-heading"><span class="eyebrow">Token 用量</span><select id="dashboard-hours" class="round" aria-label="用量统计时间范围"><option value="all">全部时间</option><option value="24">近 24 小时</option><option value="168">近 7 天</option><option value="720">近 30 天</option></select></div>
        <div class="hero-number">${fmtInt(usage.total_tokens)}</div>
        <div class="token-composition${tokenTotal ? '' : ' is-empty'}" role="img" aria-label="输入 ${fmtInt(input)} Token，输出 ${fmtInt(output)} Token"><span class="token-composition-input" style="width:${inputPercent}%" title="输入 ${fmtInt(input)} Token · ${inputPercent.toFixed(1)}%"></span><span class="token-composition-output" style="width:${tokenTotal ? 100 - inputPercent : 0}%" title="输出 ${fmtInt(output)} Token · ${(tokenTotal ? 100 - inputPercent : 0).toFixed(1)}%"></span></div>
        <div class="dashboard-usage-breakdown"><div data-token-part="input"><span><i class="token-input-dot" aria-hidden="true"></i>输入</span><strong>${fmtInt(input)}</strong></div><div data-token-part="output"><span><i class="token-output-dot" aria-hidden="true"></i>输出</span><strong>${fmtInt(output)}</strong></div></div>
      </div>
      <div class="pool-health">
        <div class="section-heading"><h2>账号池状态</h2></div>
        <div class="health-number">${d.summary.activeCount}<small>/ ${d.summary.count} 个账号可用</small></div>
        <div class="pool-availability" aria-hidden="true"><span style="width:${d.summary.count ? Math.max(0, Math.min(100, d.summary.activeCount / d.summary.count * 100)) : 0}%"></span></div>
        <button type="button" class="text-action" data-open-page="accounts">管理账号 ${icon('users', 14)}</button>
      </div>
    </section>
    <section class="dashboard-secondary" aria-label="额度与账号指标">
      ${statcard('剩余额度', `<span title="${esc(fmtInt(d.summary.totalRemaining))} Token">${fmtQuota(d.summary.totalRemaining)}<small> Token</small></span>`, { icon: 'layers' })}
      ${statcard('24 小时缓存命中率', cacheRate, { ratio: cacheInput > 0 ? cached / cacheInput * 100 : 0, hint: `${fmtQuota(cached)} / ${fmtQuota(cacheInput)} 输入 Token` })}
      ${statcard('24 小时忙碌率', busyPct, { ratio: d.busy_24h.total ? d.busy_24h.busy / d.busy_24h.total * 100 : 0, tone: Number.parseFloat(busyPct) >= 30 ? 'warning' : 'neutral', hint: `${fmtInt(d.busy_24h.busy)} / ${fmtInt(d.busy_24h.total)} 次网关请求` })}
    </section>
    <section id="dashboard-trend" class="combo-chart-card"></section>`;

  renderTokenCacheChart($('#dashboard-trend', main), d.cache_trend_24h);
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
}

window._accFilter = window._accFilter || { q: '', status: 'all', sort: 'default' };
const ACC_STATUS = { active: '可用', cooldown: '冷却中', needs_relogin: '需授权', unverified: '待验证', web_only: '仅网页', expired: '已过期', disabled: '已停用' };

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
    ${live || pending ? `<details class="quota-details"><summary class="disclosure-link">查看 ${buckets.length + pendingBuckets.length} 项额度</summary><div class="quota-ledger">${live}${pending}</div></details>` : `<p class="quiet-message">${account.has_zcode_jwt ? account.last_balance ? '暂无生效额度' : '等待余额查询' : '网页账号不参与推理'}</p>`}`;
}

async function pageAccounts(main) {
  const [accounts, groups] = await Promise.all([api('/api/accounts'), api('/api/groups')]);
  window._accCache = accounts;
  const gmap = Object.fromEntries(groups.map((g) => [g.id, g.name]));
  const f = window._accFilter;
  const statusCounts = {};
  for (const a of accounts) statusCounts[a.status] = (statusCounts[a.status] || 0) + 1;
  const attention = (statusCounts.needs_relogin || 0) + (statusCounts.unverified || 0);
  const remaining = accounts.reduce((sum, a) => sum + (Number(a.remaining) || 0), 0);
  const pending = accounts.reduce((sum, a) => sum + (Number(a.pending_total) || 0), 0);
  const pill = (v, label) => `<button type="button" data-st="${v}" aria-pressed="${f.status === v}" class="${f.status === v ? 'active' : ''}">${label}<span class="cnt">${v === 'all' ? accounts.length : v === 'needs_relogin' ? attention : statusCounts[v] || 0}</span></button>`;
  main.innerHTML = `
  ${pagehead('账号管理', `<button class="btn" id="accounts-refresh">${icon('refresh', 14)}刷新</button><button class="btn" id="accounts-import">${icon('import', 14)}导入</button><button class="btn" id="accounts-export" ${accounts.length ? '' : 'disabled'}>${icon('export', 14)}导出</button><button class="btn primary" id="add-btn">${icon('plus', 14)}添加账号</button>`)}
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
  <nav class="status-pills account-filters" id="acc-status" aria-label="按账号状态筛选">${pill('all', '全部')}${pill('active', '可用')}${pill('needs_relogin', '需处理')}${pill('cooldown', '冷却中')}${pill('expired', '已过期')}</nav>
  <section><div id="acc-table"></div></section>`;
  $('#add-btn').addEventListener('click', () => addAccountModal());
  $('#accounts-import').addEventListener('click', () => importAccountsModal(groups));
  $('#accounts-export').addEventListener('click', () => exportAccountsModal(accounts, filterAccounts(accounts, gmap, f)));
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
    if (!list.length) { f.page = 1; tbl.innerHTML = `<div class="management-empty">${icon('users', 24)}<h2>${accounts.length ? '没有匹配的账号' : '暂无账号'}</h2></div>`; return; }

    const page = UI.paginate(list, f.page, f.size || 25);
    f.page = page.page;
    tbl.innerHTML = `<div class="tablecard account-table compact-account-table" tabindex="0" role="region" aria-label="账号管理横向列表"><table><colgroup><col class="account-name-col"><col style="width:95px"><col style="width:150px"><col style="width:115px"><col style="width:145px"><col style="width:100px"><col style="width:160px"></colgroup><thead>
      <tr><th scope="col">账号</th><th scope="col">状态</th><th scope="col"><span class="cell-grid account-row-quota"><span>可用额度</span><span class="table-hint">Token</span></span></th><th scope="col" class="align-right" title="来自上游余额快照差额，可能包含外部客户端请求">24 小时消耗</th><th scope="col">最近到期</th><th scope="col">分组</th><th scope="col" class="align-right">操作</th></tr></thead><tbody>
      ${page.items.map(a => `<tr data-acc-id="${esc(a.id)}">
        <td>${accountNameMarkup(a)}</td>
        <td><span class="badge ${esc(a.status)}" title="${esc(a.last_error || '')}">● ${esc(ACC_STATUS[a.status] || a.status)}</span></td>
        <td><span class="cell-grid account-row-quota"><strong title="${esc(fmtInt(a.remaining))} Token">${fmtQuota(a.remaining)}</strong><button type="button" class="text-action" data-act="detail" data-id="${esc(a.id)}" aria-label="查看${esc(a.platform_name || '未获取官方名称')}的额度明细">明细</button>${(a.pending_buckets || []).length ? `<span class="account-pending-dot" role="img" title="${fmtQuota(a.pending_total)} Token 待生效" aria-label="有额度待生效"></span>` : ''}</span></td>
        <td class="align-right" title="${esc(fmtInt(a.delta24h))} Token">${fmtQuota(a.delta24h)}</td>
        <td class="account-row-expiry" title="${esc(a.card_expires_at ? fmtTime(a.card_expires_at) + '，' + fmtPlanRemaining(a.card_expires_at - Date.now()) : '到期时间未知')}">${a.card_expires_at ? esc(fmtCompactTime(a.card_expires_at)) : '未知'}</td>
        <td><span class="account-row-group" title="${esc(gmap[a.group_id] || '待分组')}">${esc(gmap[a.group_id] || '待分组')}</span></td>
        <td class="align-right"><div class="cell-inline cell-actions">
          <button class="ibtn" data-act="renew" data-id="${esc(a.id)}" aria-label="续期" title="更新账号凭据">${icon('refresh', 14)}</button>
          <button class="ibtn" data-act="verify" data-id="${esc(a.id)}" aria-label="验证连通性" title="验证连通性">${icon('zap', 14)}</button>
          <button class="ibtn" data-act="edit" data-id="${esc(a.id)}" aria-label="编辑账号" title="编辑账号">${icon('pencil', 14)}</button>
          <button class="ibtn account-delete" data-act="del" data-id="${esc(a.id)}" aria-label="删除账号" title="删除账号">${icon('trash', 14)}</button>
        </div></td></tr>`).join('')}</tbody></table></div>${accountPagerMarkup(page, [25, 50, 100])}`;
    enhanceSelects(tbl);
    tbl.onclick = onAccountAction;
  }
}

function btnLoading(btn, on) {
  btn.classList.toggle('loading', !!on);
  if (on) btn.setAttribute('aria-busy', 'true');
  else btn.removeAttribute('aria-busy');
}

async function buttonAction(button, action, errorPrefix = '') {
  if (button.disabled) return;
  button.disabled = true; btnLoading(button, true);
  try { await action(); }
  catch (error) { toast(errorPrefix + error.message, 'err'); }
  finally { button.disabled = false; btnLoading(button, false); }
}

function bindDialogForm(form, dialog, submit, complete) {
  $('[data-cancel]', form).addEventListener('click', () => dialog.close());
  form.addEventListener('submit', async event => {
    event.preventDefault();
    const button = $('button[type=submit]', form), error = $('.dialog-error', form);
    if (button.disabled || form.closest('.modal-bg')?.classList.contains('closing')) return;
    const controls = [...form.elements].map(control => [control, control.disabled]);
    controls.forEach(([control]) => { control.disabled = true; });
    error.textContent = ''; form.dataset.submitting = 'true'; btnLoading(button, true);
    try {
      const result = await submit();
      if (result?.ok === false) throw new Error(result.error || '操作失败');
      delete form.dataset.submitting;
      await complete(result);
    } catch (err) { error.textContent = err.message; }
    finally {
      delete form.dataset.submitting;
      controls.forEach(([control, disabled]) => { control.disabled = disabled; });
      btnLoading(button, false);
    }
  });
}

function exportAccountsModal(accounts, filtered) {
  const hasFilter = filtered.length !== accounts.length;
  const form = h(`<form class="dialog-form">
    <label class="dialog-field">导出范围<select name="scope"><option value="all">全部账号（${accounts.length}）</option>${hasFilter ? `<option value="filtered" ${filtered.length ? '' : 'disabled'}>当前筛选（${filtered.length}）</option>` : ''}</select></label>
    <p class="inline-notice transfer-notice">文件包含明文登录凭据，持有文件的人可使用这些账号。请妥善保管，勿公开分享。</p>
    <p class="dialog-error error" role="alert"></p>
    <div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary">${icon('export', 14)}导出 JSON</button></div>
  </form>`);
  const dialog = modal('导出账号', form);
  bindDialogForm(form, dialog, () => {
    const selected = form.elements.scope.value === 'filtered' ? filtered : accounts;
    return api('/api/accounts/export', { method: 'POST', body: { account_ids: selected.map(account => account.id) }, cache: 'no-store' });
  }, data => {
    const url = URL.createObjectURL(new Blob([JSON.stringify(data) + '\n'], { type: 'application/json;charset=utf-8' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = `zcode2api-accounts-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
    document.body.appendChild(link);
    try { link.click(); }
    finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 10000); }
    dialog.close();
    toast(`已导出 ${data.accounts.length} 个账号`, 'ok');
  });
}

function importAccountsModal(groups) {
  const form = h(`<form class="dialog-form">
    <label class="dialog-field">账号文件<input class="account-transfer-file" type="file" name="file" accept=".json,application/json" required></label>
    <div class="dialog-field"><div class="field-label"><label for="transfer-group">导入到分组</label>${infoTip('重复账号自动跳过。导入后使用本机代理设置，并重新检查凭据；已停用账号保持停用。', '账号导入')}</div><select id="transfer-group" name="group"><option value="">自动分配主力分组</option>${groups.map(group => `<option value="${esc(group.id)}">${esc(group.name)}</option>`).join('')}</select></div>
    <p class="dialog-error error" role="alert"></p>
    <div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary" disabled>${icon('import', 14)}导入账号</button></div>
  </form>`);
  const dialog = modal('导入账号', form);
  const input = form.elements.file, button = $('button[type=submit]', form), error = $('.dialog-error', form);
  input.addEventListener('change', () => {
    error.textContent = '';
    const file = input.files[0];
    button.disabled = !file || file.size > 32 * 1024 * 1024;
    if (file?.size > 32 * 1024 * 1024) error.textContent = '文件不能超过 32 MB，请分批导入。';
  });
  bindDialogForm(form, dialog, async () => {
    const file = input.files[0];
    if (!file) throw new Error('请选择账号文件。');
    const text = (await file.text()).replace(/^\uFEFF/, '');
    let data;
    try { data = JSON.parse(text); }
    catch { throw new Error('文件不是有效的 JSON，请选择本程序导出的账号文件。'); }
    if (data?.format !== 'zcode2api-accounts' || data.version !== 1 || !Array.isArray(data.accounts) || !data.accounts.length) {
      throw new Error('文件格式或版本不支持，请选择本程序导出的账号文件。');
    }
    const body = { data, group_id: form.elements.group.value || null };
    if (new Blob([JSON.stringify(body)]).size > 32 * 1024 * 1024) throw new Error('文件内容过大，请分批导入。');
    return api('/api/accounts/import', { method: 'POST', body });
  }, result => {
    dialog.close();
    toast(`已导入 ${result.imported} 个账号${result.skipped ? `，跳过 ${result.skipped} 个重复账号` : ''}`, 'ok');
    if (result.imported && currentPage === 'accounts') {
      window._accFilter = { q: '', status: 'all', sort: 'default', page: 1, size: window._accFilter?.size || 25 };
      syncRoute();
      render({ swap: true }).catch(() => toast('账号已导入，请手动刷新列表', 'err'));
    }
  });
}

function groupAcceptsAccount(group, account) {
  return !group.family_filter || group.family_filter === account.family;
}

function saveAccountPlacement(account, groupId, fields = {}) {
  const current = account.group_id || null;
  const body = groupId === current ? fields : { ...fields, group_id: groupId, expected_group_id: current };
  return api(`/api/accounts/${encodeURIComponent(account.id)}`, { method: 'PATCH', body });
}

async function onAccountAction(e) {
  const btn = e.target.closest('button[data-act]');
  if (!btn || btn.disabled) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'copy') {
    return copyText(id, '账号 ID 已复制', '复制失败，请手动选择账号 ID', btn);
  }

  if (btn.dataset.act === 'detail') {
    const a = (window._accCache || []).find(item => item.id === id);
    if (!a) { toast('账号信息已更新，请刷新列表', 'err'); return; }
    const form = h(`<div class="account-detail"><dl><div><dt>账号名称</dt><dd>${esc(a.platform_name || '未获取官方名称')}</dd></div><div><dt>备注</dt><dd>${esc(a.label)}</dd></div><div><dt>登录渠道</dt><dd>${esc(loginProviderLabel(a.login_provider || a.family))}</dd></div><div><dt>ZCode ID</dt><dd>${esc(a.customer_id || '未获取')}${a.customer_id ? `<button type="button" class="text-action" data-act="copy" data-id="${esc(a.customer_id)}">复制</button>` : ''}</dd></div>${a.business_customer_id ? `<div><dt>业务账号 ID</dt><dd>${esc(a.business_customer_id)}</dd></div>` : ''}<div><dt>状态</dt><dd>${esc(ACC_STATUS[a.status] || a.status)}</dd></div>${a.zcode_jwt_expires_at ? `<div><dt>登录有效期 ${infoTip('主登录到期后需重新授权。业务 token 更新不会延长此有效期。', '登录有效期')}</dt><dd>${esc(fmtTime(a.zcode_jwt_expires_at))}</dd></div>` : ''}${a.has_web_jwt ? `<div><dt>网页会话到期</dt><dd>${esc(fmtTime(a.web_cookie_expires_at))}</dd></div>` : ''}<div><dt>最近额度卡到期</dt><dd>${esc(fmtTime(a.card_expires_at))}</dd></div></dl>${a.last_error ? `<p class="inline-notice error">${esc(a.last_error)}</p>` : ''}<section class="account-detail-quota"><h3>模型额度明细</h3>${accountQuotaMarkup(a)}</section></div>`);
    const details = form.querySelector('.quota-details'); if (details) details.open = true;
    form.addEventListener('click', onAccountAction);
    modal('账号详情', form);
    return;
  }
  if (btn.dataset.act === 'del') {
    if (!await confirmAction('删除账号', '账号凭据和额度快照将一并删除，无法恢复。')) return;
    await buttonAction(btn, async () => {
      const r = await api(`/api/accounts/${id}`, { method: 'DELETE' });
      toast(r.ok ? '已删除' : (r.error || '删除失败'), r.ok ? 'ok' : 'err');
      if (r.ok) render();
    }, '删除失败：');
  } else if (btn.dataset.act === 'renew') {
    const accounts = await api('/api/accounts');
    const a = accounts.find((item) => item.id === id);
    if (!a) return toast('账号不存在', 'err');
    addAccountModal(a);
  } else if (btn.dataset.act === 'verify') {
    await buttonAction(btn, async () => {
      const r = await api(`/api/accounts/${id}/verify`, { method: 'POST' });
      toast(`验证：${r.status}${r.reason ? '（' + r.reason + '）' : ''}`, r.ok ? 'ok' : 'err');
      render();
    }, '验证失败：');
  } else if (btn.dataset.act === 'edit') {
    const [accounts, groups] = await Promise.all([api('/api/accounts'), api('/api/groups')]);
    const a = accounts.find((x) => x.id === id);
    if (!a) return toast('账号不存在', 'err');
    const form = h(`<div>
      <div class="kv" style="margin-bottom:12px"><b>备注</b><input id="e-label" value="${esc(a.label)}"></div>
      <div class="kv" style="margin-bottom:12px"><b>分组</b><select id="e-group">
        <option value="">待分组</option>
        ${a.group_id && !groups.some(g => g.id === a.group_id) ? `<option value="${esc(a.group_id)}" selected>分组已失效</option>` : ''}
        ${groups.map(g => {
          const selected = a.group_id === g.id, compatible = groupAcceptsAccount(g, a);
          return `<option value="${esc(g.id)}" ${selected ? 'selected' : compatible ? '' : 'disabled'}>${esc(g.name)}${compatible ? '' : '（类型不符）'}</option>`;
        }).join('')}
      </select></div>
      <div class="kv" style="margin-bottom:6px"><label for="e-proxy">账号出站代理</label><input id="e-proxy" placeholder="http://127.0.0.1:7890（留空跟随全局设置）" value="${esc(a.proxy_url || '')}"></div>
      <div class="row"><button class="btn primary" id="e-save">保存</button></div></div>`);
    const m = modal('编辑账号', form);
    $('#e-save', form).addEventListener('click', event => buttonAction(event.currentTarget, async () => {
      await saveAccountPlacement(a, $('#e-group', form).value || null, {
        label: $('#e-label', form).value, proxy_url: $('#e-proxy', form).value.trim() || null,
      });
      toast('已保存', 'ok'); m.close(); render();
    }, '保存失败：'));
  }
}

function addAccountModal(target = null) {
  let watchTimer, flowGeneration = 0;
  const stopWatch = () => { clearInterval(watchTimer); watchTimer = null; flowGeneration++; };
  const form = h(`<div class="account-import">
    <section class="import-option">
      <div class="client-import-heading">
        <div class="section-heading"><h2>从客户端导入</h2>${infoTip('读取运行服务的机器上的 ZCode 登录账号。请使用登录客户端的同一系统用户运行服务；重复导入会更新该账号凭据，停用状态保持不变。', '客户端导入')}</div>
        <button type="button" class="btn primary" id="i-local">${icon('import', 14)}读取并导入</button>
      </div>
      <details class="client-import-path disclosure">
        <summary class="control-trigger">导入选项</summary>
        <div class="disclosure-content">
        <label class="dialog-field field-inline" for="i-client-type">客户端类型<select id="i-client-type"><option value="desktop">ZCode 桌面版</option><option value="cli">ZCode CLI</option></select></label>
        <label class="dialog-field" for="i-local-path">凭据文件路径<input id="i-local-path" type="text" autocomplete="off" spellcheck="false" placeholder="留空自动查找 credentials.json"></label>
        </div>
      </details>
      <div id="i-local-result" aria-live="polite" aria-atomic="true"></div>
    </section>
    <section class="import-option">
      <h2>账号授权</h2>
      <div class="row">
        <select id="i-provider" aria-label="登录渠道"><option value="zai_cn">Z.ai 国内</option><option value="zai">Z.ai 国际</option><option value="bigmodel">BigModel（旧版）</option></select>
        <button class="btn" id="i-oauth">开始授权</button>
      </div>
      <div id="oauth-box" aria-live="polite"></div><div id="oauth-pending"></div>
    </section></div>`);
  const m = modal(target ? '更新账号凭据：' + (target.platform_name || target.customer_id || '未获取官方名称') : '添加账号', form, stopWatch);
  if (target) {
    $('#i-local', form).closest('section').remove();
    $('#i-provider', form).value = target.login_provider || target.family;
    $('#i-provider', form).disabled = true;
    syncSelect($('#i-provider', form));
    $('#i-oauth', form).focus();
  }
  if (!target) {
    const button = $('#i-local', form), path = $('#i-local-path', form), clientType = $('#i-client-type', form), result = $('#i-local-result', form);
    button.focus();
    path.addEventListener('keydown', event => {
      if (event.key === 'Enter' && !isImeComposing(event)) { event.preventDefault(); button.click(); }
    });
    button.addEventListener('click', () => buttonAction(button, async () => {
      const controls = [path, clientType, $('#i-provider', form), $('#i-oauth', form)].map(control => [control, control.disabled]);
      controls.forEach(([control]) => { control.disabled = true; });
      syncSelect($('#i-provider', form));
      syncSelect(clientType);
      result.setAttribute('aria-busy', 'true');
      result.innerHTML = '<div class="client-import-progress"><span class="spinner"></span>正在读取并验证账号…</div>';
      try {
        const r = await api('/api/accounts/import-local', { method: 'POST', body: { credentials_path: path.value.trim(), client_type: clientType.value } });
        if (!r.ok) throw new Error(r.error || '客户端导入失败');
        if (r.login_provider) $('#i-provider', form).value = r.login_provider;
        const saved = r.updated ? '账号凭据已更新' : '账号已导入';
        if (currentPage === 'accounts') {
          delete $('#main').dataset.fp;
          render({ swap: true }).catch(() => toast('账号已保存，请手动刷新列表', 'err'));
        }
        if (!form.isConnected || form.closest('.modal-bg')?.classList.contains('closing')) { toast(saved + (r.verified ? '，验证通过' : r.status === 'disabled' ? '，保持停用' : '，' + (ACC_STATUS[r.status] || '待验证')), r.verified ? 'ok' : ''); return; }
        const title = saved + (r.verified ? '，验证通过' : r.status === 'disabled' ? '，保持停用' : '，' + (ACC_STATUS[r.status] || '待验证'));
        const reason = r.status === 'needs_relogin' ? '客户端登录已失效，请在下方重新授权。' : r.reason || '暂时无法完成验证，可在账号列表重试。';
        result.innerHTML = `<div class="client-import-result ${r.verified ? 'ok' : 'warn'}">
          <strong>${icon(r.verified ? 'shield' : 'info', 16)}${esc(title)}</strong>
          ${!r.verified && r.status !== 'disabled' ? `<p>${esc(reason)}</p>` : ''}
          <div class="row"><button type="button" class="btn" data-local-view>查看账号</button><button type="button" class="btn primary" data-local-done>完成</button></div>
        </div>`;
        $('[data-local-done]', result).addEventListener('click', () => m.close());
        $('[data-local-view]', result).addEventListener('click', () => m.close(() => viewAccount(r.id)));
      } catch (error) {
        if (!form.isConnected || form.closest('.modal-bg')?.classList.contains('closing')) { toast(error.message, 'err'); return; }
        result.innerHTML = `<p class="inline-notice error" role="alert">${esc(error.message)}</p>`;
      } finally {
        result.removeAttribute('aria-busy');
        controls.forEach(([control, disabled]) => { control.disabled = disabled; });
        syncSelect($('#i-provider', form));
        syncSelect(clientType);
      }
    }));
  }
  const startOAuth = () => buttonAction($('#i-oauth', form), async () => {
    showOAuthFlow(await api('/api/accounts/oauth/start', { method: 'POST', body: { provider: $('#i-provider', form).value, account_id: target?.id } }));
  });
  $('#i-oauth', form).addEventListener('click', startOAuth);
  function showOAuthFlow(r) {
    if (!r?.flow_id || !r.authorize_url) { toast(r?.error || '暂时无法开始授权，请重试', 'err'); return; }
    stopWatch();
    const generation = flowGeneration;
    let checking = false;
    const box = $('#oauth-box', form);
    const expiresAt = Number(r.expires_at) || 0;
    box.innerHTML = `
      <div class="row" style="gap:8px;margin:0 0 10px">
        <button class="btn primary" id="oauth-open">${icon('plug', 14)}打开授权页面</button>
        <button class="btn" id="oauth-copy">复制链接</button>
      </div>
      <p class="muted" style="margin:0 0 12px;font-size:var(--font-size-caption)">链接有效期 <b id="oauth-expire" class="mono" role="timer" aria-live="off">${expiresAt ? fmtDur(expiresAt - Date.now()) : '—'}</b></p>
      <div class="row" style="gap:10px;align-items:center">
        <span class="muted" id="oauth-status" style="display:inline-flex;align-items:center;gap:6px"><span class="spinner"></span>等待授权…</span>
        <button class="btn" id="oauth-checknow" style="height:26px;padding:0 10px;font-size:var(--font-size-caption)">检查授权状态</button>
      </div>`;
    $('#oauth-open', box).addEventListener('click', () => window.open(r.authorize_url, '_blank', 'noopener'));
    $('#oauth-copy', box).addEventListener('click', event => copyText(r.authorize_url, '授权链接已复制', '复制失败，请从授权按钮的标签页地址栏手动复制', event.currentTarget));
    $('#oauth-checknow', box).addEventListener('click', () => check());
    const setStage = (text, busy = true) => {
      const el = $('#oauth-status', box);
      if (el && (el.textContent !== text || !!el.querySelector('.spinner') !== busy)) el.innerHTML = `${busy ? '<span class="spinner"></span>' : ''}${esc(text)}`;
    };
    async function check() {
      if (generation !== flowGeneration || checking || document.hidden) return;
      if (!document.body.contains(box)) { stopWatch(); return; }
      checking = true;
      try {
        const status = await api(`/api/accounts/oauth/status/${encodeURIComponent(r.flow_id)}`);
        if (generation !== flowGeneration) return;
        if (!document.body.contains(box)) { stopWatch(); return; }
        if (status.status === 'done') return showOAuthDone(status);
        if (['failed', 'expired', 'cancelled'].includes(status.status)) return showOAuthFail(status);
        setStage(status.status === 'exchanging' ? '处理授权结果…' : (status.error || '等待授权…'));
        const exp = $('#oauth-expire', box);
        if (exp && expiresAt) exp.textContent = fmtDur(expiresAt - Date.now());
      } catch (err) { if (generation === flowGeneration) setStage('状态查询失败：' + err.message, false); }
      finally { checking = false; }
    }
    function showOAuthDone(status) {
      stopWatch();
      const x = status.result || {};
      noteFlowSeen(r.flow_id);
      const accId = status.account_id || x.id || null;
      const verification = x.verified ? '验证通过' : (ACC_STATUS[x.status] || '待验证');
      const reason = !x.verified && x.status !== 'disabled' && x.reason ? '。' + x.reason : '';
      if (currentPage === 'accounts') {
        delete $('#main').dataset.fp;
        render({ swap: true }).catch(() => toast('账号已保存，请手动刷新列表', 'err'));
      }
      box.innerHTML = `
        <div class="oauth-result ${x.verified ? 'ok' : 'warn'}">
          <p id="oauth-result-message" class="${x.verified ? 'ok-text' : 'warn-text'}" style="margin:0 0 10px;font-weight:600">${x.updated ? '账号凭据已更新' : '账号已添加'}，${esc(verification + reason)}</p>
          <div class="row" style="gap:8px">
            <button class="btn primary" id="oauth-done" aria-describedby="oauth-result-message">完成</button>
            ${!x.updated && accId ? '<button class="btn" id="oauth-view">查看账号</button>' : ''}
          </div>
        </div>`;
      $('#oauth-done', box).addEventListener('click', () => m.close());
      $('#oauth-view', box)?.addEventListener('click', () => m.close(() => viewAccount(accId)));
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
    pending.innerHTML = matching.map((f, i) => `<button class="btn" data-flow-index="${i}">继续 ${esc(loginProviderLabel(f.provider))} 授权</button>`).join(' ');
    pending.addEventListener('click', (e) => { const b = e.target.closest('[data-flow-index]'); if (b) showOAuthFlow(matching[Number(b.dataset.flowIndex)]); });
  }).catch(() => {});
}

window._oauthSeen = window._oauthSeen || new Set();
function noteFlowSeen(flowId) {
  if (!flowId || window._oauthSeen.has(flowId)) return;
  window._oauthSeen.add(flowId);
  api('/api/accounts/oauth/ack', { method: 'POST', body: { flow_ids: [flowId] } }).catch(() => {});
}
function startOauthWatcher() {
  if (window._oauthWatchTimer) clearInterval(window._oauthWatchTimer);
  let checking = false;
  window._oauthWatchTimer = setInterval(async () => {
    if (checking || !TOKEN || document.hidden || document.querySelector('.modal-bg')) return;
    const session = TOKEN;
    checking = true;
    let w;
    try { w = await api('/api/accounts/oauth/watch'); } catch { return; }
    finally { checking = false; }
    if (session !== TOKEN) return;
    let refresh = false;
    for (const f of w.notified || []) {
      if (window._oauthSeen.has(f.flow_id)) continue;
      noteFlowSeen(f.flow_id);
      const x = f.result || {};
      if (f.status === 'done') {
        toast((x.updated ? '账号凭据已更新' : '登录成功，新账号已加入账号库') + (x.verified ? '（验证通过）' : `（验证未通过：${x.reason || x.status || ''}）`), x.verified ? 'ok' : 'err');
        refresh = true;
      } else {
        toast(`${loginProviderLabel(f.provider)} 授权未完成：${f.error || f.status}`, 'err');
      }
    }
    if (refresh && ['dashboard', 'accounts'].includes(currentPage) && $('#main').dataset.loading !== '1') render({ swap: true }).catch(() => {});
  }, 5000);
}

async function viewAccount(id) {
  const accounts = window._accCache || await api('/api/accounts');
  const account = accounts.find(item => item.id === id);
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

async function pageUsage(main) {
  const hours = window._usageHours || 24;
  const s = await api('/api/usage/stats?hours=' + hours);
  if (currentPage !== 'usage' || hours !== (window._usageHours || 24)) return;
  let requests = 0, success = 0, input = 0, output = 0;
  for (const row of s.byHour || []) {
    requests += Number(row.reqs) || 0;
    success += Number(row.ok) || 0;
    input += Number(row.tin) || 0;
    output += Number(row.tout) || 0;
  }
  const categories = {
    model: { label: '模型', columns: ['模型', '请求数', 'Token 用量'], rows: (s.byModel || []).map((r) => `<tr><td>${esc(r.model || '未知')}</td><td>${fmtInt(r.reqs)}</td><td class="mono">${fmtInt((r.tin || 0) + (r.tout || 0))}</td></tr>`) },
    key: { label: '密钥', columns: ['密钥', '请求数', 'Token 用量'], rows: (s.byKey || []).map((r) => `<tr><td>${esc(r.name || r.key_id || '匿名')}</td><td>${fmtInt(r.reqs)}</td><td class="mono">${fmtInt(r.tokens || 0)}</td></tr>`) },
    account: { label: '账号', columns: ['账号', '请求数', '成功', '忙碌', 'Token 用量'], rows: (s.byAccount || []).map((r) => `<tr><td>${esc(r.label || r.account_id || '未分配')}</td><td>${fmtInt(r.reqs)}</td><td>${fmtInt(r.ok || 0)}</td><td>${fmtInt(r.busy || 0)}</td><td class="mono">${fmtInt(r.tokens || 0)}</td></tr>`) },
    status: { label: '状态', columns: ['状态', '次数'], rows: (s.byStatus || []).map((r) => `<tr><td>${esc(({ success: '成功', busy: '忙碌', error: '失败' })[r.status] || r.status || '未知')}</td><td>${fmtInt(r.n)}</td></tr>`) },
  };
  const detailState = window._usageDetail || (window._usageDetail = { tab: 'model', pages: {} });
  if (!categories[detailState.tab]) detailState.tab = 'model';
  main.innerHTML = `
  ${pagehead('用量统计',
    `<button class="btn" id="usage-refresh">${icon('refresh', 14)}刷新</button>`)}
  <section><div class="cards multicol usage-summary">
    ${statcard('请求数', fmtInt(requests), { tone: 'neutral', icon: 'activity' })}
    ${statcard('成功率', requests ? `${(success / requests * 100).toFixed(1)}%` : '—', { tone: 'success', icon: 'shield', hint: `${fmtInt(success)} 次成功` })}
    ${statcard('Token 用量', fmtInt(input + output), { tone: 'neutral', icon: 'zap', hint: `输入 ${fmtInt(input)} · 输出 ${fmtInt(output)}` })}
    ${statcard('首 Token 时间 P50 / P95', s.ttft ? `${fmtInt(s.ttft.p50)}<small> / ${fmtInt(s.ttft.p95)} ms</small>` : '—', { icon: 'timer', hint: '仅统计成功请求' })}
  </div></section>
  <section id="usage-chart" class="combo-chart-card"></section>
  <section><h2>${icon('layers', 15)} 用量明细</h2><div id="usage-detail"></div></section>`;
  renderTokenCacheChart($('#usage-chart', main), s, { rangeId: 'u-hours' });
  const detail = $('#usage-detail', main);
  const renderDetail = (focusTab = false) => {
    const { tab, pages } = detailState;
    const current = categories[tab];
    const pagination = UI.paginate(current.rows, pages[tab], 10);
    const { total, page, pages: pageCount, items: rows } = pagination;
    pages[tab] = page;
    if (!detail.firstElementChild) detail.innerHTML = `<nav class="section-tabs usage-tabs" role="tablist" aria-label="用量明细分类">${Object.entries(categories).map(([id, group]) => `<button type="button" class="tab" role="tab" id="usage-tab-${id}" aria-selected="false" aria-controls="usage-detail-panel" tabindex="-1" data-usage-tab="${id}">${group.label}<span class="cnt">${fmtInt(group.rows.length)}</span></button>`).join('')}</nav><div class="usage-panel"></div>`;
    detail.querySelectorAll('[data-usage-tab]').forEach(button => {
      const selected = button.dataset.usageTab === tab;
      button.classList.toggle('active', selected);
      button.setAttribute('aria-selected', String(selected));
      button.tabIndex = selected ? 0 : -1;
    });
    detail.querySelector('.usage-panel').outerHTML = `<div class="usage-panel" role="tabpanel" id="usage-detail-panel" aria-labelledby="usage-tab-${tab}">
        ${rows.length ? `<div class="tablecard"><table class="table-metrics"><thead><tr>${current.columns.map((col) => `<th>${col}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>` : '<p class="usage-empty">所选分类暂无数据</p>'}
        ${pageCount > 1 ? `<nav class="pager usage-pager" aria-label="用量明细分页"><button type="button" data-usage-page="prev" aria-label="上一页" ${page === 1 ? 'disabled' : ''}>‹</button>${UI.pageNumberButtons(page, pageCount, 'data-usage-page')}<button type="button" data-usage-page="next" aria-label="下一页" ${page === pageCount ? 'disabled' : ''}>›</button><span class="pinfo">共 ${fmtInt(total)} 条</span></nav>` : ''}
      </div>`;
    enhanceTables(detail);
    Z2AEffects.tabs(detail.querySelector('.usage-tabs'));
    if (focusTab) Z2AEffects.reveal(detail.querySelector('.usage-panel'));
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
  $('#u-hours').addEventListener('change', event => {
    const range = event.target, requested = Number(range.value), previousPages = detailState.pages;
    window._usageHours = requested;
    detailState.pages = {};
    void render({ swap: true }).finally(() => {
      if (!range.isConnected || window._usageHours !== requested) return;
      window._usageHours = hours;
      detailState.pages = previousPages;
      range.value = String(hours);
      syncSelect(range); syncRoute();
    });
  });
  $('#usage-refresh').addEventListener('click', () => render());
}

function showKeySecret(title, secret, notice = '请复制并妥善保存完整密钥。') {
  keySecretDialog?.close();
  const result = h(`<div class="secret-result">
    <p class="dialog-intro">${esc(notice)}</p><pre class="log" tabindex="0" aria-label="完整 API 密钥"></pre>
    <div class="modal-actions"><button type="button" class="btn primary" data-copy>复制密钥</button></div>
  </div>`);
  const value = $('pre', result);
  value.textContent = secret;
  const dialog = modal(title, result, () => {
    value.textContent = '';
    if (keySecretDialog === dialog) keySecretDialog = null;
  });
  keySecretDialog = dialog;
  $('[data-copy]', result).addEventListener('click', event => copyText(value.textContent, '密钥已复制', '复制失败，请手动选择并复制密钥', event.currentTarget));
}

function finishKeyChange(dialog, title, secret, token, notice) {
  if (token === TOKEN) void render();
  dialog.close(() => {
    if (token === TOKEN) showKeySecret(title, secret, notice);
  });
}

function groupChainLabel(ids, groups) {
  const names = ids.map(id => {
    const group = groups.get(id);
    return group ? group.name + (Number(group.enabled) === 1 ? '' : '（已停用）') : '失效分组';
  });
  return names.length ? names.join(' → ') : '无分组';
}

function bindKeyGroupChain(form, initial, groups) {
  const mode = form.elements.chain_mode, select = form.elements.chain_add;
  const custom = $('[data-chain-custom]', form), list = $('ol', custom), add = $('[data-chain-add]', custom);
  const groupsById = new Map(groups.map(group => [group.id, group]));
  let chain = initial?.slice() ?? [];
  mode.value = initial == null ? 'default' : 'custom';
  const paint = () => {
    custom.hidden = mode.value === 'default';
    list.innerHTML = chain.map((id, index) => {
      const group = groupsById.get(id);
      const problem = !group ? '分组已失效' : chain.indexOf(id) !== index ? '重复引用' : '';
      const name = group?.name || String(id ?? '');
      const action = (value, label, disabled) => `<button type="button" class="btn" data-chain-action="${value}" data-index="${index}" aria-label="${esc(label + ' ' + name)}" ${disabled ? 'disabled' : ''}>${label}</button>`;
      return `<li><span>${esc(name)}${problem ? ` <span class="warn-text">${problem}</span>` : Number(group.enabled) === 1 ? '' : '（已停用）'}</span>
        <div class="cell-inline cell-actions">${action('up', '上移', index === 0)}${action('down', '下移', index === chain.length - 1)}${action('remove', '移除')}</div></li>`;
    }).join('');
    select.innerHTML = `<option value="">选择要加入的分组</option>${groups.filter(group => !chain.includes(group.id)).map(group =>
      `<option value="${esc(group.id)}">${esc(group.name)}${Number(group.enabled) === 1 ? '' : '（已停用）'}</option>`).join('')}`;
    select.disabled = select.options.length === 1;
    add.disabled = true;
    syncSelect(select);
  };
  mode.addEventListener('change', paint);
  select.addEventListener('change', () => { add.disabled = !select.value; });
  add.addEventListener('click', () => {
    const id = select.value;
    if (!groupsById.has(id) || chain.includes(id)) return;
    chain.push(id);
    paint();
    list.lastElementChild?.querySelector('[data-chain-action="remove"]')?.focus();
  });
  list.addEventListener('click', event => {
    const button = event.target.closest('[data-chain-action]');
    if (!button || button.disabled) return;
    const index = Number(button.dataset.index), action = button.dataset.chainAction;
    const [id] = chain.splice(index, 1);
    const target = action === 'up' ? index - 1 : action === 'down' ? index + 1 : index;
    if (action !== 'remove') chain.splice(target, 0, id);
    paint();
    const row = list.children[Math.min(target, chain.length - 1)];
    (row?.querySelector(`[data-chain-action="${action}"]:not(:disabled)`) ||
      row?.querySelector('[data-chain-action="remove"]') || mode.parentElement.querySelector('.select-trigger'))?.focus();
  });
  paint();
  return () => mode.value === 'default' ? null : chain.slice();
}

function keyFormValues(form, groupOrder) {
  const fields = form.elements;
  const list = (input) => input.value.split(/[,，]/).map(value => value.trim()).filter(Boolean);
  return {
    name: fields.name.value.trim(),
    quota_limit: fields.quota_limit.value.trim() || null,
    group_order: groupOrder,
    default_level: fields.default_level.value || null,
    model_whitelist: list(fields.model_whitelist),
    ip_whitelist: list(fields.ip_whitelist),
    sticky_minutes: fields.sticky_minutes.value === '' ? 0 : Number(fields.sticky_minutes.value),
    enabled: fields.enabled.checked,
  };
}

function openKeyForm(groups, key) {
  const values = key ?? { enabled: true, sticky_minutes: 0 };
  const form = h(`<form class="dialog-form key-form" autocomplete="off">
    <div class="dialog-grid">
      <label class="dialog-field">密钥名称<input name="name" value="${esc(values.name)}" maxlength="80" placeholder="例如：Claude Code" required></label>
      <label class="dialog-field">配额（Token）<input name="quota_limit" inputmode="numeric" value="${esc(values.quota_limit)}" placeholder="留空不限"></label>
      <div class="dialog-field key-field-wide">
        <div class="field-label"><label for="key-chain-mode">分组链</label>${infoTip('指定分组时按列表从上到下尝试，跳过停用的分组。指定列表为空时，这把密钥无分组可用。', '分组链')}</div>
        <select id="key-chain-mode" name="chain_mode" data-match-width><option value="default">使用默认链</option><option value="custom">指定分组与顺序</option></select>
        <div class="key-chain-custom" data-chain-custom hidden>
          <ol class="key-chain-list" aria-label="分组调度顺序"></ol>
          <div class="key-chain-add"><select name="chain_add" aria-label="选择要加入的分组" data-match-width></select><button type="button" class="btn" data-chain-add>加入</button></div>
        </div>
      </div>
      <label class="dialog-field">默认思考档位<select name="default_level">
        <option value="">跟随全局设置</option>
        <option value="low">low（最快）</option>
        <option value="high">high（深思）</option>
        <option value="max">max</option>
      </select></label>
      <label class="dialog-field">黏住分钟<input name="sticky_minutes" type="number" min="0" step="any" value="${esc(values.sticky_minutes)}" placeholder="0"></label>
      <label class="dialog-field key-field-wide">模型白名单<input name="model_whitelist" value="${esc(values.model_whitelist?.join(','))}" placeholder="逗号分隔，留空允许全部模型" spellcheck="false"></label>
      <label class="dialog-field key-field-wide">IP 白名单<input name="ip_whitelist" value="${esc(values.ip_whitelist?.join(','))}" placeholder="逗号分隔，支持 CIDR 和 1.2.3.*，留空不限" spellcheck="false"></label>
    </div>
    <label class="settings-check key-enabled"><span>启用密钥</span><input name="enabled" type="checkbox" role="switch" ${values.enabled ? 'checked' : ''}></label>
    <p class="dialog-error error" role="alert"></p>
    <div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary">${key ? '保存' : '创建密钥'}</button></div>
  </form>`);
  const level = form.elements.default_level;
  level.value = values.default_level ?? '';
  if (level.selectedIndex === -1) level.selectedIndex = 0;
  const groupOrder = bindKeyGroupChain(form, values.group_order, groups);
  const initial = keyFormValues(form, groupOrder());
  const token = TOKEN;
  const dialog = modal(key ? '编辑密钥' : '新建密钥', form);
  bindDialogForm(form, dialog, () => {
    const payload = keyFormValues(form, groupOrder());
    const body = key ? Object.fromEntries(Object.entries(payload).filter(([name, value]) =>
      JSON.stringify(value) !== JSON.stringify(initial[name]))) : payload;
    if ('name' in body && !body.name) throw new Error('请输入密钥名称');
    if (body.quota_limit != null && !/^\d+$/.test(body.quota_limit)) throw new Error('配额（Token）必须为非负整数');
    if (token !== TOKEN) throw new Error('管理会话已变更，请重新打开表单');
    return key
      ? api(`/api/keys/${encodeURIComponent(key.id)}`, { token, method: 'PATCH', body })
      : api('/api/keys', { token, method: 'POST', body, cache: 'no-store' });
  }, result => {
    if (key) {
      dialog.close();
      if (token === TOKEN) {
        toast('已保存', 'ok');
        void render();
      }
    } else {
      finishKeyChange(dialog, '密钥已创建', result.secret, token);
    }
  });
}

function offerKeyRotation(key, message, token) {
  const form = h(`<form class="dialog-form">
    <p class="dialog-intro">${esc(message)}</p>
    <p class="dialog-intro">确认轮换后，旧密钥立即失效。请将所有客户端切换到新密钥，原有配置和已用 Token 会保留。</p>
    <p class="dialog-error error" role="alert"></p>
    <div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn danger-solid">轮换密钥</button></div>
  </form>`);
  const dialog = modal('轮换历史密钥', form);
  $('[data-cancel]', form).focus();
  bindDialogForm(form, dialog, () => {
    if (token !== TOKEN) throw new Error('管理会话已变更，请重新打开弹窗');
    return api(`/api/keys/${encodeURIComponent(key.id)}/rotate`, {
      token, method: 'POST', body: { key_tail: key.key_tail }, cache: 'no-store',
    });
  }, result => finishKeyChange(dialog, '密钥已轮换', result.secret, token, '请保存新密钥，并更新客户端配置。'));
}

async function copyKey(key, button) {
  const token = TOKEN;
  let result;
  try {
    result = await api(`/api/keys/${encodeURIComponent(key.id)}/secret`, { token, method: 'POST', cache: 'no-store' });
  } catch (error) {
    if (error.code !== 'key_not_recoverable') throw error;
    if (token === TOKEN) offerKeyRotation(key, error.message, token);
    return;
  }
  if (token !== TOKEN) return;
  if (!result.secret) throw new Error('服务端未返回完整密钥');
  if (!await copyText(result.secret, '密钥已复制', '', button) && token === TOKEN) {
    showKeySecret('复制密钥', result.secret, '自动复制失败，请选择并手动复制完整密钥。');
  }
}

async function pageKeys(main) {
  const [keys, groups] = await Promise.all([api('/api/keys'), api('/api/groups')]);
  const groupsById = new Map(groups.map(group => [group.id, group]));
  main.innerHTML = `
  ${pagehead('API 密钥',
    `<button class="btn primary" id="add-key">${icon('plus', 14)}新建密钥</button>`)}
  <div class="gateway-integration"><div class="copy-field"><code>http://127.0.0.1:${RUNTIME?.gateway_port || 6780}/v1</code><button type="button" class="btn" id="copy-gw" aria-label="复制网关地址" title="复制网关地址">${icon('copy', 15)}复制</button></div>${infoTip('使用 Authorization: Bearer <密钥>。OpenAI 请求路径为 /v1/chat/completions，Anthropic 为 /v1/messages。', '网关接入')}</div>
  <section style="animation-delay:50ms"><div id="key-table" class="fade-up"></div></section>`;
  $('#key-table').innerHTML = keys.length ? `<div class="tablecard"><table>
    <tr><th>名称</th><th>尾号</th><th class="align-right">配额</th><th class="align-right">已用</th><th>分组链</th><th>默认档位</th><th>白名单</th><th>状态</th><th class="align-right">操作</th></tr>
    ${keys.map((k) => `<tr>
      <td><b style="font-size:var(--font-size-dense)">${esc(k.name)}</b><br><span class="muted" style="font-size:var(--font-size-micro)">${fmtTime(k.created_at)}</span></td>
      <td class="mono">…${esc(k.key_tail)}</td>
      <td class="mono align-right">${k.quota_limit === null ? '不限' : fmtInt(k.quota_limit)}</td>
      <td class="mono align-right">${fmtInt(k.used_units)}</td>
      <td style="font-size:var(--font-size-caption)">${esc(k.group_order ? groupChainLabel(k.group_order, groupsById) : '默认链')}</td>
      <td>${k.default_level ? `<span class="badge">${esc(({ low: '低', high: '高', max: '最高' })[k.default_level] || k.default_level)}</span>` : '<span class="muted">跟随全局</span>'}</td>
      <td style="font-size:var(--font-size-micro)">${k.model_whitelist.length ? k.model_whitelist.map((m) => `<span class="badge sm">${esc(m)}</span>`).join(' ') : '全部模型'}${k.ip_whitelist.length ? '<br>IP: ' + esc(k.ip_whitelist.join(',')) : ''}</td>
      <td>${k.enabled ? '<span class="ok-text">● 启用</span>' : '<span class="muted">禁用</span>'}</td>
      <td class="align-right"><div class="cell-inline cell-actions key-actions"><button type="button" class="btn" data-key-action="copy" data-id="${esc(k.id)}">复制密钥</button><button type="button" class="btn" data-key-action="edit" data-id="${esc(k.id)}">编辑</button><button type="button" class="btn danger" data-key-action="delete" data-id="${esc(k.id)}">删除</button></div></td></tr>`).join('')}
  </table></div>` : '<p class="muted">还没有密钥。</p>';
  $('#key-table').addEventListener('click', async (e) => {
    const button = e.target.closest('button[data-key-action]');
    if (!button || button.disabled) return;
    const key = keys.find(item => item.id === button.dataset.id);
    if (!key) return;
    if (button.dataset.keyAction === 'edit') {
      openKeyForm(groups, key);
    } else if (button.dataset.keyAction === 'copy') {
      await buttonAction(button, () => copyKey(key, button), '复制失败：');
    } else {
      if (!await confirmAction('删除密钥', '使用此密钥的客户端将立即失去访问权限。')) return;
      await buttonAction(button, async () => {
        await api(`/api/keys/${encodeURIComponent(key.id)}`, { method: 'DELETE' });
        toast('已删除', 'ok');
        render();
      }, '删除失败：');
    }
  });
  $('#copy-gw').addEventListener('click', event => copyText(`http://127.0.0.1:${RUNTIME?.gateway_port || 6780}/v1`, '网关地址已复制', '复制失败，请手动复制', event.currentTarget));
  $('#add-key').addEventListener('click', () => openKeyForm(groups));
}

const GROUP_ROLE_LABELS = { primary: '主力', backup: '备用' };

function openGroupForm(group) {
  const values = group ?? { role: 'primary', policy: 'balanced', urgent_window_min: 180, enabled: 1 };
  const field = (name, label, control) => `<div class="dialog-field"><div class="field-label"><label for="group-${name}">${label}</label></div>${control}</div>`;
  const select = (name, options) => `<select id="group-${name}" name="${name}" data-match-width>${options.map(([value, label]) => `<option value="${value}">${esc(label)}</option>`).join('')}</select>`;
  const form = h(`<form class="dialog-form group-form" autocomplete="off">
    <fieldset class="group-fields">
      <legend>基础信息</legend>
      <div class="dialog-grid">
        <label class="dialog-field group-field-wide">分组名称<input name="name" value="${esc(values.name)}" maxlength="80" placeholder="例如：推理主力" required></label>
        ${field('role', '角色', select('role', Object.entries(GROUP_ROLE_LABELS)))}
        ${field('family_filter', '账号类型', select('family_filter', [['', '全部类型'], ...Object.entries(ACCOUNT_FAMILY_LABELS)]))}
      </div>
    </fieldset>
    <fieldset class="group-fields">
      <legend>调度配置</legend>
      <div class="dialog-grid">
        ${field('policy', '调度策略', select('policy', ['balanced', 'expiring_first', 'remaining_desc'].map(value => [value, policyLabel(value)])))}
        ${field('urgent_window_min', '临期窗口（分钟）', `<input id="group-urgent_window_min" name="urgent_window_min" inputmode="numeric" value="${esc(values.urgent_window_min)}" placeholder="180">`)}
      </div>
    </fieldset>
    <label class="settings-check group-enabled"><span>启用分组</span><input name="enabled" type="checkbox" role="switch" ${Number(values.enabled) === 1 ? 'checked' : ''}></label>
    <p class="dialog-error error" role="alert"></p>
    <div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button><button type="submit" class="btn primary">${group ? '保存' : '创建分组'}</button></div>
  </form>`);
  const fields = form.elements;
  for (const name of ['role', 'family_filter', 'policy']) {
    const value = values[name] ?? '';
    const field = fields[name];
    field.value = value;
    if (field.selectedIndex < 0) field.add(new Option(`历史值：${value}`, value, true, true));
  }
  const read = () => ({
    name: fields.name.value.trim(),
    role: fields.role.value,
    family_filter: fields.family_filter.value || null,
    policy: fields.policy.value,
    urgent_window_min: fields.urgent_window_min.value.trim(),
    enabled: fields.enabled.checked,
  });
  const initial = read();
  const dialog = modal(group ? '编辑分组' : '新建分组', form);
  bindDialogForm(form, dialog, () => {
    const values = read();
    const body = group ? Object.fromEntries(Object.entries(values).filter(([name, value]) => value !== initial[name])) : values;
    if ('name' in body && !body.name) throw new Error('请输入分组名称');
    if ('urgent_window_min' in body) {
      const minutes = body.urgent_window_min;
      if (minutes && !/^\d+$/.test(minutes)) throw new Error('临期窗口必须为非负整数分钟');
      body.urgent_window_min = minutes ? Number(minutes) : null;
    }
    return group
      ? api(`/api/groups/${encodeURIComponent(group.id)}`, { method: 'PATCH', body })
      : api('/api/groups', { method: 'POST', body });
  }, () => {
    dialog.close();
    toast(group ? '分组已保存' : '分组已创建', 'ok');
    void render();
  });
}

async function openGroupAccounts(id) {
  const load = () => Promise.all([api('/api/accounts?view=groups'), api('/api/groups')]);
  let [accounts, groups] = await load();
  const group = () => groups.find(item => item.id === id);
  if (!group()) throw new Error('分组不存在');
  const form = h(`<div class="group-members">
    <div class="management-toolbar">
      <div class="searchbox">${icon('search', 14)}<input data-search aria-label="搜索账号" placeholder="搜索账号或 ID"></div>
      <select aria-label="关联状态" data-membership data-match-width><option value="all">全部账号</option><option value="member">已在本组</option><option value="eligible">可加入</option><option value="incompatible">类型不符</option></select>
    </div>
    <div data-accounts></div>
    <div class="modal-actions"><button type="button" class="btn" data-close>完成</button></div>
  </div>`);
  const list = $('[data-accounts]', form), search = $('[data-search]', form), membership = $('[data-membership]', form);
  const paging = { page: 1, size: 10 };
  const paint = () => {
    const target = group();
    if (!target) {
      list.innerHTML = '<p class="muted">分组已删除。</p>';
      return;
    }
    const query = search.value.trim().toLowerCase();
    const names = new Map(groups.map(item => [item.id, item.name]));
    const filtered = accounts.filter(account => {
      const member = account.group_id === id, compatible = groupAcceptsAccount(target, account);
      if (membership.value === 'member' && !member || membership.value === 'eligible' && (member || !compatible) ||
          membership.value === 'incompatible' && compatible) return false;
      return !query || `${account.label || ''} ${account.customer_id || ''} ${account.id}`.toLowerCase().includes(query);
    });
    filtered.sort((x, y) => Number(y.group_id === id) - Number(x.group_id === id));
    const page = UI.paginate(filtered, paging.page, paging.size);
    paging.page = page.page;
    if (!page.total) {
      list.innerHTML = `<p class="muted">${accounts.length ? '暂无符合条件的账号。' : '暂无账号。'}</p>`;
      return;
    }
    list.innerHTML = `<ul class="group-member-list">${page.items.map(account => {
      const member = account.group_id === id, compatible = groupAcceptsAccount(target, account);
      const name = account.label || account.customer_id || account.id;
      const current = member ? '已在本组' : account.group_id ? names.get(account.group_id) || '分组已失效' : '待分组';
      const identity = account.customer_id || account.id;
      return `<li><div class="group-member-name"><strong>${esc(name)}</strong><span class="muted">${esc(ACCOUNT_FAMILY_LABELS[account.family] || account.family)} · ${esc(account.status === 'active' ? '活跃' : ACC_STATUS[account.status] || account.status)}</span>${name !== identity ? `<span class="mono">${esc(identity)}</span>` : ''}</div>
        <div class="group-member-status"><span>${esc(current)}</span>${compatible ? '' : '<span class="warn-text">类型不符</span>'}</div>
        <div class="cell-inline cell-actions"><button type="button" class="btn" data-member="${esc(account.id)}" aria-label="${esc((member ? '移出 ' : '加入 ') + name)}" ${member || compatible ? '' : 'disabled'}>${member ? '移出' : account.group_id ? '移入' : '加入'}</button></div></li>`;
    }).join('')}</ul>${filtered.length > 10 ? accountPagerMarkup(page, [10, 25, 50]) : ''}`;
    enhanceSelects(list);
  };
  paint();
  const dialog = modal(`管理账号 · ${group().name}`, form, null, { externalFocus: true });
  $('[data-close]', form).addEventListener('click', () => dialog.close());
  const reset = () => { paging.page = 1; paint(); };
  search.addEventListener('input', reset);
  membership.addEventListener('change', reset);
  bindAccountPager(list, paging, paint);
  let saving = false;
  list.addEventListener('click', async (event) => {
    const button = event.target.closest('[data-member]');
    if (saving || !button || button.disabled) return;
    const account = accounts.find(item => item.id === button.dataset.member);
    const target = group();
    if (!account || !target) return;
    const member = account.group_id === id;
    if (!member && !groupAcceptsAccount(target, account)) return;
    saving = true;
    try {
      if (member && !await confirmAction('移出账号', '移出后，账号转为待分组，直到重新分配后才会参与分组调度。', '移出')) return;
      if (!member && account.group_id) {
        const source = groups.find(item => item.id === account.group_id)?.name || '原分组';
        if (!await confirmAction('移动账号', `确认将账号从「${source}」移入「${target.name}」？账号只归属一个分组。`, '移入', false)) return;
      }
      await buttonAction(button, async () => {
        const destination = member ? null : id;
        try {
          await saveAccountPlacement(account, destination);
        } catch (error) {
          if ([400, 404, 409].includes(error.status)) {
            try { [accounts, groups] = await load(); if (form.isConnected) paint(); } catch { /* Keep the request error. */ }
            if (currentPage === 'groups') void render();
          }
          throw error;
        }
        account.group_id = destination;
        if (form.isConnected) {
          paint();
          const next = list.querySelector(`[data-member="${CSS.escape(account.id)}"]:not(:disabled)`);
          (next || search).focus({ preventScroll: true });
        }
        toast(member ? '账号已移出' : '账号已加入', 'ok');
        if (currentPage === 'groups') void render();
      });
    } finally {
      saving = false;
    }
  });
}

async function pageGroups(main) {
  const { groups, default_chain: defaultChain } = await api('/api/groups?details=1');
  const byId = new Map(groups.map(group => [group.id, group]));
  main.innerHTML = `
  ${pagehead('分组调度链',
    `<button type="button" class="btn" id="refresh-groups">${icon('refresh', 14)}刷新</button><button type="button" class="btn primary" id="add-group">${icon('plus', 14)}新建分组</button>`)}
  <div class="group-default-chain"><span class="muted">默认链</span><strong>${esc(groupChainLabel(defaultChain, byId))}</strong></div>
  <section id="group-list" style="animation-delay:50ms">
  ${groups.length ? `<div class="tablecard group-table fade-up" tabindex="0" role="region" aria-label="分组列表"><table>
    <thead><tr><th scope="col">名称</th><th scope="col">角色</th><th scope="col">账号类型</th><th scope="col">组内策略</th><th scope="col" class="align-right">活跃 / 关联</th><th scope="col" class="align-right">操作</th></tr></thead>
    <tbody>
    ${groups.map((g) => `<tr><td><div class="cell-name">${avatarMarkup(g.name, g.id)}<span class="nm"><b>${esc(g.name)}</b></span></div></td>
      <td><span class="badge ${Number(g.enabled) === 1 && g.role === 'primary' ? 'active' : ''}">${esc(GROUP_ROLE_LABELS[g.role] || g.role)}</span>${Number(g.enabled) === 1 ? '' : '<span class="group-row-meta muted">已停用</span>'}</td>
      <td>${esc(ACCOUNT_FAMILY_LABELS[g.family_filter] || g.family_filter || '全部类型')}</td>
      <td>${esc(policyLabel(g.policy))}${g.policy === 'expiring_first' ? `<span class="group-row-meta muted">${fmtInt(g.urgent_window_min || 180)} 分钟</span>` : ''}</td>
      <td class="mono align-right">${fmtInt(g.active_account_count)} / ${fmtInt(g.account_count)}</td>
      <td class="align-right"><div class="cell-inline cell-actions"><button type="button" class="btn" data-group-action="accounts" data-id="${esc(g.id)}">管理账号</button><button type="button" class="btn" data-group-action="edit" data-id="${esc(g.id)}">编辑</button><button type="button" class="btn danger" data-group-action="delete" data-id="${esc(g.id)}">删除</button></div></td></tr>`).join('')}
    </tbody></table></div>`
    : '<p class="muted">暂无分组。</p>'}</section>`;
  $('#add-group').addEventListener('click', () => openGroupForm());
  $('#refresh-groups').addEventListener('click', event => buttonAction(event.currentTarget, () => render({ swap: true })));
  $('#group-list').addEventListener('click', async (e) => {
    const button = e.target.closest('[data-group-action]');
    const group = byId.get(button?.dataset.id);
    if (!group || button.disabled) return;
    if (button.dataset.groupAction === 'edit') return openGroupForm(group);
    if (button.dataset.groupAction === 'accounts') return buttonAction(button, () => openGroupAccounts(group.id), '加载账号失败：');
    const blockers = [];
    if (group.account_count > 0) blockers.push(`先移出 ${group.account_count} 个关联账号`);
    if (group.key_count > 0) blockers.push(`先调整引用该组的 ${group.key_count} 个 API 密钥`);
    if (blockers.length) return toast(`暂无法删除，请${blockers.join('，并')}`, 'err');
    const effect = groups.length === 1
      ? '删除后将没有任何分组。若重启时仍为空，服务会创建默认池。'
      : group.default_chain_after_delete ? `删除后默认链将变为「${groupChainLabel(group.default_chain_after_delete, byId)}」。`
      : '删除后，该分组将从可选分组中移除。';
    if (!await confirmAction('删除分组', `确定删除「${group.name}」？${effect}`)) return;
    await buttonAction(button, async () => {
      try {
        const result = await api(`/api/groups/${encodeURIComponent(group.id)}`, { method: 'DELETE' });
        if (!result.ok) throw new Error(result.error || '操作失败');
        toast('分组已删除', 'ok');
      } finally {
        if (currentPage === 'groups') void render();
      }
    }, '删除失败：');
  });
}

async function pageLogs(main, options = {}) {
  await Z2ALogs.render(main, { api, esc, icon, pagehead, fmtTime, fmtCompactTime, fmtInt, rerender: () => render({ swap: true }) }, options);
}
async function boot() {
  if (!TOKEN) return showLogin();
  const session = TOKEN;
  try {
    const runtime = await api('/api/runtime-info', { token: session });
    if (session !== TOKEN) return;
    RUNTIME = runtime;
  } catch (error) {
    if (session !== TOKEN || error.status === 401) return;
    $('#login-view').classList.add('hidden');
    $('#app-view').classList.remove('hidden');
    renderNav();
    const notice = h(`<section class="page-error" role="alert">${icon('zap', 20)}<div><h2>暂时无法连接服务</h2><p>${esc(error.message)}</p></div><button type="button" class="btn" data-retry-session>${icon('refresh', 14)}重试</button></section>`);
    $('#main').replaceChildren(notice);
    $('[data-retry-session]', notice).addEventListener('click', () => boot());
    return;
  }
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  restoreRoute(UI.readRoute(location.hash));
  syncRoute();
  renderNav();
  $('#ports-label').textContent = `Web ${RUNTIME.web_port} · 网关 ${RUNTIME.gateway_port}`;
  render();
  startOauthWatcher();
  if (window._refreshTimer) clearInterval(window._refreshTimer);
  window._refreshTimer = setInterval(() => {
    if (session !== TOKEN || document.hidden || $('#app-view').classList.contains('hidden')) return;
    const period = currentPage === 'logs' ? 1000 : ['dashboard', 'accounts'].includes(currentPage) ? 5000 : 0;
    if (!period || Date.now() - lastRefreshAt < period) return;
    const main = $('#main');
    if (document.querySelector('.modal-bg') || main.dataset.loading === '1' || openSelection) return;
    if (currentPage === 'accounts' && (main.contains(document.activeElement) || main.querySelector('details[open]'))) return;
    if (currentPage === 'dashboard' && (main.querySelector('.combo-chart-card:hover') || main.contains(document.activeElement) || activeHint)) return;
    if (currentPage === 'logs' && (!Z2ALogs.canAutoRefresh(main) || activeHint)) return;
    render({ swap: true, live: currentPage === 'logs' }).catch(() => {});
  }, 1000);
}
boot();
