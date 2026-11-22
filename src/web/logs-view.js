'use strict';

globalThis.Z2ALogs = (() => {
  const fields = ['q', 'hours', 'status', 'error_code', 'model', 'account', 'key', 'since', 'until'];
  const defaults = () => Object.fromEntries(fields.map(name => [name, ['q', 'since', 'until'].includes(name) ? '' : 'all']));
  let filters = defaults(), logPage = 1, logSnapshot = null, logLive = true;
  function errorText(row) {
    const summary = String(row.error_message ?? '').trim();
    const detail = String(row.error_detail?.message ?? '').trim();
    let message = String(row.error_reason || summary || detail).trim();
    const code = String(row.error_code ?? '').trim();
    const upstreamCode = String(row.error_detail?.upstream_code ?? '').trim();
    const status = Number(row.error_detail?.http_status);
    const sdkStatus = Number(row.error_detail?.sdk_http_status);
    const identifiers = [];
    if (Number.isInteger(status) && status >= 100 && status <= 599) identifiers.push(`${row.error_detail?.local_cooldown ? '上次上游' : '上游'} HTTP ${status}`);
    else if (Number.isInteger(sdkStatus) && sdkStatus >= 100 && sdkStatus <= 599) identifiers.push(`SDK HTTP ${sdkStatus}`);
    else if (row.error_detail?.local_cooldown) identifiers.push('本地冷却拦截');
    else if (row.error_detail?.upstream_response_received === false) identifiers.push(row.error_detail.upstream_attempted ? '未收到上游 HTTP 响应' : '未发起推理请求');
    else if (code || message) identifiers.push('HTTP 状态未记录');
    if (upstreamCode && upstreamCode !== code) identifiers.push(`上游码 ${upstreamCode}`);
    const sdkCode = row.error_detail?.verify_code || row.error_detail?.sdk_error_code || row.error_detail?.sdk_code;
    if (sdkCode && sdkCode !== code && sdkCode !== upstreamCode) identifiers.push(`SDK ${sdkCode}`);
    if (code) {
      const suffix = `（${code}）`;
      if (message.endsWith(suffix)) message = message.slice(0, -suffix.length).trim();
      identifiers.push(code);
    }
    if (!identifiers.length) return message || '—';
    return message && message !== code ? `${message}（${identifiers.join(' · ')}）` : identifiers.join(' · ');
  }
  function routeValues() {
    const values = { page: logPage, ...logSnapshot };
    for (const [name, value] of Object.entries(filters)) if (value && value !== 'all') values[name] = value;
    return values;
  }
  function restoreRoute(query) {
    if (query.tab && query.tab !== 'logs') query = {};
    const f = defaults();
    for (const key of fields) if (query[key] !== undefined) f[key] = String(query[key]).slice(0, key === 'q' ? 200 : 2000);
    if (!['all', '24', '168', '720'].includes(f.hours)) f.hours = 'all';
    if (f.status === 'error') f.status = 'failed';
    if (!['all', 'success', 'busy', 'failed', 'canceled', 'non_success'].includes(f.status)) f.status = 'all';
    filters = f;
    logPage = Math.max(1, Math.floor(Number(query.page)) || 1);
    logSnapshot = /^\d+$/.test(query.snapshot || '') && /^\d+$/.test(query.as_of || '') ? { snapshot: query.snapshot, as_of: query.as_of } : null;
  }
  function canAutoRefresh(main) {
    const doc = main.ownerDocument, form = main.querySelector('#log-filters');
    if (!logLive || logPage !== 1 || doc.hidden || !form) return false;
    if (form.contains(doc.activeElement) || main.querySelector('.mobile-log[open], .cache-detail-dialog[open]') || doc.getSelection()?.toString()) return false;
    return [...new FormData(form)].every(([name, value]) => String(value).trim() === String(filters[name] || '')) && form.checkValidity();
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
    const live = logLive && logPage === 1;
    button.classList.remove('is-reconnecting');
    button.setAttribute('aria-pressed', String(live));
    button.title = live ? '约每 1 秒刷新；编辑筛选、查看详情时暂缓' : !logLive ? '开启实时刷新' : '查看历史时暂停；点击返回最新记录';
    button.lastChild.textContent = live ? '实时' : '已暂停';
  }
  function queryFor(fresh = false) {
    const query = new URLSearchParams({ page: logPage });
    for (const [name, value] of Object.entries(filters)) {
      if (!value || value === 'all') continue;
      if (name === 'since' || name === 'until') {
        const ts = new Date(value).getTime();
        if (Number.isFinite(ts)) query.set(name, ts);
      } else query.set(name, value);
    }
    if (!fresh && logSnapshot) for (const [name, value] of Object.entries(logSnapshot)) query.set(name, value);
    return '/api/usage/logs?' + query;
  }

  async function render(main, helpers, options = {}) {
    if (options.live && !canAutoRefresh(main)) return;
    const { api, esc, icon, pagehead, fmtTime, fmtCompactTime, fmtInt, rerender } = helpers;
    const url = queryFor(options.live);
    const previousPanel = main.querySelector('#log-panel');
    const active = main.ownerDocument.activeElement;
    const focused = active?.matches('#lf-q') ? { start: active.selectionStart, end: active.selectionEnd } : null;
    const advancedOpen = main.querySelector('#log-advanced')?.open;
    const data = await api(url);
    if (queryFor(options.live) !== url) return;
    if (options.live && (!previousPanel?.isConnected || !canAutoRefresh(main))) return;
    const fingerprint = JSON.stringify([data.items, data.total, data.pages, data.page_size, data.summary, data.filters]);
    if (options.live && main.dataset.logFingerprint === fingerprint) { updateLiveButton(main); return; }
    const view = main.ownerDocument.defaultView, scroll = { x: view.scrollX, y: view.scrollY, tableX: main.querySelector('.log-calls')?.scrollLeft || 0 };
    const focusedID = options.live && main.contains(view.document.activeElement) ? view.document.activeElement.id : '';
    main.dataset.logFingerprint = fingerprint;
    logPage = data.page;
    logSnapshot = { snapshot: data.snapshot, as_of: data.as_of };
    const f = filters, rows = data.items, choices = data.filters || {}, summary = data.summary || {};
    const statusLabel = value => ({ success: '成功', busy: '忙碌', canceled: '已取消', failed: '失败', error: '失败' })[value] || '失败';
    const badge = value => `<span class="badge ${value === 'success' ? 'ok' : value === 'busy' ? 'busy' : value === 'canceled' ? 'plain' : 'err'}">${statusLabel(value)}</span>`;
    const select = (name, label, options) => {
      const values = options.map(option => typeof option === 'string' ? { value: option, label: option } : option);
      if (f[name] !== 'all' && !values.some(option => option.value === f[name])) values.push({ value: f[name], label: f[name] });
      return `<label class="log-filter${name === 'error_code' ? ' log-filter-wide' : ''}"><span>${label}</span><select id="lf-${name}" name="${name}" aria-label="${label}"><option value="all" ${f[name] === 'all' ? 'selected' : ''}>全部</option>${values.map(option => `<option value="${esc(option.value)}" title="${esc(option.value)}" ${f[name] === option.value ? 'selected' : ''}>${esc(option.label)}</option>`).join('')}</select></label>`;
    };
    const primary = `<label class="log-filter log-filter-search"><span>关键词</span><input type="search" id="lf-q" name="q" value="${esc(f.q)}" maxlength="200" autocomplete="off" placeholder="报错、错误码、请求 ID、账号、密钥、IP"></label>` +
      select('hours', '时间范围', [{ value: '24', label: '近 24 小时' }, { value: '168', label: '近 7 天' }, { value: '720', label: '近 30 天' }]) +
      select('status', '调用状态', [{ value: 'success', label: '成功' }, { value: 'busy', label: '忙碌' }, { value: 'failed', label: '失败' }, { value: 'canceled', label: '已取消' }, { value: 'non_success', label: '全部异常' }]) +
      select('error_code', '错误类型 / 错误码', choices.errors || []);
    const advancedFields = ['model', 'account', 'key', 'since', 'until'];
    const selectedAdvanced = advancedFields.filter(name => f[name] && f[name] !== 'all').length;
    const showAdvanced = advancedOpen ?? selectedAdvanced > 0;
    const advanced = select('model', '模型', choices.models || []) + select('account', '账号', choices.accounts || []) + select('key', '密钥', choices.keys || []) +
      `<label class="log-filter log-filter-date"><span>开始时间</span><input type="datetime-local" step="1" name="since" id="lf-since" value="${esc(f.since)}" aria-label="开始时间"></label><label class="log-filter log-filter-date"><span>结束时间</span><input type="datetime-local" step="1" name="until" id="lf-until" value="${esc(f.until)}" aria-label="结束时间"></label>`;
    const filterIcon = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M4 7h16M4 17h16M8 4v6M16 14v6"/></svg>';
    const form = `<form id="log-filters" class="log-filter-card control-compact"><div class="log-filter-grid log-primary-filters">${primary}</div><details id="log-advanced" class="log-advanced disclosure" ${showAdvanced ? 'open' : ''}><summary class="log-advanced-trigger control-trigger" aria-expanded="${showAdvanced}" aria-controls="log-advanced-fields">${filterIcon}<span>更多筛选</span>${selectedAdvanced ? `<span class="log-filter-count" title="已选 ${selectedAdvanced} 项">${selectedAdvanced}</span>` : ''}</summary><div id="log-advanced-fields" class="log-filter-grid">${advanced}</div></details><div class="log-filter-actions"><span id="lf-error" role="alert"></span><button type="button" class="btn" id="lf-reset">重置</button><button type="submit" class="btn primary">筛选</button></div></form>`;
    const { pages, page, total, page_size: pageSize } = data;
    const start = total ? (page - 1) * pageSize + 1 : 0, end = Math.min(page * pageSize, total);
    const pager = pages > 1 ? `<nav class="pager usage-pager" aria-label="调用记录分页"><span class="pinfo">${fmtInt(start)}–${fmtInt(end)} / ${fmtInt(total)} 条</span><button type="button" data-pg="1" aria-label="首页" ${page === 1 ? 'disabled' : ''}>«</button><button type="button" data-pg="${page - 1}" aria-label="上一页" ${page === 1 ? 'disabled' : ''}>‹</button>${Z2AUI.pageNumberButtons(page, pages, 'data-pg')}<button type="button" data-pg="${page + 1}" aria-label="下一页" ${page === pages ? 'disabled' : ''}>›</button><button type="button" data-pg="${pages}" aria-label="末页" ${page === pages ? 'disabled' : ''}>»</button><form id="log-page-jump"><input name="page" type="number" min="1" max="${pages}" value="${page}" required aria-label="跳转页码"><button class="btn" type="submit">跳转</button></form></nav>` : '';
    const strip = `<div class="overview-strip logs-strip"><div><span>匹配调用</span><strong>${fmtInt(total)}</strong></div><div><span>成功 / 忙碌</span><strong>${fmtInt(summary.success)}<small> / ${fmtInt(summary.busy)}</small></strong></div><div><span>失败 / 已取消</span><strong>${fmtInt(summary.failed)}<small> / ${fmtInt(summary.canceled)}</small></strong></div><div><span>首 Token 时间 P50</span><strong>${summary.p50 == null ? '—' : fmtInt(summary.p50) + '<small> ms</small>'}</strong></div><div><span>Token 消耗</span><strong>${fmtInt(summary.tokens)}</strong></div></div>`;
    const errorMessages = rows.map(errorText);
    const clipped = (value, className = 'log-cell-clip') => {
      const text = esc(value);
      return `<span class="${className}" title="${text}">${text}</span>`;
    };
    const cacheRate = row => {
      const rate = Z2ACacheDetails.hitRate(row);
      return rate == null ? '—' : rate.toFixed(1) + '%';
    };
    const details = (row, index) => {
      const available = row.request_id || row.trace_id || row.error_code || row.error_message || row.error_detail || ['tokens_in', 'tokens_out', 'cache_read', 'cache_write', 'ttft_ms'].some(field => row[field] != null);
      return `<button type="button" class="cache-open" ${available ? `data-cache-detail="${index}"` : 'disabled'} aria-label="${esc('查看 ' + (row.request_id || row.model || '请求') + ' 的详情')}">详情</button>`;
    };
    const cacheCell = (row, index) => `<span class="log-cache-inline"><span class="log-cache-percent">${cacheRate(row)}</span>${details(row, index)}</span>`;
    const tokenUsage = row => row.tokens_in == null && row.tokens_out == null ? '—' : fmtInt((row.tokens_in || 0) + (row.tokens_out || 0));
    const table = `<div class="tablecard log-table log-desktop log-calls"><table><colgroup><col span="9"><col class="log-col-error"></colgroup><thead><tr><th>时间</th><th>密钥</th><th>账号</th><th>模型</th><th>状态</th><th>尝试</th><th>首 Token <small>ms</small></th><th>Token 用量</th><th>缓存命中率</th><th>报错内容</th></tr></thead><tbody>${rows.map((row, index) => `<tr><td class="mono">${fmtTime(row.ts)}</td><td>${clipped(row.key_name || row.key_id || '—')}</td><td>${clipped(row.account_label || row.account_id || '—')}</td><td>${clipped(row.model || '—')}</td><td>${badge(row.status)}</td><td>${fmtInt(row.attempt_count)}</td><td>${fmtInt(row.ttft_ms)}</td><td>${tokenUsage(row)}</td><td>${cacheCell(row, index)}</td><td>${clipped(errorMessages[index], 'log-error-message')}</td></tr>`).join('')}</tbody></table></div>` +
      `<div class="log-mobile log-calls-mobile">${rows.map((row, index) => `<details class="mobile-log"><summary><span class="mobile-log-heading"><strong>${esc(row.model || '未知模型')}</strong>${badge(row.status)}</span><span class="mobile-log-time">${fmtCompactTime(row.ts)}${icon('chevron-right', 14)}</span>${errorMessages[index] !== '—' ? `<span class="mobile-log-error">${esc(errorMessages[index])}</span>` : ''}</summary><dl><div><dt>账号</dt><dd>${esc(row.account_label || row.account_id || '—')}</dd></div><div><dt>密钥</dt><dd>${esc(row.key_name || row.key_id || '—')}</dd></div><div><dt>尝试次数</dt><dd>${fmtInt(row.attempt_count)}</dd></div><div><dt>首 Token 时间</dt><dd>${row.ttft_ms == null ? '—' : fmtInt(row.ttft_ms) + ' ms'}</dd></div><div><dt>Token 用量</dt><dd>${tokenUsage(row)}</dd></div><div><dt>缓存命中率</dt><dd>${cacheCell(row, index)}</dd></div><div><dt>请求 ID</dt><dd>${esc(row.request_id)}</dd></div><div><dt>追踪 ID</dt><dd>${esc(row.trace_id || '—')}</dd></div></dl></details>`).join('')}</div>`;
    main.innerHTML = `${pagehead('请求日志', `<button type="button" class="btn log-live" id="log-live" aria-label="实时刷新" aria-pressed="true"><span>实时</span></button><button class="btn" id="log-refresh">${icon('refresh', 14)}刷新</button>`)}<div id="log-panel">${form}${strip}${rows.length ? table : '<div class="empty">暂无匹配记录</div>'}${pager}</div>`;
    Z2ACacheDetails.bind(main, rows, { ...helpers, badge });
    const $ = selector => main.querySelector(selector);
    const advancedDetails = $('#log-advanced');
    advancedDetails.addEventListener('toggle', () => {
      const trigger = advancedDetails.querySelector('summary');
      trigger.setAttribute('aria-expanded', String(advancedDetails.open));
    });
    const filtersForm = $('#log-filters'), search = $('#lf-q');
    let searchTimer, composing = false;
    function refreshFirstPage() {
      clearTimeout(searchTimer);
      logPage = 1;
      logSnapshot = null;
      rerender();
    }
    function applyFilters() {
      clearTimeout(searchTimer);
      if (!filtersForm.isConnected || composing || !filtersForm.reportValidity()) return;
      const next = defaults();
      for (const [name, value] of new FormData(filtersForm)) next[name] = value;
      next.q = next.q.trim();
      const start = Date.parse(next.since), end = Date.parse(next.until);
      if (Number.isFinite(start) && Number.isFinite(end) && start > end) { $('#lf-error').textContent = '结束时间不能早于开始时间'; return; }
      filters = next;
      refreshFirstPage();
    }
    filtersForm.addEventListener('submit', event => { event.preventDefault(); applyFilters(); });
    filtersForm.addEventListener('change', event => {
      if (['since', 'until'].includes(event.target.name) && event.target.value) $('#lf-hours').value = 'all';
      applyFilters();
    });
    search.addEventListener('compositionstart', () => { composing = true; clearTimeout(searchTimer); });
    search.addEventListener('compositionend', () => { composing = false; searchTimer = setTimeout(applyFilters, 300); });
    search.addEventListener('input', () => { clearTimeout(searchTimer); if (!composing) searchTimer = setTimeout(applyFilters, 300); });
    $('#lf-reset').addEventListener('click', () => { filters = defaults(); refreshFirstPage(); });
    $('#log-refresh').addEventListener('click', refreshFirstPage);
    updateLiveButton(main);
    $('#log-live').addEventListener('click', () => {
      logLive = $('#log-live').getAttribute('aria-pressed') !== 'true';
      if (logLive) refreshFirstPage();
      else updateLiveButton(main);
    });
    main.querySelectorAll('[data-pg]').forEach(button => button.addEventListener('click', () => {
      const target = Number(button.dataset.pg);
      if (button.disabled || target === page) return;
      clearTimeout(searchTimer);
      logPage = target;
      rerender();
    }));
    $('#log-page-jump')?.addEventListener('submit', event => {
      event.preventDefault();
      if (!event.currentTarget.reportValidity()) return;
      clearTimeout(searchTimer);
      logPage = Number(new FormData(event.currentTarget).get('page'));
      rerender();
    });
    if (focused) { search.focus(); search.setSelectionRange(focused.start, focused.end); }
    else if (focusedID) main.ownerDocument.getElementById(focusedID)?.focus({ preventScroll: true });
    main.querySelector('.log-calls')?.scrollTo({ left: scroll.tableX, behavior: 'instant' });
    if (options.live && (view.scrollX !== scroll.x || view.scrollY !== scroll.y)) view.scrollTo({ left: scroll.x, top: scroll.y, behavior: 'instant' });
  }
  return Object.freeze({ routeValues, restoreRoute, render, canAutoRefresh, refreshFailed });
})();
