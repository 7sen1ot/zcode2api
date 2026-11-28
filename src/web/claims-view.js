function claimablePlans(preview) {
  const plans = new Map();
  for (const plan of preview.plans || []) {
    if (Number(plan.ends_at) > 0 && Number(plan.ends_at) * 1000 <= Date.now()) continue;
    if (plan.plan_id && !plans.has(plan.plan_id)) plans.set(plan.plan_id, plan);
  }
  return [...plans.values()];
}

let claimCaptchaScript, claimCaptchaSequence = 0;
let claimBrowserVerificationQueue = Promise.resolve();
const claimJobKey = job => JSON.stringify([job.account_id, job.plan_id]);
const claimDay = (at = Date.now()) => new Date(Number(at) + 8 * 3600000).toISOString().slice(0, 10);
let claimResults = { day: '', items: new Map() };
const claimPhaseLabels = {
  queued: '等待领取…',
  config: '读取配置…',
  waiting: '等待验证…',
  preparing: '准备验证…',
  verifying: '验证中…',
  submitting: '正在提交…',
};
const claimNeedsVerification = result => result && !result.ok && result.requires_verification;

function manualClaimResults() {
  const day = claimDay();
  if (claimResults.day !== day) claimResults = { day, items: new Map() };
  return claimResults.items;
}

function claimResultMessage(result) {
  const code = String(result.code ?? '').trim();
  let message = String(result.error_reason || result.message || result.error || '领取失败');
  const detail = result.error_detail || result.verification?.upstream_error || {};
  const status = Number(result.http_status || detail.http_status || result.verification?.http_status || result.httpStatus);
  const sdkStatus = Number(detail.sdk_http_status);
  const gatewayStatus = Number(result.gateway_status);
  const identifiers = [];
  if (Number.isInteger(status) && status >= 100 && status <= 599) identifiers.push(`${detail.local_cooldown ? '上次上游' : '上游'} HTTP ${status}`);
  else if (Number.isInteger(sdkStatus) && sdkStatus >= 100 && sdkStatus <= 599) identifiers.push(`SDK HTTP ${sdkStatus}`);
  else if (Number.isInteger(gatewayStatus) && gatewayStatus >= 100 && gatewayStatus <= 599) identifiers.push(`网关 HTTP ${gatewayStatus}`);
  else if (detail.local_cooldown) identifiers.push('本地冷却拦截');
  else if (detail.claim_request_sent === false || result.verification?.claim_request_sent === false) identifiers.push('未发起领卡请求');
  else identifiers.push('HTTP 状态未记录');
  const sdkCode = detail.verify_code || detail.sdk_error_code || detail.sdk_code;
  if (sdkCode && sdkCode !== code) identifiers.push(`SDK ${sdkCode}`);
  if (code) {
    const suffix = `（${code}）`;
    if (message.endsWith(suffix)) message = message.slice(0, -suffix.length).trim();
    identifiers.push(code);
  }
  return `${message}${identifiers.length ? `（${identifiers.join(' · ')}）` : ''}`;
}

function loadClaimCaptchaScript() {
  if (typeof window.initAliyunCaptcha === 'function') return Promise.resolve();
  if (!claimCaptchaScript) {
    claimCaptchaScript = new Promise((resolve, reject) => {
      const script = document.createElement('script');
      const timeout = setTimeout(() => fail(), 15000);
      const fail = () => { clearTimeout(timeout); script.remove(); reject(new Error('验证组件加载失败，请检查网络后重试。')); };
      script.src = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js';
      script.async = true;
      script.onload = () => { clearTimeout(timeout); typeof window.initAliyunCaptcha === 'function' ? resolve() : fail(); };
      script.onerror = fail;
      document.head.appendChild(script);
    }).catch(error => { claimCaptchaScript = null; throw error; });
  }
  return claimCaptchaScript;
}

function requestClaimCaptcha(config, signal, { onStart, onFeedback, onDismiss, diagnostic = {} }) {
  if (signal.aborted) return Promise.resolve(null);
  return new Promise((resolve, reject) => {
    const mountID = `claim-captcha-${++claimCaptchaSequence}`;
    const host = h(`<div><div id="${mountID}"></div><button type="button" id="${mountID}-start" class="visually-hidden" tabindex="-1" aria-hidden="true"></button></div>`);
    const trigger = $('button', host);
    document.body.append(host);
    let instance, settled = false, traceless = false, waitingForChallenge = false, readyTimer, initializationTimer;
    const startedAt = performance.now();
    let initializedAt, readyAt, verificationStartedAt;
    Object.assign(diagnostic, { sdk_stage: 'sdk_load', sdk_initialized: false, sdk_executed: false });
    const validFor = config.expires_at - Date.now() - 1000;
    const fail = (message, code = 'captcha_runtime_error') => finish(null, Object.assign(new Error(message), { code }));
    const deadline = setTimeout(() => fail(
      validFor < 120000 ? '验证会话已过期，请重新领取。' : '验证超时，请重新领取。',
      'captcha_timeout',
    ), Math.max(0, Math.min(120000, validFor)));
    const cancel = () => finish(null);
    function finish(ticket, error) {
      if (settled) return;
      settled = true;
      const finishedAt = performance.now();
      diagnostic.sdk_total_ms = Math.round(finishedAt - startedAt);
      if (diagnostic.sdk_stage === 'sdk_load') diagnostic.sdk_load_ms = diagnostic.sdk_total_ms;
      if (diagnostic.sdk_stage === 'sdk_init') diagnostic.sdk_init_ms = Math.round(finishedAt - initializedAt);
      if (readyAt !== undefined) diagnostic.sdk_ready_wait_ms = Math.round((verificationStartedAt ?? finishedAt) - readyAt);
      diagnostic.browser_visibility = document.visibilityState;
      if (verificationStartedAt !== undefined) diagnostic.sdk_verify_ms = Math.round(finishedAt - verificationStartedAt);
      if (error && !error.code) error.code = 'captcha_runtime_error';
      clearTimeout(deadline);
      clearTimeout(readyTimer);
      clearTimeout(initializationTimer);
      signal.removeEventListener('abort', cancel);
      window.removeEventListener('pagehide', cancel);
      try { instance?.destroy?.(); } catch {}
      host.remove();
      if (error) reject(error);
      else resolve(ticket);
    }
    signal.addEventListener('abort', cancel, { once: true });
    window.addEventListener('pagehide', cancel, { once: true });
    window.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix };
    loadClaimCaptchaScript().then(() => {
      if (settled) return;
      diagnostic.sdk_load_ms = Math.round(performance.now() - startedAt);
      diagnostic.sdk_stage = 'sdk_init';
      initializedAt = performance.now();
      initializationTimer = setTimeout(() => fail('验证组件初始化超时，请重试。', 'captcha_timeout'), 15000);
      return window.initAliyunCaptcha({
        SceneId: config.scene_id,
        mode: 'popup', language: 'cn', element: `#${mountID}`, button: `#${mountID}-start`, delayBeforeSuccess: false,
        getInstance(value) {
          if (settled) { try { value?.destroy?.(); } catch {} return; }
          if (!value) { fail('验证组件初始化失败，请重试。'); return; }
          clearTimeout(initializationTimer);
          const firstInstance = !instance;
          instance = value;
          if (!firstInstance) return;
          diagnostic.sdk_initialized = true;
          diagnostic.sdk_init_ms = Math.round(performance.now() - initializedAt);
          diagnostic.sdk_stage = 'sdk_ready';
          readyAt = performance.now();
          traceless = typeof value.startTracelessVerification === 'function';
          // The SDK needs time to become ready. Its trigger also handles scenes without a challenge.
          readyTimer = setTimeout(() => {
            if (settled) return;
            if (document.hidden) {
              fail('请回到领卡页面后重试。', 'captcha_page_hidden');
              return;
            }
            diagnostic.sdk_ready_wait_ms = Math.round(performance.now() - readyAt);
            diagnostic.sdk_stage = 'sdk_verify';
            diagnostic.sdk_executed = true;
            verificationStartedAt = performance.now();
            onStart();
            trigger.click();
          }, 2100);
        },
        success(param) {
          if (settled) return;
          if (config.expires_at <= Date.now() + 1000) {
            fail('验证会话已过期，请重新领取。', 'captcha_expired');
          } else if (typeof param !== 'string' || !param.trim()) {
            fail('验证码结果无效，请重试。', 'captcha_invalid_result');
          } else {
            finish({ captcha_verify_param: param, captcha_region: config.region, verification_session: config.verification_session });
          }
        },
        fail(value) {
          if (settled) return;
          const result = value?.Result || value?.result || value || {};
          const code = String(result.VerifyCode || result.verifyCode || value?.verifyCode || '');
          if (/^[FT][0-9]{3}$/.test(code)) diagnostic.verify_code = code;
          // The first traceless refusal can lead to the SDK's own interactive challenge.
          if (code === 'F001' && traceless && !waitingForChallenge) {
            waitingForChallenge = true;
            onFeedback('等待浏览器验证，如出现验证码请完成。');
            return;
          }
          if (code === 'F015') {
            onFeedback('验证未通过，请在验证码中重试。', true);
            return;
          }
          const expired = ['F008', 'F014', 'F018', 'F019', 'F020'].includes(code);
          const suffix = /^[FT][0-9]{3}$/.test(code) ? `（${code}）` : '';
          finish(null, Object.assign(new Error(expired ? '验证会话已失效，请重新领取。' : `安全验证未通过${suffix}，请重试。`), { code: code || 'captcha_verification_failed' }));
        },
        onError(error) {
          if (settled) return;
          const code = String(error?.code || '');
          finish(null, Object.assign(new Error(`验证组件出错${/^[A-Za-z0-9_.-]{1,64}$/.test(code) ? `（${code}）` : ''}，请重试。`), { code: 'captcha_runtime_error' }));
        },
        onClose(reason) {
          if (settled || reason !== 'userDismiss') return;
          onDismiss();
          cancel();
        },
      });
    }).catch(error => finish(null, error));
  });
}

function queueClaimBrowserVerification(signal, verify) {
  const run = () => {
    if (signal.aborted) return null;
    return navigator.locks?.request
      ? navigator.locks.request('z2a-claim-verification', { signal }, verify)
      : verify();
  };
  const pending = claimBrowserVerificationQueue.then(run, run);
  claimBrowserVerificationQueue = pending.catch(() => {});
  // A skipped job can settle while it is still waiting for another tab or account.
  return new Promise((resolve, reject) => {
    const cancel = () => resolve(null);
    if (signal.aborted) cancel();
    else signal.addEventListener('abort', cancel, { once: true });
    pending.then(resolve, reject).finally(() => signal.removeEventListener('abort', cancel));
  });
}

function waitForClaimPage(signal) {
  const ready = () => !document.hidden && !document.querySelector('.modal-bg:not(.closing)');
  if (signal.aborted) return Promise.resolve(false);
  if (ready()) return Promise.resolve(true);
  return new Promise(resolve => {
    const observer = new MutationObserver(check);
    function finish(value) {
      observer.disconnect();
      document.removeEventListener('visibilitychange', check);
      signal.removeEventListener('abort', check);
      resolve(value);
    }
    function check() {
      if (signal.aborted) finish(false);
      else if (ready()) finish(true);
    }
    document.addEventListener('visibilitychange', check);
    signal.addEventListener('abort', check, { once: true });
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] });
    check();
  });
}

function claimAccountMarkup(preview, account = {}) {
  return claimablePlans(preview).map(plan => {
    const name = account.platform_name || account.label || '当前账号';
    const details = preview.stale ? `显示上次有效结果。${preview.error || ''}` : '';
    return `<div class="daily-claim-row${preview.stale ? ' query-stale' : ''}">
    ${accountNameMarkup({ ...account, id: preview.account_id }, 'claim-account')}
    <div class="daily-claim-status"><p class="daily-claim-feedback" role="status"></p>${details ? infoTip(details, '查询状态') : ''}</div>
    <div class="daily-claim-grants">${(plan.entitlements || []).map(entitlement => `<div><strong title="${esc(fmtInt(entitlement.grant_units))} ${esc(entitlement.unit_type || 'Token')}">${fmtQuota(entitlement.grant_units)}<small> ${esc(entitlement.unit_type || 'Token')}</small></strong><span title="${esc(entitlement.show_name || entitlement.meter || '')}">${esc(entitlement.show_name || entitlement.meter || '')}</span></div>`).join('')}</div>
    <div class="daily-claim-action"><button type="button" class="btn" data-claim-skip aria-label="取消 ${esc(name)} 的验证" hidden>取消验证</button><button type="button" class="btn primary" data-claim="${esc(preview.account_id)}" data-plan="${esc(plan.plan_id)}" aria-label="领取 ${esc(name)} 的每日免费 Token">领取</button></div>
  </div>`;
  }).join('');
}

const timePad2 = n => String(n).padStart(2, '0');
function normalizeDailyTime(value, fallback) {
  const digits = String(value || '').replace(/\D/g, '').slice(0, 4);
  if (!digits) return fallback || null;
  const splitAt = digits.length === 3 ? 1 : 2;
  const hour = Number(digits.length <= 2 ? digits : digits.slice(0, splitAt));
  const minute = digits.length <= 2 ? 0 : Number(digits.slice(splitAt).padEnd(2, '0'));
  return hour <= 23 && minute <= 59 ? `${timePad2(hour)}:${timePad2(minute)}` : null;
}
function formatTimeDraft(digits) {
  return digits.length <= 2 ? digits : `${digits.slice(0, 2)}:${digits.slice(2)}`;
}
// A custom HH:MM picker avoids platform differences in native time inputs.
function setupTimeField(main, hooks) {
  const field = $('#claims-time-field', main);
  const input = $('#claims-time', main);
  const trigger = $('#claims-time-trigger', main);
  const pop = $('#claims-time-pop', main);
  if (typeof pop.showPopover === 'function') pop.setAttribute('popover', 'manual');
  for (const [unit, count] of [['hour', 24], ['minute', 60]]) {
    $(`.time-field-col[data-unit="${unit}"]`, pop).innerHTML = Array.from({ length: count }, (_, i) =>
      `<button type="button" class="time-option" data-value="${timePad2(i)}" aria-label="${i} ${unit === 'hour' ? '点' : '分'}">${timePad2(i)}</button>`).join('');
  }
  const isOpen = () => !pop.hidden;
  const highlight = () => {
    const current = normalizeDailyTime(input.value, hooks.fallback()) || hooks.fallback();
    const [hour, minute] = current.split(':');
    for (const col of $$('.time-field-col', pop)) {
      const target = col.dataset.unit === 'hour' ? hour : minute;
      for (const option of $$('.time-option', col)) {
        const selected = option.dataset.value === target;
        option.classList.toggle('selected', selected);
        option.setAttribute('aria-pressed', String(selected));
        option.tabIndex = selected ? 0 : -1;
      }
    }
  };
  function open() {
    if (trigger.disabled) return;
    openSelection?.close();
    closeHint();
    pop.hidden = false;
    if (pop.hasAttribute('popover')) pop.showPopover();
    else document.body.appendChild(pop);
    openSelection = { trigger, wrapper: field, list: pop, close };
    trigger.setAttribute('aria-expanded', 'true');
    highlight();
    positionFloatingPanel(pop, field, true);
    for (const option of $$('.time-option.selected', pop)) {
      const col = option.parentElement;
      col.scrollTop = option.offsetTop - (col.clientHeight - option.offsetHeight) / 2;
    }
  }
  function close() {
    if (pop.hasAttribute('popover') && pop.matches(':popover-open')) pop.hidePopover();
    pop.hidden = true;
    if (pop.parentElement !== field) field.appendChild(pop);
    trigger.setAttribute('aria-expanded', 'false');
    if (openSelection?.trigger === trigger) openSelection = null;
  }
  async function commit() {
    const time = normalizeDailyTime(input.value, hooks.fallback());
    if (!time) { input.value = hooks.fallback(); toast('时间须为 00:00–23:59', 'err'); return; }
    input.value = time;
    if (time === hooks.fallback() || hooks.busy()) return;
    try { await hooks.save(time); }
    catch { input.value = hooks.fallback(); }
    finally { highlight(); }
  }
  trigger.addEventListener('click', () => (isOpen() ? close() : open()));
  // Keep focus in the field while picking, so blur-commit never races the click.
  pop.addEventListener('mousedown', event => event.preventDefault());
  pop.addEventListener('click', event => {
    const option = event.target.closest('.time-option');
    if (!option) return;
    const [hour, minute] = (normalizeDailyTime(input.value, hooks.fallback()) || hooks.fallback()).split(':');
    input.value = option.closest('.time-field-col').dataset.unit === 'hour'
      ? `${option.dataset.value}:${minute}` : `${hour}:${option.dataset.value}`;
    close();
    trigger.focus();
    commit();
  });
  trigger.addEventListener('keydown', event => {
    if (event.key === 'Escape' && isOpen()) { event.preventDefault(); event.stopPropagation(); close(); }
    else if (event.key === 'ArrowDown') { event.preventDefault(); if (!isOpen()) open(); $('.time-option.selected', pop)?.focus(); }
  });
  pop.addEventListener('keydown', event => {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); trigger.focus(); return; }
    const option = event.target.closest('.time-option');
    if (!option) return;
    const col = option.parentElement, options = $$('.time-option', col), index = options.indexOf(option);
    let next;
    if (event.key === 'ArrowUp') next = options[Math.max(0, index - 1)];
    else if (event.key === 'ArrowDown') next = options[Math.min(options.length - 1, index + 1)];
    else if (event.key === 'Home') next = options[0];
    else if (event.key === 'End') next = options.at(-1);
    else if (event.key === 'ArrowRight') next = col.nextElementSibling?.querySelector('.selected');
    else if (event.key === 'ArrowLeft') next = col.previousElementSibling?.querySelector('.selected');
    if (next) { event.preventDefault(); next.focus(); }
  });
  input.addEventListener('input', () => {
    const draft = formatTimeDraft(String(input.value).replace(/\D/g, '').slice(0, 4));
    if (input.value !== draft) input.value = draft;
    if (isOpen()) highlight();
  });
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); commit(); }
    else if (event.key === 'Escape') { input.value = hooks.fallback(); close(); }
    else if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      event.preventDefault();
      const [hour, minute] = (normalizeDailyTime(input.value, hooks.fallback()) || hooks.fallback()).split(':').map(Number);
      const total = ((hour * 60 + minute + (event.key === 'ArrowUp' ? 1 : -1)) % 1440 + 1440) % 1440;
      input.value = `${timePad2(Math.floor(total / 60))}:${timePad2(total % 60)}`;
      if (isOpen()) highlight();
    }
  });
  input.addEventListener('blur', () => { if (input.value !== hooks.fallback()) commit(); });
  return { highlight, close };
}

function pageClaims(main) {
  let previews = [], accounts = [], accountsError = '', batchRunning = false, loading = false, page = 1;
  let batchStopped = false, batchRemaining = 0;
  let schedule = null, scheduleSaving = false, scheduleError = '';
  let discoveryError = '', loadController;
  const pendingClaims = new Map(), claimQueue = [];
  let claimRunning = false;
  const pageSize = 8;
  main.innerHTML = `${pagehead('领卡中心', '<button type="button" class="btn" id="claims-refresh">' + icon('refresh', 14) + '刷新</button>')}
    <section class="daily-claim-card fade-up" aria-labelledby="daily-claim-title">
      <header class="daily-claim-header"><div class="daily-claim-title"><span class="daily-claim-icon">${icon('gift', 22)}</span><h2 id="daily-claim-title">每日免费 Token</h2></div><div class="daily-claim-actions"><div class="daily-claim-schedule"><span class="time-field control-compact" id="claims-time-field"><input id="claims-time" type="text" inputmode="numeric" autocomplete="off" spellcheck="false" maxlength="5" value="19:02" disabled aria-label="每日浏览器定时领取时间（北京时间）"><button type="button" class="time-field-trigger" id="claims-time-trigger" aria-label="选择定时领取时间" aria-expanded="false" aria-haspopup="dialog" aria-controls="claims-time-pop" disabled>${icon('timer', 13)}</button><span class="time-field-pop control-compact" id="claims-time-pop" hidden role="dialog" aria-label="选择定时领取时间"><span class="time-field-col" data-unit="hour" role="group" aria-label="小时"></span><span class="time-field-col" data-unit="minute" role="group" aria-label="分钟"></span></span></span><label for="claims-schedule">定时领取（北京时间）<input id="claims-schedule" type="checkbox" role="switch" disabled></label><span id="claims-schedule-info">${infoTip('服务需保持运行，浏览器依赖在「设置 → 上游安全验证」配置。', '定时领取')}</span></div><button type="button" class="btn" id="claims-stop" hidden>停止剩余领取</button><button type="button" class="btn primary" id="claims-all" disabled>全部领取</button></div></header>
      <div id="claims-content" class="daily-claim-list" aria-busy="true"><div class="skeleton-block"></div></div>
      <footer class="daily-claim-footer"><span id="claims-update" class="result-count" aria-live="polite">正在查询…</span><span id="claims-summary" class="result-count" role="status"></span><div class="daily-claim-footer-end"><div id="claims-notice"></div><nav class="pager" id="claims-pages" aria-label="可领取账号分页" hidden></nav></div></footer>
    </section>`;
  const content = $('#claims-content', main);
  const token = TOKEN;
  const active = () => main.contains(content) && TOKEN === token && !$('#app-view').classList.contains('hidden');
  const visiblePreviews = () => {
    const records = manualClaimResults();
    return previews.map(preview => ({ ...preview, plans: (preview.plans || []).filter(plan => !records.get(claimJobKey({ account_id: preview.account_id, plan_id: plan.plan_id }))?.ok) }));
  };
  const jobs = () => visiblePreviews().flatMap(preview => claimablePlans(preview).map(plan => ({ account_id: preview.account_id, plan_id: plan.plan_id })));
  const accountFor = id => accounts.find(account => account.id === id) || { label: previews.find(preview => preview.account_id === id)?.label };
  const syncBusy = () => {
    if (!active()) return;
    const busy = batchRunning || pendingClaims.size > 0;
    const pendingAccounts = new Set([...pendingClaims.values()].map(entry => entry.job.account_id));
    const results = manualClaimResults();
    const total = busy ? 0 : jobs().length;
    const refreshButton = $('#claims-refresh', main);
    refreshButton.disabled = busy || loading || scheduleSaving;
    refreshButton.classList.toggle('loading', loading);
    refreshButton.setAttribute('aria-busy', String(loading));
    $('#claims-schedule', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time-trigger', main).disabled = busy || loading || scheduleSaving || !schedule;
    const allButton = $('#claims-all', main);
    allButton.disabled = busy || loading || scheduleSaving || !total;
    allButton.textContent = busy ? `正在领取（剩余 ${batchRunning ? batchRemaining : pendingClaims.size}）` : total ? `全部领取（${total}）` : '全部领取';
    const stop = $('#claims-stop', main);
    stop.hidden = !batchRunning;
    stop.disabled = batchStopped;
    $$('button[data-claim]', content).forEach(button => {
      const accountID = button.dataset.claim, key = claimJobKey({ account_id: accountID, plan_id: button.dataset.plan });
      const entry = pendingClaims.get(key);
      const result = results.get(key);
      const needsVerification = claimNeedsVerification(result);
      const row = button.closest('.daily-claim-row'), feedback = $('.daily-claim-feedback', row), skip = $('[data-claim-skip]', row);
      const phase = entry?.controller.signal.aborted ? '正在停止…' : claimPhaseLabels[entry?.phase];
      const account = accountFor(accountID);
      const name = account.platform_name || account.label || '当前账号';
      const label = phase || (needsVerification ? '浏览器验证' : result && !result.canceled ? '重试' : '领取');
      button.disabled = loading || scheduleSaving || batchRunning || pendingAccounts.has(accountID);
      button.textContent = label;
      button.setAttribute('aria-label', `${label} · ${name}`);
      button.setAttribute('aria-busy', String(!!entry));
      skip.hidden = !entry || entry.controller.signal.aborted || !['waiting', 'preparing', 'verifying'].includes(entry.phase);
      skip.textContent = entry?.batchId ? '跳过账号' : '取消验证';
      skip.setAttribute('aria-label', `${entry?.batchId ? '跳过' : '取消验证'} · ${name}`);
      const message = entry ? entry.feedback : needsVerification ? result.message || '等待浏览器验证' : result && !result.ok ? claimResultMessage(result) : '';
      const showError = entry ? entry.feedbackError : !result?.canceled && (!needsVerification || result?.manualVerificationFailed);
      if (feedback.textContent !== message) {
        feedback.textContent = message;
        feedback.title = message;
      }
      row.classList.toggle('needs-verification', !!needsVerification);
      feedback.classList.toggle('error', Boolean(message && showError));
    });
  };
  function paintSchedule() {
    if (!active()) return;
    $('#claims-schedule', main).checked = !!schedule?.enabled;
    const time = schedule?.time || '19:02';
    $('#claims-time', main).value = time;
    timeField.highlight();
    const details = ['服务端执行定时领取，关闭网页后仍会运行。浏览器依赖在「设置 → 上游安全验证」配置。'];
    if (schedule?.browser_window) {
      const window = schedule.browser_window;
      const clock = value => new Date(value).toLocaleTimeString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false, hour: '2-digit', minute: '2-digit' });
      details.push(`浏览器窗口：${clock(window.start_at)}–${clock(window.end_at)}，任务完成后提前退出。`);
    }
    if (scheduleError) details.push('设置读取失败：' + scheduleError);
    if (schedule?.next_run_at) details.push('下次执行：' + new Date(schedule.next_run_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) + '。');
    const last = schedule?.last_run;
    if (last?.started_at) {
      const date = new Date(last.finished_at || last.started_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
      const pending = last.pending_manual ?? (last.results || []).filter(result => result.requires_verification).length;
      const state = { running: '执行中', retry_wait: '等待恢复', completed: '已完成', partial: '部分成功', failed: '领取失败', needs_verification: '待浏览器验证', interrupted: '已中断' }[last.status] || last.status;
      details.push(`上次执行：${date}，${state}，已领取 ${last.claimed || 0} 张${last.failed ? `，失败 ${last.failed} 项` : ''}${pending ? `，${pending} 项待浏览器验证` : ''}。`);
      if (last.next_retry_at && schedule.enabled) details.push('恢复时间：' + new Date(last.next_retry_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) + '。');
      const reasons = [...new Set((last.results || []).filter(result => !result.ok && !result.no_cards).map(result => result.error || result.message).filter(Boolean))];
      if (reasons.length) details.push('未领取原因：' + reasons.join('；'));
    }
    $('#claims-schedule-info', main).innerHTML = infoTip(details.join('\n'), '定时领取');
  }
  function paint() {
    if (!active()) return;
    const ready = visiblePreviews().filter(preview => claimablePlans(preview).length);
    const failures = previews.filter(preview => preview.error);
    const pagination = UI.paginate(ready, page, pageSize), { items, pages } = pagination;
    page = pagination.page;
    content.innerHTML = items.length ? items.map(preview => claimAccountMarkup(preview, accountFor(preview.account_id))).join('')
      : loading ? '<div class="skeleton-block"></div>'
      : '<div class="daily-claim-empty">' + icon('gift', 28) + `<p${discoveryError || failures.length ? ' role="alert"' : ''}>${discoveryError || failures.length ? '查询未完成，请刷新重试' : '暂无可领取的卡片'}</p></div>`;
    const notices = [];
    if (failures.length) notices.push(`<span class="warn-text">${failures.length} 个账号未更新</span>${infoTip(failures.map(preview => `${accountFor(preview.account_id).platform_name || preview.label || '账号'}：${preview.error}${preview.stale ? '（保留上次有效结果）' : ''}`).join('\n'), '账号查询失败')}`);
    if (discoveryError) notices.push(infoTip(discoveryError, '查询失败'));
    if (accountsError) notices.push(infoTip('官方账号信息暂不可用：' + accountsError, '账号信息'));
    $('#claims-notice', main).innerHTML = notices.join('');
    const pager = $('#claims-pages', main);
    pager.hidden = pages === 1;
    pager.innerHTML = pages > 1 ? `<button type="button" data-claim-page="-1" aria-label="上一页可领取账号" ${page === 1 ? 'disabled' : ''}>‹</button><span>${page} / ${pages}</span><button type="button" data-claim-page="1" aria-label="下一页可领取账号" ${page === pages ? 'disabled' : ''}>›</button>` : '';
    syncBusy();
  }
  async function load() {
    if (!active() || loading || batchRunning || pendingClaims.size || scheduleSaving) return;
    loadController?.abort();
    const controller = new AbortController();
    loadController = controller;
    const current = () => active() && loadController === controller && !controller.signal.aborted;
    const request = async (path, timeout = 12000) => {
      const pending = new AbortController();
      const abort = () => pending.abort();
      controller.signal.addEventListener('abort', abort, { once: true });
      const timer = setTimeout(abort, timeout);
      try {
        return await api(path, { cache: 'no-store', signal: pending.signal });
      } finally {
        clearTimeout(timer);
        controller.signal.removeEventListener('abort', abort);
      }
    };
    const errorMessage = error => ['AbortError', 'TimeoutError'].includes(error.name) ? '查询超时，请刷新重试' : error.message;
    const validPreviousPlans = preview => claimablePlans(preview).filter(plan => Number(plan.ends_at) * 1000 > Date.now());
    discoveryError = '';
    loading = true; syncBusy(); content.setAttribute('aria-busy', 'true');
    $('#claims-update', main).textContent = '正在查询…';
    // Account names and schedule settings must not delay card discovery.
    void request('/api/accounts').then(result => {
      if (!current()) return;
      if (!Array.isArray(result)) throw new Error('账号信息返回格式无效');
      accounts = result; accountsError = ''; paint();
    }).catch(error => {
      if (!current()) return;
      accountsError = errorMessage(error); paint();
    });
    void request('/api/claims/schedule').then(result => {
      if (!current() || scheduleSaving) return;
      schedule = result; scheduleError = ''; paintSchedule(); syncBusy();
    }).catch(error => {
      if (!current()) return;
      scheduleError = errorMessage(error); paintSchedule(); syncBusy();
    });
    try {
      const discovery = await request('/api/claims/discover?scope=daily', 35000);
      if (!current()) return;
      if (!Array.isArray(discovery.previews)) throw new Error('查询结果不完整，请刷新重试');
      const previous = new Map(previews.map(preview => [preview.account_id, preview]));
      previews = discovery.previews.map(preview => {
        // New servers already reconcile cached candidates against ownership.
        // Only older/error responses without that check need a local fallback.
        if (!preview.error || preview.stale !== undefined || !previous.has(preview.account_id)) return preview;
        const plans = validPreviousPlans(previous.get(preview.account_id));
        return { ...preview, plans, stale: plans.length > 0 };
      });
      const failed = previews.filter(preview => preview.error).length;
      const time = new Date(discovery.checked_at || Date.now()).toLocaleTimeString('zh-CN', { hour12: false });
      $('#claims-update', main).textContent = failed && failed === previews.length ? '查询未完成' : `${failed ? '部分更新' : '更新于'} ${time}`;
    } catch (error) {
      if (current()) {
        discoveryError = errorMessage(error);
        previews = previews.map(preview => {
          const plans = validPreviousPlans(preview);
          return { ...preview, plans, error: discoveryError, stale: plans.length > 0 };
        });
        $('#claims-update', main).textContent = jobs().length ? '未更新 · 保留上次结果' : '查询未完成';
      }
    } finally {
      if (loadController === controller) {
        loading = false;
        if (active()) { content.setAttribute('aria-busy', 'false'); paint(); }
      }
    }
  }
  const claimActive = entry => active() && !entry.controller.signal.aborted && entry.day === claimDay();

  function setClaimPhase(entry, phase, feedback = '', feedbackError = false) {
    entry.phase = phase;
    entry.feedback = feedback;
    entry.feedbackError = feedbackError;
    syncBusy();
  }

  function enqueueClaim(job, batchId = '') {
    const key = claimJobKey(job);
    const existing = pendingClaims.get(key);
    if (existing) return existing.promise;
    const entry = { job, key, batchId, day: claimDay(), phase: 'queued', feedback: '', controller: new AbortController() };
    entry.promise = new Promise(resolve => { entry.resolve = resolve; });
    pendingClaims.set(key, entry);
    claimQueue.push(entry);
    pumpClaims();
    return entry.promise;
  }

  function recordClaimResult(entry, result) {
    if (!result || TOKEN !== token || entry.day !== claimDay()) return;
    const results = manualClaimResults();
    if (result.canceled && claimNeedsVerification(results.get(entry.key))) return;
    results.set(entry.key, result);
  }

  function cancelResult(entry) {
    return entry.cancelMessage ? { ok: false, canceled: true, skipped: !!entry.skipped, code: 'canceled', message: entry.cancelMessage } : null;
  }

  function pumpClaims() {
    for (let index = 0; index < claimQueue.length;) {
      const entry = claimQueue[index];
      if (!claimActive(entry)) {
        claimQueue.splice(index, 1);
        const result = cancelResult(entry);
        recordClaimResult(entry, result);
        pendingClaims.delete(entry.key);
        entry.resolve(result);
      } else if (claimRunning) {
        index++;
      } else {
        claimQueue.splice(index, 1);
        claimRunning = true;
        void runClaim(entry);
      }
    }
    syncBusy();
  }

  function cancelClaimEntries(matches, message, skipped = false) {
    for (const entry of pendingClaims.values()) {
      if (!matches(entry) || entry.phase === 'submitting' || entry.controller.signal.aborted) continue;
      entry.cancelMessage = message;
      entry.skipped = skipped;
      entry.controller.abort();
    }
    pumpClaims();
  }

  async function runClaim(entry) {
    const { job, controller: { signal } } = entry;
    let config, result;
    const diagnostic = {};
    const releaseConfig = (value = config, outcome) => {
      if (value === config) config = null;
      const session = value?.verification_session;
      if (session) void api('/api/claims/verification/cancel', { method: 'POST', body: { verification_session: session, batch_id: entry.batchId, verification_diagnostics: diagnostic, verification_error: outcome ? { code: outcome.code || 'browser_verification_failed', message: outcome.message, canceled: !!outcome.canceled } : undefined } }).catch(() => {});
    };
    const readConfig = async forVerification => {
      if (!claimActive(entry)) return;
      const query = new URLSearchParams(job);
      if (!forVerification) query.set('config_only', '1');
      setClaimPhase(entry, forVerification ? 'preparing' : 'config');
      const fresh = await api('/api/claims/verification?' + query, { cache: 'no-store' });
      if (!claimActive(entry)) { releaseConfig(fresh); return; }
      config = fresh;
      if (typeof config?.enabled !== 'boolean') throw new Error('上游未返回有效的安全验证配置，请重试。');
    };

    try {
      await readConfig(false);
      if (!claimActive(entry)) return;
      let ticket = {};
      if (config.enabled) {
        setClaimPhase(entry, 'waiting');
        ticket = await queueClaimBrowserVerification(signal, async () => {
          if (!claimActive(entry)) return null;
          setClaimPhase(entry, 'waiting', document.hidden ? '等待回到领卡页面' : '等待浏览器验证');
          if (!(await waitForClaimPage(signal)) || !claimActive(entry)) return null;
          await readConfig(true);
          if (!claimActive(entry)) return null;
          if (!config.enabled) return {};
          if (!config.scene_id || !config.verification_session || !Number.isFinite(config.expires_at)) {
            throw new Error('上游未提供有效的验证会话，请重试。');
          }
          return requestClaimCaptcha(config, signal, {
            onStart: () => setClaimPhase(entry, 'verifying'),
            onFeedback: (message, isError) => setClaimPhase(entry, 'verifying', message, isError),
            onDismiss: () => { entry.skipped = !!entry.batchId; entry.cancelMessage = entry.batchId ? '已跳过该账号' : '已取消验证'; },
            diagnostic,
          });
        });
      }
      if (!ticket || !claimActive(entry)) return;
      setClaimPhase(entry, 'submitting');
      const body = { ...job, ...ticket, batch_id: entry.batchId, verification_diagnostics: diagnostic };
      result = await api('/api/claims/claim', { method: 'POST', body });
      if (result && !result.ok && ticket.captcha_verify_param && result.httpStatus > 0) {
        result = { ...result, message: 'ZCode 领取失败：' + claimResultMessage(result), manualVerificationFailed: true };
      }
      if (result && !result.ok && (claimNeedsVerification(result) || result.retry_after_ms > 0 || ['auth', 'account_risk', 'network', 'rate_limit'].includes(result.failure_kind))) {
        cancelClaimEntries(other => other !== entry && other.job.account_id === job.account_id, '等待重新领取');
      }
    } catch (error) {
      if (claimActive(entry)) {
        const requiresVerification = (entry.phase !== 'submitting' && !!config?.enabled) || claimNeedsVerification(manualClaimResults().get(entry.key));
        result = { ok: false, code: error.code, message: error.message, error_reason: error.error_reason, error_detail: error.error_detail, http_status: error.http_status, gateway_status: error.status, requires_verification: requiresVerification, manualVerificationFailed: true };
      }
    } finally {
      result ||= cancelResult(entry);
      releaseConfig(config, entry.phase === 'submitting' ? undefined : result);
      recordClaimResult(entry, result);
      claimRunning = false;
      pendingClaims.delete(entry.key);
      entry.resolve(result);
      if (result?.ok) window.dispatchEvent(new Event('manual-claim-completed'));
      pumpClaims();
    }
  }
  $('#claims-refresh', main).addEventListener('click', load);
  $('#claims-schedule', main).addEventListener('change', async event => {
    if (!schedule || scheduleSaving || batchRunning || pendingClaims.size) return;
    const enabled = event.target.checked;
    scheduleSaving = true; syncBusy();
    try {
      const result = await api('/api/settings', { method: 'PUT', body: { daily_claim_enabled: enabled } });
      schedule = result.claim_schedule;
      scheduleError = '';
      toast(enabled ? '定时已开启' : '定时已关闭', 'ok');
    } catch (error) { toast(error.message, 'err'); }
    finally { scheduleSaving = false; paintSchedule(); syncBusy(); }
  });
  const timeField = setupTimeField(main, {
    fallback: () => schedule?.time || '19:02',
    busy: () => scheduleSaving || batchRunning || pendingClaims.size > 0,
    save: async time => {
      scheduleSaving = true; syncBusy();
      try {
        const result = await api('/api/settings', { method: 'PUT', body: { daily_claim_time: time } });
        schedule = result.claim_schedule;
        scheduleError = '';
        toast('时间已保存', 'ok');
      } catch (error) { toast(error.message, 'err'); throw error; }
      finally { scheduleSaving = false; paintSchedule(); syncBusy(); }
    },
  });
  $('#claims-pages', main).addEventListener('click', event => {
    const button = event.target.closest('[data-claim-page]');
    if (!button || button.disabled) return;
    page += Number(button.dataset.claimPage); paint();
    $('#claims-pages button:not(:disabled)', main)?.focus({ preventScroll: true });
  });
  content.addEventListener('click', event => {
    const accountLink = event.target.closest('[data-act="claim-account"]');
    if (accountLink) { viewAccount(accountLink.dataset.id); return; }
    const skip = event.target.closest('[data-claim-skip]');
    const button = skip ? $('[data-claim]', skip.closest('.daily-claim-row')) : event.target.closest('[data-claim]');
    if (!button) return;
    const job = { account_id: button.dataset.claim, plan_id: button.dataset.plan };
    if (skip) {
      cancelClaimEntries(entry => entry.key === claimJobKey(job), batchRunning ? '已跳过该账号' : '已取消验证', batchRunning);
      return;
    }
    if (button.disabled) return;
    void enqueueClaim(job).then(result => {
      if (active() && result?.ok) toast(result.reconciled ? '已持有' : result.already_claimed ? '已领取过' : '已领取', 'ok');
    });
  });
  $('#claims-stop', main).addEventListener('click', () => {
    batchStopped = true;
    cancelClaimEntries(() => true, '已停止领取');
  });
  $('#claims-all', main).addEventListener('click', async () => {
    if (batchRunning || pendingClaims.size || loading || scheduleSaving) return;
    const queued = jobs();
    if (!queued.length) return;
    const batchId = Array.from(crypto.getRandomValues(new Uint8Array(32)), byte => byte.toString(16).padStart(2, '0')).join('');
    const summary = $('#claims-summary', main);
    summary.textContent = '';
    batchRunning = true;
    batchStopped = false;
    batchRemaining = queued.length;
    syncBusy();
    try {
      let claimed = 0, confirmed = 0, alreadyClaimed = 0, needsVerification = 0, failed = 0, canceled = 0;
      const blockedAccounts = new Set();
      for (const job of queued) {
        let result = { canceled: true };
        if (active() && !batchStopped && !blockedAccounts.has(job.account_id)) {
          const index = visiblePreviews().filter(preview => claimablePlans(preview).length).findIndex(preview => preview.account_id === job.account_id);
          const targetPage = Math.floor(Math.max(0, index) / pageSize) + 1;
          if (targetPage !== page) { page = targetPage; paint(); }
          result = await enqueueClaim(job, batchId);
        }
        if (result && !result.ok && (result.skipped || claimNeedsVerification(result) || result.requires_confirmation || result.retry_after_ms > 0 || ['auth', 'account_risk', 'network', 'rate_limit'].includes(result.failure_kind))) {
          blockedAccounts.add(job.account_id);
        }
        batchRemaining--;
        syncBusy();
        if (!result || result.canceled) canceled++;
        else if (result.reconciled) confirmed++;
        else if (result.already_claimed) alreadyClaimed++;
        else if (result.ok) claimed++;
        else if (claimNeedsVerification(result)) needsVerification++;
        else failed++;
      }
      if (!active()) return;
      summary.textContent = `本次领取 ${claimed} 张${confirmed ? `，${confirmed} 张已核实持有` : ''}${alreadyClaimed ? `，${alreadyClaimed} 张此前已领` : ''}${needsVerification ? `，${needsVerification} 张待浏览器验证` : ''}${failed ? `，${failed} 张失败` : ''}${canceled ? `，${canceled} 张未继续` : ''}`;
    } finally {
      batchRunning = false;
      batchRemaining = 0;
      syncBusy();
    }
  });
  window.addEventListener('manual-claim-completed', paint);
  const removalObserver = new MutationObserver(() => {
    if (active()) return;
    loadController?.abort();
    window.removeEventListener('manual-claim-completed', paint);
    removalObserver.disconnect();
    cancelClaimEntries(() => true, '');
  });
  removalObserver.observe(main, { childList: true, subtree: true });
  removalObserver.observe($('#app-view'), { attributes: true, attributeFilter: ['class'] });
  void load();
}
