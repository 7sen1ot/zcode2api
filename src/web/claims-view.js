function claimablePlans(preview) {
  const plans = new Map();
  for (const plan of preview.plans || []) {
    if (plan.plan_id && !plans.has(plan.plan_id)) plans.set(plan.plan_id, plan);
  }
  return [...plans.values()];
}

let claimCaptchaScript, claimCaptchaSequence = 0;
const manualClaimConcurrency = 5;
let claimBrowserVerificationQueue = Promise.resolve();
// The SDK requires more than two seconds before verification.
const claimCaptchaReadyDelay = 2100;
const claimCaptchaAutoTimeout = 15000;
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

async function requestClaimCaptcha(config, label, { isActive = () => true, refreshConfig, forceManual = false } = {}) {
  return new Promise((resolve, reject) => {
    const mountID = `claim-captcha-${++claimCaptchaSequence}`;
    const form = h(`<div><p class="dialog-intro">${esc(label)} · 验证后领取</p><div id="${mountID}"></div><button type="button" class="btn primary" id="${mountID}-start" disabled>开始验证</button><p class="quiet-message" data-captcha-status role="status">正在加载验证…</p><div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button></div></div>`);
    form.hidden = true;
    document.body.appendChild(form);
    const status = $('[data-captcha-status]', form), mount = $(`#${mountID}`, form);
    let instance, dialog, observer, timeout, readyTimer, autoWaitTimer, sessionTimer, epoch = 0, settled = false, initializing = false, verifying = false, initializedOnce = false, manualRequired = forceManual;
    let startButton = $(`#${mountID}-start`, form);
    const setVerifying = value => {
      verifying = value;
      startButton.classList.toggle('loading', value);
      startButton.setAttribute('aria-busy', String(value));
    };
    const finish = (ticket, error) => {
      if (settled) return;
      settled = true;
      epoch++;
      clearTimeout(timeout);
      clearTimeout(readyTimer);
      clearTimeout(autoWaitTimer);
      clearTimeout(sessionTimer);
      observer?.disconnect();
      window.removeEventListener('pagehide', cancel);
      try { instance?.destroy?.(); } catch { }
      if (dialog) dialog.close();
      else form.remove();
      error ? reject(error) : resolve(ticket);
    };
    const showDialog = () => {
      if (settled || !manualRequired || dialog) return;
      form.hidden = false;
      dialog = modal('领取验证', form, () => finish(null), { externalFocus: true });
    };
    const cancel = () => finish(null);
    observer = new MutationObserver(() => { if (!form.isConnected || !isActive()) finish(null); });
    observer.observe(document.body, { childList: true, subtree: true });
    window.addEventListener('pagehide', cancel);
    timeout = setTimeout(() => finish(null, new Error('验证超时，请重新领取。')), 120000);
    $('[data-cancel]', form).addEventListener('click', () => finish(null));
    const failed = value => {
      if (settled) return;
      clearTimeout(readyTimer);
      clearTimeout(autoWaitTimer);
      const result = value?.Result || value?.result || value || {};
      const code = String(result.VerifyCode || result.verifyCode || value?.verifyCode || '');
      const enteringManual = !manualRequired;
      manualRequired = true;
      showDialog();
      status.textContent = code === 'F015'
        ? '请按组件提示完成验证。'
        : `验证未通过${/^[FT][0-9]{3}$/.test(code) ? `（${code}）` : ''}，请点击重新验证。`;
      status.classList.add('error');
      setVerifying(false);
      startButton.disabled = !instance;
      startButton.textContent = '重新验证';
      if (enteringManual) { try { instance?.show?.(); } catch {} }
    };
    const initializationFailed = error => {
      if (settled) return;
      manualRequired = true;
      showDialog();
      epoch++;
      initializing = false;
      clearTimeout(readyTimer);
      clearTimeout(autoWaitTimer);
      clearTimeout(sessionTimer);
      try { instance?.destroy?.(); } catch { }
      instance = null;
      setVerifying(false);
      status.textContent = error?.message || '验证资源加载失败，请重新加载。';
      status.classList.add('error');
      startButton.textContent = '重新加载';
      startButton.disabled = false;
    };
    async function initialize() {
      if (settled || initializing) return;
      initializing = true;
      const current = ++epoch;
      clearTimeout(readyTimer);
      clearTimeout(autoWaitTimer);
      try { instance?.destroy?.(); } catch { }
      instance = null;
      mount.replaceChildren();
      // Replacing the trigger removes old SDK listeners, preventing duplicate submissions.
      const button = startButton.cloneNode(false);
      startButton.replaceWith(button);
      startButton = button;
      setVerifying(false);
      startButton.textContent = '开始验证';
      startButton.disabled = true;
      status.classList.remove('error');
      status.textContent = '正在加载验证…';
      startButton.addEventListener('click', event => {
        if (settled || current !== epoch || !instance) {
          event.stopImmediatePropagation();
          if (!settled && !instance) void initialize();
          return;
        }
        if (verifying) { event.stopImmediatePropagation(); return; }
        setVerifying(true);
        status.classList.remove('error');
        status.textContent = '请按官方组件提示完成验证。';
      });
      try {
        if (!isActive()) { finish(null); return; }
        if (refreshConfig && (initializedOnce || (config.expires_at && config.expires_at <= Date.now() + 5000))) {
          const fresh = await refreshConfig();
          if (settled || current !== epoch) return;
          if (!fresh || !isActive()) { finish(null); return; }
          config = fresh;
        }
        initializedOnce = true;
        clearTimeout(sessionTimer);
        if (config.expires_at) sessionTimer = setTimeout(() => {
          if (!settled && current === epoch) initializationFailed(new Error('验证会话已过期，请重新加载。'));
        }, Math.max(0, config.expires_at - Date.now() - 1000));
        window.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix };
        await loadClaimCaptchaScript();
        if (settled || current !== epoch) return;
        const initializedAt = performance.now();
        const initialization = window.initAliyunCaptcha({
          SceneId: config.scene_id,
          mode: 'popup', language: 'cn', element: `#${mountID}`, button: `#${mountID}-start`, delayBeforeSuccess: false,
          getInstance(value) {
            if (settled || current !== epoch) { try { value?.destroy?.(); } catch {} return; }
            if (!value) { initializationFailed(); return; }
            if (instance) { if (value !== instance) { try { value.destroy?.(); } catch {} } return; }
            instance = value;
            status.textContent = '准备验证…';
            readyTimer = setTimeout(() => {
              if (settled || current !== epoch || !instance) return;
              startButton.disabled = false;
              // Use the official traceless API once in the real browser. A
              // confirmed interactive challenge stays manual across reloads.
              const automatic = typeof instance.startTracelessVerification === 'function';
              if (!manualRequired && automatic) {
                setVerifying(true);
                status.textContent = '正在自动验证…';
                const failAutomatic = (message = '自动验证未完成，请重新领取。') => {
                  if (!settled && current === epoch && !manualRequired) initializationFailed(new Error(message));
                };
                autoWaitTimer = setTimeout(() => failAutomatic('自动验证超时，请重新领取。'), claimCaptchaAutoTimeout);
                try {
                  Promise.resolve(instance.startTracelessVerification()).catch(() => failAutomatic());
                } catch { failAutomatic(); }
                return;
              }
              if (!manualRequired && typeof instance.show !== 'function') {
                initializationFailed(new Error('验证组件未提供可用验证方式，请重新领取。'));
                return;
              }
              manualRequired = true;
              showDialog();
              status.textContent = '请点击“开始验证”，按官方组件提示完成验证。';
              // Interactive scenes can display their official challenge now.
              // Traceless scenes still require the SDK-bound button click.
              if (!automatic) {
                try { instance.show?.(); } catch {}
              }
            }, Math.max(0, claimCaptchaReadyDelay - (performance.now() - initializedAt)));
          },
          success(param) {
            if (settled || current !== epoch) return;
            if (!isActive()) { finish(null); return; }
            if (config.expires_at && config.expires_at <= Date.now() + 1000) { initializationFailed(new Error('验证会话已过期，请重新加载。')); return; }
            if (typeof param !== 'string' || !param.trim()) { failed(); return; }
            finish({ captcha_verify_param: param, captcha_region: config.region, verification_session: config.verification_session });
          },
          fail(value) { if (!settled && current === epoch) failed(value); },
          onError() { if (!settled && current === epoch) initializationFailed(); },
          onClose(reason) { if (!settled && current === epoch && reason === 'userDismiss') finish(null); },
        });
        Promise.resolve(initialization).catch(() => { if (!settled && current === epoch) initializationFailed(); });
      } catch (error) { if (!settled && current === epoch) initializationFailed(error); }
      finally { if (current === epoch) initializing = false; }
    }
    void initialize();
  });
}

async function submitClaim(job, label, isActive = () => true) {
  if (!isActive()) return null;
  const result = await api('/api/claims/claim', { method: 'POST', body: job });
  if (!isActive()) return null;
  if (result.ok || !result.requires_verification) return result;
  // The page SDK has shared configuration and a shared modal surface. Queue
  // only this fallback; server verification and claims remain concurrent.
  // Create the short-lived verification session after obtaining this turn.
  const verify = () => submitClaimWithBrowser(job, label, isActive, result);
  const pending = claimBrowserVerificationQueue.then(verify, verify);
  claimBrowserVerificationQueue = pending.catch(() => {});
  return pending;
}

async function submitClaimWithBrowser(job, label, isActive, result) {
  if (!isActive()) return null;
  let config, closed = false;
  const releaseConfig = value => {
    if (value?.verification_session) void api('/api/claims/verification/cancel', { method: 'POST', body: { verification_session: value.verification_session } }).catch(() => {});
  };
  const refreshConfig = async () => {
    releaseConfig(config);
    config = null;
    const fresh = await api('/api/claims/verification?account_id=' + encodeURIComponent(job.account_id) + '&plan_id=' + encodeURIComponent(job.plan_id));
    if (closed || !isActive()) { releaseConfig(fresh); return null; }
    config = fresh;
    if (!config.enabled) throw new Error('上游要求验证，但当前未提供可用验证场景，请稍后重试。');
    return config;
  };
  try {
    let response = result;
    for (let sessionAttempt = 0; sessionAttempt < 2; sessionAttempt++) {
      const fresh = await refreshConfig();
      if (!fresh) return null;
      const verification = await requestClaimCaptcha(fresh, label, { isActive, refreshConfig, forceManual: sessionAttempt > 0 });
      if (!verification || !isActive()) return null;
      response = await api('/api/claims/claim', { method: 'POST', body: { ...job, ...verification } });
      if (!isActive()) return null;
      // An invalid local session is rejected before submission upstream. Only
      // that case may reopen verification automatically, using a fresh ticket.
      if (response.code !== 'captcha_session_invalid') return response;
    }
    return response;
  } finally {
    closed = true;
    releaseConfig(config);
  }
}

function claimAccountMarkup(preview, account = {}) {
  return claimablePlans(preview).map(plan => `<div class="daily-claim-row">
    ${accountNameMarkup({ ...account, id: preview.account_id }, 'claim-account')}
    <div class="daily-claim-grants">${(plan.entitlements || []).map(entitlement => `<div><strong title="${esc(fmtInt(entitlement.grant_units))} ${esc(entitlement.unit_type || 'Token')}">${fmtQuota(entitlement.grant_units)}<small> ${esc(entitlement.unit_type || 'Token')}</small></strong><span>${esc(entitlement.show_name || entitlement.meter || '')}</span></div>`).join('') || '<span class="muted">额度以 ZCode 为准</span>'}</div>
    <button type="button" class="btn primary" data-claim="${esc(preview.account_id)}" data-plan="${esc(plan.plan_id)}" aria-label="立即手动领取 ${esc(account.platform_name || '当前账号')} 的每日免费 Token">立即领取</button>
  </div>`).join('');
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
  for (const [unit, count] of [['hour', 24], ['minute', 60]]) {
    $(`.time-field-col[data-unit="${unit}"]`, pop).innerHTML = Array.from({ length: count }, (_, i) =>
      `<button type="button" class="time-option" data-value="${timePad2(i)}" aria-label="${i} ${unit === 'hour' ? '点' : '分'}">${timePad2(i)}</button>`).join('');
  }
  const isOpen = () => pop.classList.contains('open');
  const highlight = () => {
    const current = normalizeDailyTime(input.value, hooks.fallback()) || hooks.fallback();
    const [hour, minute] = current.split(':');
    for (const col of $$('.time-field-col', pop)) {
      const target = col.dataset.unit === 'hour' ? hour : minute;
      for (const option of $$('.time-option', col)) option.classList.toggle('selected', option.dataset.value === target);
    }
  };
  function open() {
    if (trigger.disabled) return;
    highlight();
    for (const option of $$('.time-option.selected', pop)) {
      const col = option.parentElement;
      col.scrollTop = option.offsetTop - (col.clientHeight - option.offsetHeight) / 2;
    }
    pop.classList.add('open');
    trigger.setAttribute('aria-expanded', 'true');
  }
  const close = () => { pop.classList.remove('open'); trigger.setAttribute('aria-expanded', 'false'); };
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
    commit();
  });
  pop.addEventListener('keydown', event => { if (event.key === 'Escape') { close(); input.focus(); } });
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
  const dismiss = event => {
    if (!main.contains(field)) { document.removeEventListener('pointerdown', dismiss); return; }
    if (!field.contains(event.target)) close();
  };
  document.addEventListener('pointerdown', dismiss);
  return { highlight, close };
}

async function pageClaims(main) {
  let previews = [], accounts = [], accountsError = '', batchRunning = false, loading = false, page = 1;
  let schedule = null, scheduleSaving = false, scheduleError = '';
  const pendingClaims = new Map(), claimQueue = [], runningAccounts = new Set();
  const claimKey = job => JSON.stringify([job.account_id, job.plan_id]);
  const pageSize = 8;
  main.innerHTML = `${pagehead('领卡中心', '', '<button type="button" class="btn" id="claims-refresh">' + icon('refresh', 14) + '刷新</button>')}
    <section class="daily-claim-card fade-up" aria-labelledby="daily-claim-title">
      <header class="daily-claim-header"><div class="daily-claim-title"><span class="daily-claim-icon">${icon('gift', 22)}</span><div><h2 id="daily-claim-title">每日免费 Token</h2><span>每日 19:00 开放 · 北京时间</span></div></div><div class="daily-claim-actions"><div class="daily-claim-schedule"><span class="time-field" id="claims-time-field"><input id="claims-time" type="text" inputmode="numeric" autocomplete="off" spellcheck="false" maxlength="5" value="19:02" disabled aria-label="每日浏览器定时领取时间（北京时间）"><button type="button" class="time-field-trigger" id="claims-time-trigger" aria-label="选择定时领取时间" aria-expanded="false" aria-haspopup="dialog" disabled>${icon('timer', 13)}</button><span class="time-field-pop" id="claims-time-pop" role="dialog" aria-label="选择定时领取时间"><span class="time-field-col" data-unit="hour" role="group" aria-label="小时"></span><span class="time-field-col" data-unit="minute" role="group" aria-label="分钟"></span></span></span><label for="claims-schedule">浏览器定时领取<input id="claims-schedule" type="checkbox" role="switch" disabled></label><span id="claims-schedule-info">${infoTip('定时领卡固定使用服务端真实浏览器，需在“设置 → 上游安全验证 → 领卡验证”配置浏览器依赖。服务需保持运行，网页可关闭。“立即领取”和“全部立即领取”使用手动领卡设置。', '定时领取')}</span></div><button type="button" class="btn primary" id="claims-all" disabled>全部立即领取</button></div></header>
      <div id="claims-content" aria-busy="true"><div class="skeleton-block"></div></div>
      <footer class="daily-claim-footer"><span id="claims-update" class="result-count" aria-live="polite">正在查询…</span><div id="claims-notice"></div><nav class="pager" id="claims-pages" aria-label="可领取账号分页" hidden></nav></footer>
    </section>`;
  const content = $('#claims-content', main);
  const active = () => main.contains(content);
  const jobs = () => previews.flatMap(preview => claimablePlans(preview).map(plan => ({ account_id: preview.account_id, plan_id: plan.plan_id })));
  const accountFor = id => accounts.find(account => account.id === id) || {};
  const syncBusy = () => {
    if (!active()) return;
    const busy = batchRunning || pendingClaims.size > 0;
    const pendingAccounts = new Set([...pendingClaims.values()].map(entry => entry.job.account_id));
    const total = jobs().length;
    $('#claims-refresh', main).disabled = busy || loading || scheduleSaving;
    $('#claims-schedule', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time-trigger', main).disabled = busy || loading || scheduleSaving || !schedule;
    const allButton = $('#claims-all', main);
    allButton.disabled = busy || loading || scheduleSaving || !total;
    allButton.textContent = busy ? `正在领取（剩余 ${pendingClaims.size}）` : total ? `全部立即领取（${total}）` : '全部立即领取';
    $$('button[data-claim]', content).forEach(button => {
      const entry = pendingClaims.get(claimKey({ account_id: button.dataset.claim, plan_id: button.dataset.plan }));
      button.disabled = loading || scheduleSaving || batchRunning || pendingAccounts.has(button.dataset.claim);
      button.textContent = entry ? entry.running ? '正在领取…' : '等待领取…' : '立即领取';
      button.setAttribute('aria-busy', String(!!entry?.running));
    });
  };
  function paintSchedule() {
    if (!active()) return;
    $('#claims-schedule', main).checked = !!schedule?.enabled;
    const time = schedule?.time || '19:02';
    $('#claims-time', main).value = time;
    timeField.highlight();
    const details = [`每日北京时间 ${time} 固定使用服务端真实浏览器领取；服务需保持运行，网页可关闭。请在“设置 → 上游安全验证 → 领卡验证”配置浏览器依赖，浏览器缺失或启动失败时不执行领取。`, '可重试的失败在计划时间起 10 分钟内恢复一次，仍失败则等待次日。需要人工验证时，请点击“立即领取”；“全部立即领取”也使用手动领卡设置。'];
    if (schedule?.browser_window) {
      const window = schedule.browser_window;
      const clock = value => new Date(value).toLocaleTimeString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false, hour: '2-digit', minute: '2-digit' });
      details.push(`浏览器窗口：${clock(window.start_at)}–${clock(window.end_at)}，任务完成后提前退出；手动领取选择浏览器时按需启动，空闲 ${window.idle_seconds} 秒退出。`);
    }
    if (scheduleError) details.push('设置读取失败：' + scheduleError);
    if (schedule?.next_run_at) details.push('下次执行：' + new Date(schedule.next_run_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) + '。');
    const last = schedule?.last_run;
    if (last?.started_at) {
      const date = new Date(last.finished_at || last.started_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
      const pending = last.pending_manual ?? (last.results || []).filter(result => result.requires_verification).length;
      const state = { running: '执行中', retry_wait: '等待恢复', completed: '已完成', partial: '部分成功', failed: '领取失败', needs_verification: '需人工验证', interrupted: '已中断' }[last.status] || last.status;
      details.push(`上次执行：${date}，${state}，已领取 ${last.claimed || 0} 张${last.failed ? `，失败 ${last.failed} 项` : ''}${pending ? `，${pending} 项需人工验证` : ''}。`);
      if (last.next_retry_at && schedule.enabled) details.push('恢复时间：' + new Date(last.next_retry_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) + '。');
      const reasons = [...new Set((last.results || []).filter(result => !result.ok && !result.no_cards).map(result => result.error || result.message).filter(Boolean))];
      if (reasons.length) details.push('未领取原因：' + reasons.join('；'));
    }
    $('#claims-schedule-info', main).innerHTML = infoTip(details.join('\n'), '定时领取');
  }
  function paint() {
    const ready = previews.filter(preview => claimablePlans(preview).length);
    const pagination = UI.paginate(ready, page, pageSize), { items, pages } = pagination;
    page = pagination.page;
    content.innerHTML = items.length ? items.map(preview => claimAccountMarkup(preview, accountFor(preview.account_id))).join('') : '<div class="daily-claim-empty">' + icon('gift', 28) + '<p>暂无可领取的每日免费卡</p></div>';
    const failures = previews.filter(preview => preview.error);
    const notices = [];
    if (failures.length) notices.push(`<span>${failures.length} 个账号查询失败</span>${infoTip(failures.map(preview => `${accountFor(preview.account_id).platform_name || '账号'}：${preview.error}`).join('\n'), '账号查询失败')}`);
    if (accountsError) notices.push(infoTip('官方账号信息暂不可用：' + accountsError, '账号信息'));
    $('#claims-notice', main).innerHTML = notices.join('');
    const pager = $('#claims-pages', main);
    pager.hidden = pages === 1;
    pager.innerHTML = pages > 1 ? `<button type="button" data-claim-page="-1" aria-label="上一页可领取账号" ${page === 1 ? 'disabled' : ''}>‹</button><span>${page} / ${pages}</span><button type="button" data-claim-page="1" aria-label="下一页可领取账号" ${page === pages ? 'disabled' : ''}>›</button>` : '';
    syncBusy();
  }
  async function load() {
    if (loading || batchRunning || pendingClaims.size) return;
    loading = true; syncBusy(); content.setAttribute('aria-busy', 'true');
    $('#claims-update', main).textContent = '正在查询…';
    try {
      const [discovery, accountResult, scheduleResult] = await Promise.allSettled([api('/api/claims/discover?scope=daily'), api('/api/accounts'), api('/api/claims/schedule')]);
      if (!active()) return;
      if (scheduleResult.status === 'fulfilled') schedule = scheduleResult.value;
      scheduleError = scheduleResult.status === 'rejected' ? scheduleResult.reason.message : '';
      paintSchedule();
      if (accountResult.status === 'fulfilled') accounts = accountResult.value;
      accountsError = accountResult.status === 'rejected' ? accountResult.reason.message : '';
      if (discovery.status === 'rejected') throw discovery.reason;
      previews = discovery.value.previews || [];
      paint();
      $('#claims-update', main).textContent = `更新于 ${new Date().toLocaleTimeString('zh-CN', { hour12: false })}`;
    } catch (error) {
      if (active()) {
        previews = [];
        content.innerHTML = `<div class="daily-claim-empty" role="alert"><p class="error">查询失败：${esc(error.message)}</p></div>`;
        $('#claims-notice', main).innerHTML = '';
        $('#claims-pages', main).hidden = true;
        $('#claims-update', main).textContent = '查询失败';
      }
    } finally { loading = false; if (active()) { content.setAttribute('aria-busy', 'false'); syncBusy(); } }
  }
  function enqueueClaim(job) {
    if (!active() || loading || scheduleSaving) return Promise.resolve(null);
    const key = claimKey(job);
    if (pendingClaims.has(key)) return pendingClaims.get(key).promise;
    const entry = { job, key, running: false };
    entry.promise = new Promise((resolve, reject) => { entry.resolve = resolve; entry.reject = reject; });
    pendingClaims.set(key, entry);
    claimQueue.push(entry);
    pumpClaims();
    return entry.promise;
  }
  function pumpClaims() {
    if (!active()) {
      for (const entry of claimQueue.splice(0)) {
        pendingClaims.delete(entry.key);
        entry.resolve(null);
      }
      return;
    }
    while (runningAccounts.size < manualClaimConcurrency) {
      const index = claimQueue.findIndex(entry => !runningAccounts.has(entry.job.account_id));
      if (index < 0) break;
      const [entry] = claimQueue.splice(index, 1);
      entry.running = true;
      runningAccounts.add(entry.job.account_id);
      void runClaim(entry);
    }
    syncBusy();
  }
  async function runClaim(entry) {
    try {
      const result = await submitClaim(entry.job, accountFor(entry.job.account_id).platform_name || '当前账号', active);
      if (result?.ok) removeClaimed(entry.job);
      if (!result || (!result.ok && (result.requires_verification || result.retry_after_ms > 0 || ['auth', 'account_risk', 'network', 'rate_limit'].includes(result.failure_kind)))) {
        cancelAccountQueue(entry.job.account_id);
      }
      entry.resolve(result);
    } catch (error) {
      cancelAccountQueue(entry.job.account_id);
      entry.reject(error);
    }
    finally {
      runningAccounts.delete(entry.job.account_id);
      pendingClaims.delete(entry.key);
      pumpClaims();
    }
  }
  function cancelAccountQueue(accountID) {
    for (let index = claimQueue.length - 1; index >= 0; index--) {
      if (claimQueue[index].job.account_id !== accountID) continue;
      const [entry] = claimQueue.splice(index, 1);
      pendingClaims.delete(entry.key);
      entry.resolve(null);
    }
  }
  function removeClaimed(job) {
    const preview = previews.find(item => item.account_id === job.account_id);
    if (!preview) return;
    preview.plans = (preview.plans || []).filter(plan => plan.plan_id !== job.plan_id);
    if (active()) paint();
  }
  const resultMessage = result => {
    const code = result.code ?? result.httpStatus;
    return `${result.message || '领取失败'}${code !== undefined && code !== null && code !== '' && !String(result.message || '').includes(String(code)) ? `（${code}）` : ''}`;
  };
  $('#claims-refresh', main).addEventListener('click', load);
  $('#claims-schedule', main).addEventListener('change', async event => {
    if (!schedule || scheduleSaving || batchRunning || pendingClaims.size) return;
    const enabled = event.target.checked;
    scheduleSaving = true; syncBusy();
    try {
      const result = await api('/api/settings', { method: 'PUT', body: { daily_claim_enabled: enabled } });
      schedule = result.claim_schedule;
      scheduleError = '';
      toast(enabled ? `已开启每日 ${schedule.time} 浏览器定时领取` : '已关闭定时领取', 'ok');
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
        toast(`浏览器定时领取已改为每日 ${time}`, 'ok');
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
    const button = event.target.closest('[data-claim]');
    if (!button || button.disabled) return;
    const job = { account_id: button.dataset.claim, plan_id: button.dataset.plan };
    void enqueueClaim(job).then(result => {
      if (!active() || !result) return;
      toast(result.ok ? '每日免费卡已领取' : resultMessage(result), result.ok ? 'ok' : 'err');
    }).catch(error => { if (active()) toast(error.message, 'err'); });
  });
  $('#claims-all', main).addEventListener('click', async () => {
    if (batchRunning || pendingClaims.size || loading || scheduleSaving) return;
    const pending = jobs();
    batchRunning = true; syncBusy();
    try {
      const outcomes = await Promise.allSettled(pending.map(enqueueClaim));
      if (!active()) return;
      let claimed = 0, failed = 0, canceled = 0;
      for (const [index, outcome] of outcomes.entries()) {
        if (outcome.status === 'fulfilled' && !outcome.value) { canceled++; continue; }
        if (outcome.status === 'fulfilled' && outcome.value.ok) { claimed++; continue; }
        failed++;
        const message = outcome.status === 'rejected' ? outcome.reason.message : resultMessage(outcome.value);
        toast(`${accountFor(pending[index].account_id).platform_name || '账号'}：${message}`, 'err');
      }
      toast(`已领取 ${claimed} 张${failed ? `，${failed} 张失败` : ''}${canceled ? `，${canceled} 张未继续` : ''}`, failed ? 'err' : 'ok');
    } finally {
      batchRunning = false; syncBusy();
    }
  });
  await load();
}
