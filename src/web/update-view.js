function settingsUpdateMarkup() {
  return `<details class="settings-section formcard" id="update-panel">
    <summary class="settings-section-summary"><span><strong>版本更新</strong></span><span class="pill" id="update-summary">${esc(RUNTIME?.version || '未读取')}</span></summary>
    <div class="settings-panel settings-section-body update-panel-body">
      <div class="update-versions"><div><span>当前版本</span><strong id="update-current">${esc(RUNTIME?.version || '—')}</strong></div><div><span>最新发行版本</span><strong id="update-latest">未检查</strong></div></div>
      <div class="update-toolbar"><a class="text-action update-repository" href="https://github.com/7sen1ot/zcode2api" target="_blank" rel="noopener noreferrer">7sen1ot/zcode2api ↗</a><div class="update-actions"><a class="btn" id="update-manual" href="https://github.com/7sen1ot/zcode2api/releases" target="_blank" rel="noopener noreferrer" hidden>下载发行包 ↗</a><button type="button" class="btn" id="update-check">${icon('refresh', 14)}检查更新</button><button type="button" class="btn primary" id="update-install" hidden>更新并重启</button></div></div>
      <progress id="update-progress" aria-label="发行包下载进度" hidden></progress>
      <p id="update-message" class="update-message" role="status" aria-live="polite"></p>
      <details class="update-notes" id="update-notes" hidden><summary>发布说明</summary><pre id="update-notes-content"></pre><a id="update-release" class="text-action" href="https://github.com/7sen1ot/zcode2api/releases" target="_blank" rel="noopener noreferrer">在 GitHub 查看 ↗</a></details>
    </div>
  </details>`;
}

async function mountSettingsUpdate(main) {
  const panel = $('#update-panel', main), check = $('#update-check', panel), install = $('#update-install', panel);
  const message = $('#update-message', panel), progress = $('#update-progress', panel);
  const session = TOKEN;
  let info, requesting = false, timer, reconnecting = false, stopped = false, reconnectSince = 0;
  const active = () => !stopped && panel.isConnected && TOKEN === session;
  const pendingKey = 'z2a_update_pending';
  const remember = version => { try { sessionStorage.setItem(pendingKey, JSON.stringify({ version, started_at: Date.now() })); } catch {} };
  const forget = () => { try { sessionStorage.removeItem(pendingKey); } catch {} };
  const setMessage = (text, error = false) => { message.textContent = text; message.classList.toggle('error', error); };
  const request = (path, body, timeout = 8000) => api(path, {
    token: session, reauthenticate: false, method: body === undefined ? 'GET' : 'POST', body, signal: AbortSignal.timeout(timeout),
  });
  function paint() {
    if (!active()) return;
    const state = info?.state || {}, busy = !!info?.busy;
    $('#update-current', panel).textContent = info?.current_version || RUNTIME?.version || '—';
    $('#update-latest', panel).textContent = info?.latest_version || '未检查';
    $('#update-summary', panel).textContent = busy ? '更新中' : info?.available ? '有新版本' : info?.current_version || RUNTIME?.version || '未检查';
    check.disabled = requesting || busy;
    check.classList.toggle('loading', requesting && !busy);
    install.hidden = !info?.available;
    install.disabled = requesting || busy || !info?.supported || !info?.asset_ready;
    install.title = info?.reason || info?.asset_reason || '';
    const manual = $('#update-manual', panel);
    manual.hidden = !info || !!info.supported;
    manual.textContent = info?.deployment === 'container' ? '查看镜像 ↗' : '下载发行包 ↗';
    manual.href = info?.deployment === 'container' ? 'https://github.com/7sen1ot/zcode2api/pkgs/container/zcode2api' : info?.release_url || 'https://github.com/7sen1ot/zcode2api/releases';
    progress.hidden = !busy;
    if (state.phase === 'downloading' && state.total > 0) {
      progress.max = state.total; progress.value = Math.min(state.downloaded || 0, state.total);
    } else progress.removeAttribute('value');
    $('#update-notes', panel).hidden = !info?.latest_version;
    $('#update-notes-content', panel).textContent = info?.release_notes || '该版本未提供发布说明。';
    const link = $('#update-release', panel);
    link.href = info?.release_url || 'https://github.com/7sen1ot/zcode2api/releases';
    if (reconnecting) setMessage('服务正在重启，正在重新连接…');
    else if (state.message) setMessage(state.message, ['error', 'rolled_back'].includes(state.phase));
    else if (!info?.supported && info?.reason) setMessage(info.reason);
    else if (info?.available && !info.asset_ready) setMessage(info.asset_reason || '该版本暂无可用发行包', true);
    else if (info?.latest_version) setMessage(info.available ? `可更新至 ${info.latest_version}` : '已是最新发行版本');
    else setMessage('');
  }
  function queue() {
    clearTimeout(timer);
    if (active()) timer = setTimeout(poll, 1500);
  }
  async function recoverConnection() {
    if (!active()) return;
    reconnecting = true; paint();
    if (!reconnectSince) reconnectSince = Date.now();
    if (Date.now() - reconnectSince > 5 * 60000) {
      setMessage('服务连接尚未恢复，请稍后刷新页面或检查服务器状态。', true);
      return;
    }
    try {
      const response = await fetch('/healthz', { cache: 'no-store', signal: AbortSignal.timeout(4000) });
      const health = await response.json();
      if (response.ok && health.ok) {
        stopped = true; clearTimeout(timer);
        showLogin();
        toast('服务已恢复，请重新登录查看更新结果', 'ok');
        return;
      }
    } catch {}
    if (active()) timer = setTimeout(recoverConnection, 1500);
  }
  async function poll() {
    if (!active()) return;
    try {
      info = await request('/api/settings/update');
      reconnecting = false; paint();
      if (info.busy) queue();
      else if (['completed', 'rolled_back', 'error'].includes(info.state?.phase)) forget();
    } catch (error) {
      if (!active()) return;
      if (error.status === 401) {
        if (info?.busy) { void recoverConnection(); return; }
        stopped = true; showLogin(); return;
      }
      if (info?.busy && ['applying', 'restarting'].includes(info.state?.phase)) {
        if (!reconnectSince) reconnectSince = Date.now();
        if (Date.now() - reconnectSince > 5 * 60000) { setMessage('服务连接尚未恢复，请稍后刷新页面或检查服务器状态。', true); return; }
        reconnecting = true; paint(); queue();
      } else { setMessage('无法读取更新状态：' + error.message, true); if (info?.busy) queue(); }
    }
  }
  check.addEventListener('click', async () => {
    if (requesting || info?.busy) return;
    requesting = true; paint(); setMessage('正在检查 GitHub 正式版本…');
    try { info = await request('/api/settings/update/check', {}, 30000); }
    catch (error) { requesting = false; paint(); setMessage(error.message, true); return; }
    finally { requesting = false; }
    paint();
  });
  install.addEventListener('click', async () => {
    if (requesting || info?.busy || !info?.available || !info?.supported || !info?.asset_ready) return;
    if (settingsDirty || settingsSaving) { toast('请先保存设置，再更新程序', 'err'); return; }
    const version = info.latest_version;
    if (!await confirmAction('更新并重启', `更新至 ${version} 会重启服务，正在进行的请求可能中断，完成后需重新登录。账号、配置和数据保留，当前程序会备份，启动失败时自动恢复原程序。`, '更新并重启', false)) return;
    requesting = true; paint();
    remember(version);
    let installError = '';
    try {
      info = await request('/api/settings/update/install', { version });
      paint(); queue();
    } catch (error) {
      if (error.status) forget();
      installError = error.message;
      if (!error.status) { info = { ...info, busy: true }; queue(); }
    } finally { requesting = false; paint(); if (installError && active()) setMessage(installError, true); }
  });
  try {
    info = await request('/api/settings/update');
    if (!active()) return;
    paint();
    if (info.busy) { panel.open = true; queue(); }
    else {
      let pending;
      try { pending = JSON.parse(sessionStorage.getItem(pendingKey) || 'null'); } catch {}
      if (pending && pending.started_at > Date.now() - 30 * 60000 && ['completed', 'rolled_back', 'error'].includes(info.state?.phase)) {
        panel.open = true;
        toast(info.state.message || '更新状态已刷新', info.state.phase === 'completed' ? 'ok' : 'err');
      }
      if (!info.busy) forget();
    }
  } catch (error) {
    if (active()) { if (error.status === 401) { stopped = true; showLogin(); } else setMessage(error.message, true); }
  }
}
