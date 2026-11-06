'use strict';

globalThis.Z2ALogs = (() => {
  const pageSize = 20;
  const tabs = [['logs', '调用记录', 'scroll'], ['sched', '调度时间线', 'timer'], ['audit', '审计', 'shield']];
  const paths = { logs: '/api/usage/logs', sched: '/api/schedule-logs', audit: '/api/audit' };
  let closeActiveDetails = null;
  function closeDetails(restoreFocus = false) { closeActiveDetails?.(restoreFocus); }
  function errorText(row) {
    const message = String(row.error_message ?? '').trim();
    // Display the stored error code rather than deriving one from the status.
    const code = String(row.error_code ?? '').trim();
    if (!code) return message || '—';
    return !message || message === code ? code : `${message}（${code}）`;
  }
  function candidatesOf(row) {
    try {
      const value = typeof row.candidates === 'string' ? JSON.parse(row.candidates) : row.candidates;
      return Array.isArray(value) ? value : null;
    } catch { return null; }
  }
  function skipLabel(reason) {
    const labels = { excluded_for_request: '本次请求已排除', card_expiring: '额度卡即将到期', family_filter: '平台不匹配', model_not_available: '模型不可用', 'quota_remaining=0': '账号额度不足', cooldown: '账号冷却中', upstream_busy: '账号并发已满' };
    if (labels[reason]) return labels[reason];
    if (String(reason).startsWith('status=')) {
      const status = String(reason).slice(7);
      return ({ needs_relogin: '需要重新登录', unverified: '账号待验证', web_only: '仅网页会话', expired: '账号已到期', disabled: '账号已禁用', cooldown: '账号冷却中' })[status] || '账号状态：' + status;
    }
    return String(reason || '未选中');
  }
  const fields = ['q', 'hours', 'status', 'error_code', 'model', 'account', 'key', 'policy', 'outcome', 'actor', 'action', 'ip', 'since', 'until'];
  const defaults = () => Object.fromEntries(fields.map(name => [name, ['q', 'since', 'until'].includes(name) ? '' : 'all']));
  const state = () => {
    globalThis._logFilters ||= {};
    globalThis._logPage ||= {};
    globalThis._logSnapshots ||= {};
    globalThis._logTab ||= 'logs';
  };
  function filter(tab) { state(); return globalThis._logFilters[tab] ||= defaults(); }
  function routeValues() {
    state();
    const tab = globalThis._logTab;
    const values = { tab, page: globalThis._logPage[tab] || 1, ...globalThis._logSnapshots[tab] };
    for (const [name, value] of Object.entries(filter(tab))) if (value && value !== 'all') values[name] = value;
    return values;
  }
  function restoreRoute(query) {
    state();
    const tab = tabs.some(([id]) => id === query.tab) ? query.tab : 'logs';
    globalThis._logTab = tab;
    const f = defaults();
    for (const key of fields) if (query[key] !== undefined) f[key] = String(query[key]).slice(0, key === 'q' ? 200 : 2000);
    if (!['all', '24', '168', '720'].includes(f.hours)) f.hours = 'all';
    if (f.status === 'error') f.status = 'failed';
    if (!['all', 'success', 'busy', 'failed', 'canceled', 'non_success'].includes(f.status)) f.status = 'all';
    globalThis._logFilters[tab] = f;
    globalThis._logPage[tab] = Math.max(1, Math.floor(Number(query.page)) || 1);
    delete globalThis._logSnapshots[tab];
    if (/^\d+$/.test(query.snapshot || '') && /^\d+$/.test(query.as_of || '')) globalThis._logSnapshots[tab] = { snapshot: query.snapshot, as_of: query.as_of };
  }
  function pageNumbers(current, pages) {
    const set = new Set([1, pages]);
    for (let n = Math.max(1, current - 2); n <= Math.min(pages, current + 2); n++) set.add(n);
    const values = [...set].sort((a, b) => a - b), result = [];
    for (const n of values) { if (result.length && n - result.at(-1) > 1) result.push('…'); result.push(n); }
    return result;
  }
  function canAutoRefresh(main) {
    state();
    const doc = main.ownerDocument, form = main.querySelector('#log-filters');
    if (globalThis._logLive === false || (globalThis._logPage[globalThis._logTab] || 1) !== 1 || doc.hidden || !form) return false;
    if (closeActiveDetails || form.contains(doc.activeElement) || main.querySelector('.mobile-log[open]') || doc.getSelection()?.toString()) return false;
    const f = filter(globalThis._logTab);
    return [...new FormData(form)].every(([name, value]) => String(value).trim() === String(f[name] || '')) && form.checkValidity();
  }
  function refreshFailed(main) {
    const button = main.querySelector('#log-live');
    if (!button) return;
    button.classList.add('is-reconnecting');
    button.title = '刷新失败，正在重连；当前记录已保留';
    button.lastChild.textContent = '重连中';
  }
  function updateLiveButton(main) {
    const button = main.querySelector('#log-live');
    if (!button) return;
    const live = globalThis._logLive !== false && (globalThis._logPage[globalThis._logTab] || 1) === 1;
    button.classList.remove('is-reconnecting');
    button.setAttribute('aria-pressed', String(live));
    button.title = live ? '每 3 秒刷新；编辑筛选、查看详情时暂缓' : globalThis._logLive === false ? '开启实时刷新' : '查看历史时暂停；点击返回最新记录';
    button.lastChild.textContent = live ? '实时' : '已暂停';
  }
  function queryFor(tab, fresh = false) {
    state();
    const f = filter(tab), query = new URLSearchParams({ page: globalThis._logPage[tab] || 1 });
    const relevant = ['q', 'hours', 'since', 'until', ...(tab === 'logs' ? ['status', 'error_code', 'model', 'account', 'key'] : tab === 'sched' ? ['policy', 'outcome', 'account'] : ['actor', 'action', 'ip'])];
    for (const name of relevant) {
      const value = f[name];
      if (!value || value === 'all') continue;
      if (name === 'since' || name === 'until') {
        const ts = new Date(value).getTime();
        if (Number.isFinite(ts)) query.set(name, ts);
      } else query.set(name, value);
    }
    if (!fresh) for (const [name, value] of Object.entries(globalThis._logSnapshots[tab] || {})) query.set(name, value);
    return paths[tab] + '?' + query;
  }

  async function render(main, helpers, options = {}) {
    state();
    if (options.live && !canAutoRefresh(main)) return;
    if (!options.live) closeDetails();
    const { api, esc, icon, pagehead, fmtTime, fmtCompactTime, fmtInt, policyLabel, rerender } = helpers;
    const tab = globalThis._logTab, url = queryFor(tab, options.live);
    const previousPanel = main.querySelector('#log-panel');
    const active = main.ownerDocument.activeElement;
    const focused = active?.matches('#lf-q') ? { start: active.selectionStart, end: active.selectionEnd } : null;
    const advancedOpen = main.querySelector('#log-advanced')?.open;
    const data = await api(url);
    if (globalThis._logTab !== tab || queryFor(tab, options.live) !== url) return;
    if (options.live && (!previousPanel?.isConnected || !canAutoRefresh(main))) return;
    const fingerprint = JSON.stringify([tab, data.items, data.total, data.pages, data.summary, data.filters]);
    if (options.live && main.dataset.logFingerprint === fingerprint) { updateLiveButton(main); return; }
    closeDetails();
    const view = main.ownerDocument.defaultView, scroll = { x: view.scrollX, y: view.scrollY };
    const focusedID = options.live && main.contains(view.document.activeElement) ? view.document.activeElement.id : '';
    main.dataset.logFingerprint = fingerprint;
    globalThis._logPage[tab] = data.page;
    globalThis._logSnapshots[tab] = { snapshot: data.snapshot, as_of: data.as_of };
    const f = filter(tab), rows = data.items, choices = data.filters || {}, summary = data.summary || {};
    const statusLabel = value => ({ success: '成功', busy: '忙碌', canceled: '已取消', failed: '失败', error: '失败' })[value] || '失败';
    const badge = value => `<span class="badge ${value === 'success' ? 'ok' : value === 'busy' ? 'busy' : value === 'canceled' ? 'plain' : 'err'}">${statusLabel(value)}</span>`;
    const select = (name, label, options, format = x => x) => {
      const values = options.map(option => typeof option === 'string' ? { value: option, label: option } : option);
      if (f[name] !== 'all' && !values.some(option => option.value === f[name])) values.push({ value: f[name], label: f[name] });
      return `<label class="log-filter${name === 'error_code' ? ' log-filter-wide' : ''}"><span>${label}</span><select id="lf-${name}" name="${name}" aria-label="${label}"><option value="all" ${f[name] === 'all' ? 'selected' : ''}>全部</option>${values.map(option => `<option value="${esc(option.value)}" title="${esc(option.value)}" ${f[name] === option.value ? 'selected' : ''}>${esc(format(option.label))}</option>`).join('')}</select></label>`;
    };
    const common = `<label class="log-filter log-filter-search"><span>关键词</span><input type="search" id="lf-q" name="q" value="${esc(f.q)}" maxlength="200" autocomplete="off" placeholder="${tab === 'logs' ? '报错、错误码、请求 ID、账号、密钥、IP' : tab === 'sched' ? '请求 ID、账号、候选信息' : '动作、操作者、IP、详情'}"></label>` +
      select('hours', '时间范围', [{ value: '24', label: '近 24 小时' }, { value: '168', label: '近 7 天' }, { value: '720', label: '近 30 天' }]);
    const specific = tab === 'logs' ?
      select('status', '调用状态', [{ value: 'success', label: '成功' }, { value: 'busy', label: '忙碌' }, { value: 'failed', label: '失败' }, { value: 'canceled', label: '已取消' }, { value: 'non_success', label: '全部异常' }]) +
      select('error_code', '错误类型 / 错误码', choices.errors || []) : tab === 'sched' ?
      select('outcome', '调度结果', [{ value: 'picked', label: '已选中账号' }, { value: 'unpicked', label: '未选中账号' }]) + select('policy', '调度策略', choices.policies || [], policyLabel) :
      select('actor', '操作者', choices.actors || []) + select('action', '审计动作', choices.actions || []);
    const advancedFields = tab === 'logs' ? ['model', 'account', 'key', 'since', 'until'] : tab === 'sched' ? ['account', 'since', 'until'] : ['ip', 'since', 'until'];
    const selectedAdvanced = advancedFields.filter(name => f[name] && f[name] !== 'all').length;
    const showAdvanced = advancedOpen ?? selectedAdvanced > 0;
    const advanced = (tab === 'logs' ? select('model', '模型', choices.models || []) + select('account', '账号', choices.accounts || []) + select('key', '密钥', choices.keys || []) : tab === 'sched' ? select('account', '选中账号', choices.accounts || []) : select('ip', '来源 IP', choices.ips || [])) +
      `<label class="log-filter log-filter-date"><span>开始时间</span><input type="datetime-local" step="1" name="since" id="lf-since" value="${esc(f.since)}" aria-label="开始时间"></label><label class="log-filter log-filter-date"><span>结束时间</span><input type="datetime-local" step="1" name="until" id="lf-until" value="${esc(f.until)}" aria-label="结束时间"></label>`;
    const filterIcon = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M4 7h16M4 17h16M8 4v6M16 14v6"/></svg>';
    const chevron = '<svg class="log-filter-chevron" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>';
    const form = `<form id="log-filters" class="log-filter-card"><div class="log-filter-grid log-primary-filters">${common}${specific}</div><details id="log-advanced" class="log-advanced" ${showAdvanced ? 'open' : ''}><summary class="log-advanced-trigger" aria-expanded="${showAdvanced}" aria-controls="log-advanced-fields">${filterIcon}<span class="log-advanced-label">${showAdvanced ? '收起筛选' : '更多筛选'}</span>${selectedAdvanced ? `<span class="log-filter-count" title="已选 ${selectedAdvanced} 项">${selectedAdvanced}</span>` : ''}${chevron}</summary><div id="log-advanced-fields" class="log-filter-grid">${advanced}</div></details><div class="log-filter-actions"><span id="lf-error" role="alert"></span><button type="button" class="btn" id="lf-reset">重置</button><button type="submit" class="btn primary">筛选</button></div></form>`;
    const pages = data.pages, page = data.page, total = data.total;
    const start = total ? (page - 1) * pageSize + 1 : 0, end = Math.min(page * pageSize, total);
    const pager = `<nav class="pager usage-pager" aria-label="${tabs.find(([id]) => id === tab)[1]}分页"><span class="pinfo">第 ${fmtInt(start)}–${fmtInt(end)} 条 / 共 ${fmtInt(total)} 条 · 每页 20 条</span><button type="button" data-pg="1" aria-label="首页" ${page === 1 ? 'disabled' : ''}>«</button><button type="button" data-pg="${page - 1}" aria-label="上一页" ${page === 1 ? 'disabled' : ''}>‹</button>${pageNumbers(page, pages).map(n => n === '…' ? '<span aria-hidden="true">…</span>' : `<button type="button" data-pg="${n}" aria-label="第 ${n} 页" ${n === page ? 'class="cur" aria-current="page"' : ''}>${n}</button>`).join('')}<button type="button" data-pg="${page + 1}" aria-label="下一页" ${page === pages ? 'disabled' : ''}>›</button><button type="button" data-pg="${pages}" aria-label="末页" ${page === pages ? 'disabled' : ''}>»</button><form id="log-page-jump"><label>跳至 <input name="page" type="number" min="1" max="${pages}" value="${page}" required aria-label="跳转页码"> 页</label><button class="btn" type="submit">跳转</button></form></nav>`;
    let table = '', strip = '';
    if (tab === 'logs') {
      strip = `<div class="overview-strip logs-strip"><div><span>匹配调用</span><strong>${fmtInt(total)}</strong></div><div><span>成功 / 忙碌</span><strong>${fmtInt(summary.success)}<small> / ${fmtInt(summary.busy)}</small></strong></div><div><span>失败 / 已取消</span><strong>${fmtInt(summary.failed)}<small> / ${fmtInt(summary.canceled)}</small></strong></div><div><span>首 Token 时间 P50</span><strong>${summary.p50 == null ? '—' : fmtInt(summary.p50) + '<small> ms</small>'}</strong></div><div><span>Token 消耗</span><strong>${fmtInt(summary.tokens)}</strong></div></div>`;
      const error = row => `<span class="log-error-message">${esc(errorText(row))}</span>`;
      const clipped = value => `<span class="log-cell-clip" title="${esc(value)}">${esc(value)}</span>`;
      table = `<div class="tablecard log-desktop"><table><colgroup><col class="log-col-time"><col class="log-col-key"><col class="log-col-account"><col class="log-col-model"><col class="log-col-status"><col class="log-col-attempts"><col class="log-col-ttft"><col class="log-col-tokens"><col class="log-col-error"></colgroup><thead><tr><th>时间</th><th>密钥</th><th>账号</th><th>模型</th><th>状态</th><th>尝试</th><th>首 Token <small>ms</small></th><th>Token 用量</th><th>报错内容</th></tr></thead><tbody>${rows.map(row => `<tr><td class="mono">${fmtTime(row.ts)}</td><td>${clipped(row.key_name || row.key_id || '—')}</td><td>${clipped(row.account_label || row.account_id || '—')}</td><td>${clipped(row.model || '—')}</td><td>${badge(row.status)}</td><td>${fmtInt(row.attempt_count)}</td><td>${fmtInt(row.ttft_ms)}</td><td>${fmtInt((row.tokens_in || 0) + (row.tokens_out || 0))}</td><td>${error(row)}</td></tr>`).join('')}</tbody></table></div>` +
        `<div class="log-mobile">${rows.map(row => `<details class="mobile-log"><summary><span class="mobile-log-heading"><strong>${esc(row.model || '未知模型')}</strong>${badge(row.status)}</span><span class="mobile-log-time">${fmtCompactTime(row.ts)}<span>查看详情</span></span>${errorText(row) !== '—' ? `<span class="mobile-log-error">${esc(errorText(row))}</span>` : ''}</summary><dl><div><dt>账号</dt><dd>${esc(row.account_label || row.account_id || '—')}</dd></div><div><dt>密钥</dt><dd>${esc(row.key_name || row.key_id || '—')}</dd></div><div><dt>尝试次数</dt><dd>${fmtInt(row.attempt_count)}</dd></div><div><dt>首 Token 时间</dt><dd>${row.ttft_ms == null ? '—' : fmtInt(row.ttft_ms) + ' ms'}</dd></div><div><dt>Token 用量</dt><dd>${fmtInt((row.tokens_in || 0) + (row.tokens_out || 0))}</dd></div><div><dt>请求 ID</dt><dd>${esc(row.request_id)}</dd></div><div><dt>追踪 ID</dt><dd>${esc(row.trace_id || '—')}</dd></div></dl></details>`).join('')}</div>`;
    } else if (tab === 'sched') {
      table = `<div class="tablecard"><table><thead><tr><th>时间</th><th>请求 ID</th><th>策略</th><th>调度结果</th><th>选中账号</th><th>候选信息</th></tr></thead><tbody>${rows.map((row, index) => {
        const candidates = candidatesOf(row);
        return `<tr><td class="mono">${fmtTime(row.ts)}</td><td class="mono">${esc(row.request_id)}</td><td>${esc(policyLabel(row.policy))}</td><td>${row.picked ? '已选中' : '未选中'}</td><td>${esc(row.account_label || row.picked || '—')}</td><td><button type="button" class="log-detail-trigger log-candidate-trigger" data-log-detail="${index}" aria-label="查看候选信息" aria-controls="log-detail-popover" aria-expanded="false">候选 <span>${candidates ? candidates.length : '—'}</span></button></td></tr>`;
      }).join('')}</tbody></table></div>`;
    } else {
      table = `<div class="tablecard"><table><thead><tr><th>时间</th><th>动作</th><th>操作者</th><th>来源 IP</th><th>详情</th></tr></thead><tbody>${rows.map(row => `<tr><td class="mono">${fmtTime(row.ts)}</td><td>${esc(row.action)}</td><td>${esc(row.actor)}</td><td>${esc(row.ip || '—')}</td><td class="log-audit-detail">${esc(row.detail || '—')}</td></tr>`).join('')}</tbody></table></div>`;
    }
    main.innerHTML = `${pagehead('请求日志', '日志持续保存 · 查询全部历史 · 每页 20 条', `<button type="button" class="btn log-live" id="log-live" aria-label="实时刷新" aria-pressed="true"><span>实时</span></button><button class="btn" id="log-refresh">${icon('refresh', 14)}刷新</button>`)}<nav class="section-tabs log-tabs" role="tablist" aria-label="日志分区">${tabs.map(([id, label, glyph]) => `<button type="button" class="tab${tab === id ? ' active' : ''}" data-ltab="${id}" id="log-tab-${id}" role="tab" aria-controls="log-panel" aria-selected="${tab === id}" tabindex="${tab === id ? 0 : -1}">${icon(glyph, 13)}${label}</button>`).join('')}</nav><div role="tabpanel" id="log-panel" aria-labelledby="log-tab-${tab}">${form}${strip}${rows.length ? table : '<div class="empty">当前筛选条件下暂无记录</div>'}${pager}</div>`;
    const $ = selector => main.querySelector(selector);
    const advancedDetails = $('#log-advanced');
    advancedDetails.addEventListener('toggle', () => {
      const trigger = advancedDetails.querySelector('summary');
      trigger.setAttribute('aria-expanded', String(advancedDetails.open));
      trigger.querySelector('.log-advanced-label').textContent = advancedDetails.open ? '收起筛选' : '更多筛选';
    });
    if (tab === 'sched') {
      const title = '候选账号';
      main.insertAdjacentHTML('beforeend', `<aside id="log-detail-popover" class="log-detail-card" role="dialog" aria-labelledby="log-detail-title" tabindex="-1" hidden><div class="log-detail-heading"><strong id="log-detail-title">${title}</strong><button type="button" class="log-detail-close" aria-label="关闭${title}">×</button></div><div class="log-detail-content"></div></aside>`);
      const panel = $('#log-detail-popover');
      const nativePopover = typeof panel.showPopover === 'function';
      if (nativePopover) panel.setAttribute('popover', 'auto');
      panel.querySelector('.log-detail-close').addEventListener('click', () => closeDetails(true));
      main.querySelectorAll('[data-log-detail]').forEach(button => button.addEventListener('click', () => {
        if (button.getAttribute('aria-expanded') === 'true') { closeDetails(); return; }
        closeDetails();
        const row = rows[Number(button.dataset.logDetail)];
        const candidates = candidatesOf(row);
        panel.querySelector('#log-detail-title').textContent = candidates ? `候选账号（${candidates.length}）` : '候选账号';
        panel.querySelector('.log-detail-content').innerHTML = !candidates ? '<p class="log-candidate-empty">候选信息格式无法识别</p>' : !candidates.length ? '<p class="log-candidate-empty">此轮没有候选账号</p>' : `<div class="log-candidate-list">${candidates.map(candidate => {
          if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) return '<div class="log-candidate-empty">候选信息不完整</div>';
          const label = candidate.label || candidate.account || '未知账号';
          const result = candidate.picked ? '已选中' : skipLabel(candidate.skip);
          return `<div class="log-candidate-item"><div class="log-candidate-head"><strong>${esc(label)}</strong><span class="badge ${candidate.picked ? 'ok' : candidate.skip === 'upstream_busy' ? 'busy' : 'plain'}">${esc(result)}</span></div>${candidate.account && candidate.label ? `<div class="log-candidate-id">${esc(candidate.account)}</div>` : ''}</div>`;
        }).join('')}</div>`;
        panel.style.width = Math.min(360, innerWidth - 24) + 'px';
        panel.hidden = false;
        if (nativePopover) panel.showPopover();
        const rect = button.getBoundingClientRect(), height = panel.getBoundingClientRect().height, width = panel.getBoundingClientRect().width;
        panel.style.left = Math.max(12, Math.min(rect.right - width, innerWidth - width - 12)) + 'px';
        panel.style.top = (rect.bottom + height + 20 <= innerHeight ? rect.bottom + 8 : Math.max(12, rect.top - height - 8)) + 'px';
        button.setAttribute('aria-expanded', 'true');
        const events = new AbortController();
        const close = restoreFocus => {
          events.abort();
          if (nativePopover && panel.matches(':popover-open')) panel.hidePopover();
          panel.hidden = true;
          button.setAttribute('aria-expanded', 'false');
          if (closeActiveDetails === close) closeActiveDetails = null;
          if (restoreFocus && button.isConnected) button.focus({ preventScroll: true });
        };
        closeActiveDetails = close;
        const doc = main.ownerDocument, view = doc.defaultView, signal = events.signal;
        doc.addEventListener('pointerdown', event => { if (!panel.contains(event.target) && !button.contains(event.target)) close(false); }, { capture: true, signal });
        doc.addEventListener('keydown', event => { if (event.key === 'Escape') { event.preventDefault(); close(true); } }, { signal });
        view.addEventListener('scroll', event => { if (!panel.contains(event.target)) close(false); }, { capture: true, signal });
        view.addEventListener('resize', () => close(false), { signal });
        panel.addEventListener('toggle', event => { if (event.newState === 'closed') close(false); }, { signal });
        panel.focus({ preventScroll: true });
      }));
    }
    const refresh = () => rerender();
    const filtersForm = $('#log-filters'), search = $('#lf-q');
    let searchTimer, composing = false;
    function applyFilters() {
      clearTimeout(searchTimer);
      if (!filtersForm.isConnected || composing || !filtersForm.reportValidity()) return;
      const next = defaults();
      for (const [name, value] of new FormData(filtersForm)) next[name] = value;
      next.q = next.q.trim();
      const start = Date.parse(next.since), end = Date.parse(next.until);
      if (Number.isFinite(start) && Number.isFinite(end) && start > end) { $('#lf-error').textContent = '结束时间不能早于开始时间'; return; }
      globalThis._logFilters[tab] = next;
      globalThis._logPage[tab] = 1;
      delete globalThis._logSnapshots[tab];
      refresh();
    }
    filtersForm.addEventListener('submit', event => { event.preventDefault(); applyFilters(); });
    filtersForm.addEventListener('change', event => {
      if (['since', 'until'].includes(event.target.name) && event.target.value) $('#lf-hours').value = 'all';
      applyFilters();
    });
    search.addEventListener('compositionstart', () => { composing = true; clearTimeout(searchTimer); });
    search.addEventListener('compositionend', () => { composing = false; searchTimer = setTimeout(applyFilters, 300); });
    search.addEventListener('input', () => { clearTimeout(searchTimer); if (!composing) searchTimer = setTimeout(applyFilters, 300); });
    $('#lf-reset').addEventListener('click', () => { clearTimeout(searchTimer); globalThis._logFilters[tab] = defaults(); globalThis._logPage[tab] = 1; delete globalThis._logSnapshots[tab]; refresh(); });
    $('#log-refresh').addEventListener('click', () => { clearTimeout(searchTimer); delete globalThis._logSnapshots[tab]; globalThis._logPage[tab] = 1; refresh(); });
    updateLiveButton(main);
    $('#log-live').addEventListener('click', () => {
      globalThis._logLive = $('#log-live').getAttribute('aria-pressed') !== 'true';
      if (globalThis._logLive) { clearTimeout(searchTimer); delete globalThis._logSnapshots[tab]; globalThis._logPage[tab] = 1; refresh(); }
      else updateLiveButton(main);
    });
    const changeTab = id => { clearTimeout(searchTimer); globalThis._logTab = id; Promise.resolve(refresh()).then(() => main.querySelector(`[data-ltab="${id}"]`)?.focus()); };
    main.querySelectorAll('[data-ltab]').forEach(button => button.addEventListener('click', () => { if (button.dataset.ltab !== tab) changeTab(button.dataset.ltab); }));
    main.querySelector('[role=tablist]').addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const index = tabs.findIndex(([id]) => id === tab);
      changeTab(tabs[event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length][0]);
    });
    main.querySelectorAll('[data-pg]').forEach(button => button.addEventListener('click', () => { if (!button.disabled && Number(button.dataset.pg) !== page) { clearTimeout(searchTimer); globalThis._logPage[tab] = Number(button.dataset.pg); refresh(); } }));
    $('#log-page-jump').addEventListener('submit', event => { event.preventDefault(); if (!event.currentTarget.reportValidity()) return; globalThis._logPage[tab] = Number(new FormData(event.currentTarget).get('page')); refresh(); });
    if (focused) { search.focus(); search.setSelectionRange(focused.start, focused.end); }
    else if (focusedID) main.ownerDocument.getElementById(focusedID)?.focus({ preventScroll: true });
    if (options.live && (view.scrollX !== scroll.x || view.scrollY !== scroll.y)) view.scrollTo({ left: scroll.x, top: scroll.y, behavior: 'instant' });
  }
  return Object.freeze({ pageSize, pageNumbers, routeValues, restoreRoute, queryFor, render, closeDetails, canAutoRefresh, refreshFailed });
})();
