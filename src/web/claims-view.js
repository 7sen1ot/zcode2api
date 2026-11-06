function claimablePlans(preview) {
  const plans = new Map();
  for (const plan of preview.plans || []) {
    if (plan.plan_id && !plans.has(plan.plan_id)) plans.set(plan.plan_id, plan);
  }
  return [...plans.values()];
}

let claimCaptchaScript, claimCaptchaSequence = 0;
// The SDK requires more than two seconds before verification.
const claimCaptchaReadyDelay = 2100;
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

async function requestClaimCaptcha(config, label) {
  return new Promise((resolve, reject) => {
    const mountID = `claim-captcha-${++claimCaptchaSequence}`;
    const form = h(`<div><p class="dialog-intro">${esc(label)} · 验证后领取</p><div id="${mountID}"></div><button type="button" class="btn primary" id="${mountID}-start" disabled>开始验证</button><p class="quiet-message" data-captcha-status role="status">正在加载验证…</p><div class="modal-actions"><button type="button" class="btn" data-cancel>取消</button></div></div>`);
    const status = $('[data-captcha-status]', form), mount = $(`#${mountID}`, form);
    let instance, dialog, observer, timeout, readyTimer, epoch = 0, settled = false, initializing = false, verifying = false;
    let startButton = $(`#${mountID}-start`, form);
    const finish = (ticket, error) => {
      if (settled) return;
      settled = true;
      epoch++;
      clearTimeout(timeout);
      clearTimeout(readyTimer);
      observer?.disconnect();
      try { instance?.destroy?.(); } catch { }
      dialog?.close();
      error ? reject(error) : resolve(ticket);
    };
    dialog = modal('领取验证', form, () => finish(null), { externalFocus: true });
    observer = new MutationObserver(() => { if (!form.isConnected) finish(null); });
    observer.observe(document.body, { childList: true });
    timeout = setTimeout(() => finish(null, new Error('验证超时，请重新领取。')), 120000);
    $('[data-cancel]', form).addEventListener('click', () => finish(null));
    const failed = value => {
      if (settled) return;
      const result = value?.Result || value?.result || value || {};
      const code = String(result.VerifyCode || result.verifyCode || value?.verifyCode || '');
      status.textContent = code === 'F015'
        ? '请按组件提示完成验证。'
        : `验证未通过${/^[FT][0-9]{3}$/.test(code) ? `（${code}）` : ''}，请点击重新验证。`;
      status.classList.add('error');
      verifying = false;
      startButton.classList.remove('loading');
      startButton.setAttribute('aria-busy', 'false');
      startButton.disabled = false;
      startButton.textContent = '重新验证';
    };
    const initializationFailed = error => {
      if (settled) return;
      epoch++;
      initializing = false;
      clearTimeout(readyTimer);
      try { instance?.destroy?.(); } catch { }
      instance = null;
      verifying = false;
      startButton.classList.remove('loading');
      startButton.setAttribute('aria-busy', 'false');
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
      try { instance?.destroy?.(); } catch { }
      instance = null;
      verifying = false;
      mount.replaceChildren();
      // Replacing the trigger also removes listeners belonging to an old SDK
      // instance, so retrying initialization cannot submit twice.
      const button = startButton.cloneNode(false);
      startButton.replaceWith(button);
      startButton = button;
      startButton.classList.remove('loading');
      startButton.setAttribute('aria-busy', 'false');
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
        verifying = true;
        startButton.classList.add('loading');
        startButton.setAttribute('aria-busy', 'true');
        status.classList.remove('error');
        status.textContent = '请按官方组件提示完成验证。';
      });
      try {
        window.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix };
        await loadClaimCaptchaScript();
        if (settled || current !== epoch) return;
        const initialization = window.initAliyunCaptcha({
          SceneId: config.scene_id,
          mode: 'popup', language: 'cn', element: `#${mountID}`, button: `#${mountID}-start`, delayBeforeSuccess: false,
          getInstance(value) {
            if (settled || current !== epoch) { try { value?.destroy?.(); } catch {} return; }
            if (!value) { initializationFailed(); return; }
            if (instance) return;
            instance = value;
            status.textContent = '准备验证…';
            readyTimer = setTimeout(() => {
              if (settled || current !== epoch) return;
              startButton.disabled = false;
              status.textContent = '请点击“开始验证”，按官方组件提示完成验证。';
              // Interactive scenes can display their official challenge now.
              // Traceless scenes still require the SDK-bound button click.
              if (typeof instance.startTracelessVerification !== 'function') {
                try { instance.show?.(); } catch {}
              }
            }, claimCaptchaReadyDelay);
          },
          success(param) {
            if (settled || current !== epoch) return;
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

async function submitClaim(job, label) {
  const result = await api('/api/claims/claim', { method: 'POST', body: { ...job, auto_verify: true } });
  if (result.ok || !result.requires_verification) return result;
  const config = await api('/api/claims/verification?account_id=' + encodeURIComponent(job.account_id) + '&plan_id=' + encodeURIComponent(job.plan_id));
  if (!config.enabled) throw new Error('上游要求验证，但当前未提供可用验证场景，请稍后重试。');
  try {
    const verification = await requestClaimCaptcha(config, label);
    if (verification === null) return null;
    return await api('/api/claims/claim', { method: 'POST', body: { ...job, ...verification } });
  } finally {
    if (config.verification_session) void api('/api/claims/verification/cancel', { method: 'POST', body: { verification_session: config.verification_session } }).catch(() => {});
  }
}

function claimAccountMarkup(preview, account = {}) {
  return claimablePlans(preview).map(plan => `<div class="daily-claim-row">
    ${accountNameMarkup({ ...account, id: preview.account_id }, 'claim-account')}
    <div class="daily-claim-grants">${(plan.entitlements || []).map(entitlement => `<div><strong title="${esc(fmtInt(entitlement.grant_units))} ${esc(entitlement.unit_type || 'Token')}">${fmtQuota(entitlement.grant_units)}<small> ${esc(entitlement.unit_type || 'Token')}</small></strong><span>${esc(entitlement.show_name || entitlement.meter || '')}</span></div>`).join('') || '<span class="muted">额度以 ZCode 为准</span>'}</div>
    <button type="button" class="btn primary" data-claim="${esc(preview.account_id)}" data-plan="${esc(plan.plan_id)}" aria-label="领取 ${esc(account.platform_name || '当前账号')} 的每日免费 Token">领取</button>
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
// Editable HH:MM field with a lightweight dropdown picker, replacing the native
// time input whose segmented editor and wheel popup behave inconsistently.
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
  let previews = [], accounts = [], accountsError = '', busy = false, loading = false, page = 1;
  let schedule = null, scheduleSaving = false, scheduleError = '';
  const pageSize = 8;
  main.innerHTML = `${pagehead('领卡中心', '', '<button type="button" class="btn" id="claims-refresh">' + icon('refresh', 14) + '刷新</button>')}
    <section class="daily-claim-card fade-up" aria-labelledby="daily-claim-title">
      <header class="daily-claim-header"><div class="daily-claim-title"><span class="daily-claim-icon">${icon('gift', 22)}</span><div><h2 id="daily-claim-title">每日免费 Token</h2><span>每日 19:00 · 北京时间</span></div></div><div class="daily-claim-actions"><div class="daily-claim-schedule"><span class="time-field" id="claims-time-field"><input id="claims-time" type="text" inputmode="numeric" autocomplete="off" spellcheck="false" maxlength="5" value="19:02" disabled aria-label="每日自动领取时间（北京时间）"><button type="button" class="time-field-trigger" id="claims-time-trigger" aria-label="选择时间" aria-expanded="false" aria-haspopup="dialog" disabled>${icon('timer', 13)}</button><span class="time-field-pop" id="claims-time-pop" role="dialog" aria-label="选择自动领取时间"><span class="time-field-col" data-unit="hour" role="group" aria-label="小时"></span><span class="time-field-col" data-unit="minute" role="group" aria-label="分钟"></span></span></span><label for="claims-schedule">自动领取<input id="claims-schedule" type="checkbox" role="switch" disabled></label><span id="claims-schedule-info">${infoTip('每日北京时间 19:02 领取免费卡，需保持服务运行。需要人工验证时，请手动领取。', '自动领取')}</span></div><button type="button" class="btn primary" id="claims-all" disabled>全部领取</button></div></header>
      <div id="claims-content" aria-busy="true"><div class="skeleton-block"></div></div>
      <footer class="daily-claim-footer"><span id="claims-update" class="result-count" aria-live="polite">正在查询…</span><div id="claims-notice"></div><nav class="pager" id="claims-pages" aria-label="可领取账号分页" hidden></nav></footer>
    </section>`;
  const content = $('#claims-content', main);
  const active = () => main.contains(content);
  const jobs = () => previews.flatMap(preview => claimablePlans(preview).map(plan => ({ account_id: preview.account_id, plan_id: plan.plan_id })));
  const accountFor = id => accounts.find(account => account.id === id) || {};
  const syncBusy = () => {
    if (!active()) return;
    const total = jobs().length;
    $('#claims-refresh', main).disabled = busy || loading || scheduleSaving;
    $('#claims-schedule', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time', main).disabled = busy || loading || scheduleSaving || !schedule;
    $('#claims-time-trigger', main).disabled = busy || loading || scheduleSaving || !schedule;
    const allButton = $('#claims-all', main);
    allButton.disabled = busy || loading || scheduleSaving || !total;
    allButton.textContent = busy ? '正在领取…' : total ? `全部领取（${total}）` : '全部领取';
    $$('button[data-claim]', content).forEach(button => { button.disabled = busy || loading; });
  };
  function paintSchedule() {
    if (!active()) return;
    $('#claims-schedule', main).checked = !!schedule?.enabled;
    const time = schedule?.time || '19:02';
    $('#claims-time', main).value = time;
    timeField.highlight();
    const details = [`每日北京时间 ${time} 领取免费卡，需保持服务运行。需要人工验证时，请手动领取。`];
    if (scheduleError) details.push('设置读取失败：' + scheduleError);
    const last = schedule?.last_run;
    if (last?.finished_at) {
      const date = new Date(last.finished_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
      const pending = (last.results || []).filter(result => result.requires_verification).length;
      details.push(`上次执行：${date}，已领取 ${last.claimed || 0} 张${pending ? `，${pending} 项需人工验证` : ''}${last.status === 'interrupted' ? '，任务已中断' : ''}。`);
    }
    $('#claims-schedule-info', main).innerHTML = infoTip(details.join('\n'), '自动领取');
  }
  function paint() {
    const ready = previews.filter(preview => claimablePlans(preview).length);
    const pages = Math.max(1, Math.ceil(ready.length / pageSize));
    page = Math.min(page, pages);
    content.innerHTML = ready.length ? ready.slice((page - 1) * pageSize, page * pageSize).map(preview => claimAccountMarkup(preview, accountFor(preview.account_id))).join('') : '<div class="daily-claim-empty">' + icon('gift', 28) + '<p>暂无可领取的每日免费卡</p></div>';
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
    if (loading) return;
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
  async function act(callback) {
    if (busy || loading || scheduleSaving) return;
    busy = true; syncBusy();
    try { await callback(); if (active()) await load(); }
    catch (error) { toast(error.message, 'err'); }
    finally { busy = false; syncBusy(); }
  }
  const resultMessage = result => {
    const code = result.code ?? result.httpStatus;
    return `${result.message || '领取失败'}${code !== undefined && code !== null && code !== '' && !String(result.message || '').includes(String(code)) ? `（${code}）` : ''}`;
  };
  $('#claims-refresh', main).addEventListener('click', load);
  $('#claims-schedule', main).addEventListener('change', async event => {
    if (!schedule || scheduleSaving) return;
    const enabled = event.target.checked;
    scheduleSaving = true; syncBusy();
    try {
      await api('/api/settings', { method: 'PUT', body: { daily_claim_enabled: enabled } });
      schedule = { ...schedule, enabled };
      scheduleError = '';
      toast(enabled ? `已开启每日 ${schedule.time} 自动领取` : '已关闭自动领取', 'ok');
    } catch (error) { toast(error.message, 'err'); }
    finally { scheduleSaving = false; paintSchedule(); syncBusy(); }
  });
  const timeField = setupTimeField(main, {
    fallback: () => schedule?.time || '19:02',
    busy: () => scheduleSaving,
    save: async time => {
      scheduleSaving = true; syncBusy();
      try {
        await api('/api/settings', { method: 'PUT', body: { daily_claim_time: time } });
        schedule = { ...schedule, time };
        scheduleError = '';
        toast(`自动领取时间已改为每日 ${time}`, 'ok');
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
    act(async () => {
      const account = accountFor(button.dataset.claim);
      const result = await submitClaim({ account_id: button.dataset.claim, plan_id: button.dataset.plan }, account.platform_name || '当前账号');
      if (!result) return;
      toast(result.ok ? '每日免费卡已领取' : resultMessage(result), result.ok ? 'ok' : 'err');
    });
  });
  $('#claims-all', main).addEventListener('click', () => act(async () => {
    const pending = jobs();
    let claimed = 0, attempted = 0;
    for (const job of pending) {
      if (!active()) break;
      try {
        const result = await submitClaim(job, accountFor(job.account_id).platform_name || '当前账号');
        if (!result) break;
        attempted++;
        if (result.ok) claimed++;
        else toast(`${accountFor(job.account_id).platform_name || '账号'}：${resultMessage(result)}`, 'err');
      } catch (error) { toast(error.message, 'err'); break; }
    }
    const failed = attempted - claimed, canceled = pending.length - attempted;
    toast(`已领取 ${claimed} 张${failed ? `，${failed} 张失败` : ''}${canceled ? `，${canceled} 张未继续` : ''}`, failed ? 'err' : 'ok');
  }));
  await load();
}
