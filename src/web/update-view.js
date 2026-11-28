const githubUpdateMirrors = ['https://gh-proxy.org', 'https://gh-proxy.com'];

function selectedUpdateMirror(mode, custom) {
  return (mode === 'custom' ? custom : mode).trim().replace(/\/+$/, '');
}

function settingsUpdateMarkup(mirror) {
  const base = selectedUpdateMirror(String(mirror ?? ''), '');
  const custom = base !== '' && !githubUpdateMirrors.includes(base);
  return `<div class="update-panel-body">
      <div class="settings-fields update-versions"><div><span id="update-current-label">当前版本</span><strong id="update-current">${esc(RUNTIME?.version || '—')}</strong></div><div id="update-latest-row" hidden><span id="update-latest-label">最新发行版本</span><strong id="update-latest">未检查</strong></div></div>
      <div class="settings-fields">
        <label class="f" for="s-update-mirror"><span>GitHub 发行版镜像 ${infoTip('自定义镜像须填写 HTTPS 基址。HTTP 代理请在「设置 → 出站代理」配置。', 'GitHub 发行版镜像')}</span><select id="s-update-mirror">
          <option value="">GitHub 原站</option>
          ${githubUpdateMirrors.map(url => `<option value="${url}" ${url === base ? 'selected' : ''}>${url}/</option>`).join('')}
          <option value="custom" ${custom ? 'selected' : ''}>自定义</option>
        </select></label>
        <label class="f" id="update-mirror-custom" for="s-update-mirror-custom" ${custom ? '' : 'hidden'}>镜像基址<input id="s-update-mirror-custom" type="url" autocomplete="off" spellcheck="false" maxlength="2048" value="${esc(custom ? base : '')}" placeholder="https://mirror.example.com/" ${custom ? '' : 'disabled'}></label>
      </div>
      <div class="update-toolbar"><a class="text-action update-repository" href="https://github.com/7sen1ot/zcode2api" target="_blank" rel="noopener noreferrer">7sen1ot/zcode2api ↗</a><div class="update-actions"><a class="btn" id="update-manual" href="https://github.com/7sen1ot/zcode2api/releases" target="_blank" rel="noopener noreferrer" hidden>查看发行包 ↗</a><button type="button" class="btn" id="update-check" hidden>${icon('refresh', 14)}检查更新</button><button type="button" class="btn primary" id="update-install" hidden>更新并重启</button></div></div>
      <progress id="update-progress" aria-label="发行包下载进度" hidden></progress>
      <p id="update-message" class="update-message" role="status" aria-live="polite"></p>
      <div class="update-notes" id="update-notes" hidden>
        <div id="update-notes-body"><h3>发布说明</h3><pre id="update-notes-content"></pre></div>
        <a id="update-release" class="text-action" href="https://github.com/7sen1ot/zcode2api/releases" target="_blank" rel="noopener noreferrer">查看发行版本 ↗</a>
      </div>
    </div>`;
}

async function mountSettingsUpdate(main, mirrorChanged) {
  const panel = $('#update-panel', main), check = $('#update-check', panel), install = $('#update-install', panel);
  const summary = $('#update-summary', main);
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
    const canCheck = !!info?.can_check, available = !!info?.available;
    const sourceMode = !!info?.source_check, source = info?.source;
    const current = $('#update-current', panel), latest = $('#update-latest', panel);
    $('#update-current-label', panel).textContent = sourceMode ? '本地源码提交' : '当前版本';
    $('#update-latest-label', panel).textContent = sourceMode ? '远端提交' : '最新发行版本';
    current.textContent = sourceMode ? source?.local_commit?.slice(0, 12) || '—' : info?.current_version || RUNTIME?.version || '—';
    current.title = source?.local_commit || '';
    latest.textContent = source ? source.upstream + ' · ' + source.remote_commit.slice(0, 12) : info?.latest_version || '未检查';
    latest.title = source?.remote_commit || '';
    $('#update-latest-row', panel).hidden = !canCheck;
    summary.hidden = !busy && !available;
    summary.textContent = busy ? '更新中' : sourceMode ? '有新提交' : '可更新';
    check.hidden = !canCheck;
    check.disabled = requesting || busy;
    check.classList.toggle('loading', requesting && !busy);
    install.hidden = !available || !info?.supported;
    install.disabled = requesting || busy || !info?.supported || !info?.asset_ready;
    install.title = info?.reason || info?.asset_reason || '';
    const manual = $('#update-manual', panel);
    manual.hidden = sourceMode || !canCheck || !!info?.supported;
    const container = info?.deployment === 'container';
    const releaseURL = info?.release_url || 'https://github.com/7sen1ot/zcode2api/releases';
    manual.textContent = container ? '查看镜像 ↗' : '查看发行包 ↗';
    manual.href = container ? 'https://github.com/7sen1ot/zcode2api/pkgs/container/zcode2api' : releaseURL;
    progress.hidden = !busy;
    if (state.phase === 'downloading' && state.total > 0) {
      progress.max = state.total; progress.value = Math.min(state.downloaded || 0, state.total);
    } else progress.removeAttribute('value');
    const link = $('#update-release', panel);
    const notes = info?.release_notes || '';
    const hasNotes = !!notes.trim();
    link.href = releaseURL;
    link.hidden = !manual.hidden && !container;
    $('#update-notes', panel).hidden = !info?.latest_version || (!hasNotes && link.hidden);
    $('#update-notes-body', panel).hidden = !hasNotes;
    $('#update-notes-content', panel).textContent = notes;
    if (reconnecting) setMessage('服务重启中，正在重连…');
    else if (source && !busy) {
      const messages = { current: '本地提交与远端一致', behind: '远端有新提交，可手动更新', ahead: '本地提交领先远端', diverged: '本地与远端已分叉，请手动核对' };
      setMessage(messages[source.relation] || (source.local_commit ? '提交不同，请手动核对来源和历史' : '当前构建缺少本地提交信息，无法判断是否有更新'));
    }
    else if (state.message && (busy || state.phase === 'error' || canCheck && !sourceMode)) setMessage(state.message, ['error', 'rolled_back'].includes(state.phase));
    else if (!info?.supported && info?.reason) setMessage(info.reason);
    else if (available && !info.asset_ready) {
      setMessage(info.asset_reason || (container ? '该版本暂无可用容器镜像' : '该版本暂无可用发行包'), true);
    }
    else if (info?.latest_version && !available) setMessage('已是最新发行版本');
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
        toast('请重新登录', 'ok');
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
    if (requesting || info?.busy || !info?.can_check) return;
    if (!info.source_check && mirrorChanged()) { setMessage('请先保存 GitHub 镜像设置，再检查更新。', true); return; }
    requesting = true; paint(); setMessage(info.source_check ? '正在检查远端提交…' : '正在检查 GitHub 正式版本…');
    try { info = await request('/api/settings/update/check', {}, 30000); }
    catch (error) { requesting = false; paint(); setMessage(error.message, true); return; }
    finally { requesting = false; }
    paint();
  });
  install.addEventListener('click', async () => {
    if (requesting || info?.busy || !info?.available || !info?.supported || !info?.asset_ready) return;
    if (settingsDirty || settingsSaving) { toast('请先保存', 'err'); return; }
    const version = info.latest_version;
    const deployment = info.deployment === 'container'
      ? '宿主机将拉取新镜像并重建容器，保留已挂载的数据和配置。启动失败时恢复原镜像。'
      : '服务会保留账号、配置和数据，并备份当前程序。启动失败时恢复原版本。';
    const restartMode = info.deployment === 'standalone' ? '重启后程序将在后台运行。' : '';
    const action = install.textContent;
    const details = `更新至 ${version} 时，正在进行的请求可能中断。${deployment}${restartMode}`;
    if (!await confirmAction(action, details, action, false)) return;
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
        toast(info.state.phase === 'completed' ? '更新完成' : info.state.message || '更新失败', info.state.phase === 'completed' ? 'ok' : 'err');
      }
      if (!info.busy) forget();
    }
  } catch (error) {
    if (active()) { if (error.status === 401) { stopped = true; showLogin(); } else setMessage(error.message, true); }
  }
}
