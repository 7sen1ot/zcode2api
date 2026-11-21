'use strict';

const SETTINGS_GROUPS = [
  ['服务', [['gateway', '网关与模型'], ['template', '请求模板'], ['poll', '额度轮询']]],
  ['连接', [['proxy', '出站代理'], ['captcha', '上游安全验证']]],
  ['维护', [['log-retention', '日志保留'], ['update', '版本更新']]],
];
const SETTINGS_SECTIONS = SETTINGS_GROUPS.flatMap(([, sections]) => sections);
const DEFAULT_SETTINGS_SECTION = SETTINGS_SECTIONS[0][0];
const SETTINGS_JSON_COMMENTS = {
  aliases: {
    'glm-5.3': 'GLM-5.3 的默认别名',
    'glm-5.3-flash': 'GLM-5.3-Flash 的默认别名',
    'glm-flash': 'GLM-5.3-Flash 的默认短别名',
  },
  retry: {
    backoff_factor: '重试等待时间的增长倍数',
    base_delay_ms: '首次退避等待时间（毫秒）',
    busy_wait_sec: '等待可用账号的时限（秒）',
    jitter: '随机调整等待时间，分散重试',
    max_attempts: '常规失败的尝试次数上限',
    max_delay_ms: '加入抖动前的退避上限（毫秒）',
    min_request_window_sec: '账号卡最短剩余有效期（秒）',
    rate_limit_retry_interval_ms: '上游未指定时的限流重试间隔（毫秒）',
    total_budget_ms: '排队与故障重试的总预算（毫秒）',
  },
  http: {
    first_token_timeout_ms: '等待首次模型输出的时限',
    idle_timeout_ms: '上游无有效响应的最长等待时间',
    total_timeout_ms: '单次推理的总时限，0 表示不限',
  },
};
const compactSettings = matchMedia('(max-width: 759px)');
let settingsSection = '';
let showSettingsSection = null;
compactSettings.addEventListener('change', () => showSettingsSection?.());

function formatSettingsJSON(value, comments) {
  const json = JSON.stringify(value, null, 2) ?? '';
  return json.replace(/^  "([\w.-]+)":[^\n]*/gm, (line, key) =>
    Object.hasOwn(comments, key) ? `${line}  ## ${comments[key]}` : line);
}

function parseSettingsJSON(text) {
  // Match quoted strings first to preserve ## inside JSON keys and values.
  const json = text.replace(/"(?:[^"\\\r\n]|\\[^\r\n])*"|##[^\r\n]*/g, token =>
    token.startsWith('##') ? '' : token);
  return JSON.parse(json);
}

function readSettingsSection(section) {
  return SETTINGS_SECTIONS.some(([id]) => id === section) ? section : '';
}

function settingsNavMarkup() {
  return `<nav class="settings-nav" aria-label="设置分类">${SETTINGS_GROUPS.map(([name, sections]) => `
    <div class="settings-nav-group">
      <h2>${name}</h2>
      <div class="settings-nav-list">${sections.map(([id, label]) => `
        <a class="settings-link" data-settings-link="${id}" href="${UI.routeHash('settings', { section: id })}">
          <span class="settings-link-label">${label}${id === 'update' ? '<span class="settings-link-status" id="update-summary" hidden></span>' : ''}</span>
          ${icon('chevron-right', 13)}
        </a>`).join('')}</div>
    </div>`).join('')}</nav>`;
}

function settingsPanel(id, content) {
  const [, label] = SETTINGS_SECTIONS.find(([section]) => section === id);
  return `<section class="settings-panel" id="${id}-panel" data-settings-panel="${id}" aria-labelledby="${id}-heading" hidden>
    <h2 id="${id}-heading" tabindex="-1">${label}</h2>
    <div class="settings-panel-body">${content}</div>
  </section>`;
}

function selectSettingsSection(section, { focus = true, replace = false } = {}) {
  if (settingsSection === section) return;
  settingsSection = section;
  syncRoute(replace);
  showSettingsSection?.({ focus });
}

function bindSettingsSections(main, onSectionChange) {
  const layout = $('.settings-layout', main);
  const form = $('#settings-form', layout);
  const panels = $$('[data-settings-panel]', form);
  const links = $$('[data-settings-link]', layout);
  let previous = null;

  showSettingsSection = ({ focus = false } = {}) => {
    if (!layout.isConnected) return false;
    const selected = settingsSection || DEFAULT_SETTINGS_SECTION;
    const panel = panels.find(node => node.dataset.settingsPanel === selected);
    const changed = previous !== settingsSection;
    if (changed) {
      if (openSelection && form.contains(openSelection.wrapper)) openSelection.close();
      if (activeHint && form.contains(activeHint)) closeHint();
    }
    main.classList.toggle('settings-detail', !!settingsSection);
    for (const node of panels) node.hidden = node !== panel;
    for (const link of links) {
      if (link.dataset.settingsLink === selected && (settingsSection || !compactSettings.matches)) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    }
    if (changed && previous !== null) onSectionChange(selected);
    if (changed && focus) {
      window.scrollTo({ top: 0, behavior: 'instant' });
      if (compactSettings.matches) {
        const target = settingsSection ? $('h2', panel) : links.find(link => link.dataset.settingsLink === previous) || links[0];
        target.focus({ preventScroll: true });
      }
    }
    previous = settingsSection;
    return true;
  };

  main.querySelector('.settings-nav').addEventListener('click', e => {
    const link = e.target.closest('[data-settings-link]');
    if (!link || !isPlainLinkClick(e)) return;
    e.preventDefault();
    selectSettingsSection(link.dataset.settingsLink);
  });
  main.querySelector('.settings-back').addEventListener('click', e => {
    if (!isPlainLinkClick(e)) return;
    e.preventDefault();
    selectSettingsSection('');
  });
  showSettingsSection();
}

async function pageSettings(main) {
  const [s, mihomo] = await Promise.all([api('/api/settings'), api('/api/settings/proxy/mihomo').catch(() => ({ unavailable: true }))]);
  const [mihomoOS, mihomoArch] = (mihomo.platform || '').split('/');
  const mihomoTarget = mihomo.platform ? `${({ windows: 'Windows', linux: 'Linux', darwin: 'macOS' })[mihomoOS] || mihomoOS} / ${({ amd64: 'x64', arm64: 'ARM64' })[mihomoArch] || mihomoArch}` : '';
  const httpParams = { ...s.http };
  const captcha = { mode: 'off', playwright_path: '', browser_path: '', node_path: 'node', jsdom_path: '', simulate_canvas_webgl: true, timeout_ms: 30000, ticket_ttl_ms: 75000, ...s.http.captcha };
  const proxy = { enabled: false, source: 'manual', urls: [], subscription_url: '', preferred_node: '', mihomo_path: 'mihomo', refresh_interval_sec: 3600, ...s.outbound_proxy };
  const subagentFields = { zcode_subagent_insert_enabled: 's-subagent-insert', zcode_subagent_harness_enabled: 's-subagent-harness', zcode_subagent_files_enabled: 's-subagent-files', zcode_subagent_comments_enabled: 's-subagent-comments' };
  delete httpParams.client_version;
  delete httpParams.captcha;
  main.innerHTML = `<header class="pagehead settings-header">
      <h1 class="tt" id="settings-title">设置</h1>
      <a class="settings-back" href="${UI.routeHash('settings')}">${icon('chevron-left', 16)}所有设置</a>
      <div class="settings-actions">
        <span class="settings-dirty" id="settings-dirty" aria-hidden="true" hidden>未保存</span>
        <button type="submit" form="settings-form" class="btn primary settings-save" id="s-save" aria-describedby="settings-state" disabled>保存设置</button>
      </div>
    </header>
    <p id="settings-state" class="visually-hidden" role="status" aria-live="polite"></p>
    <p id="settings-error" class="inline-notice error hidden" role="alert"></p>
    <div class="settings-layout">
      ${settingsNavMarkup()}
      <form id="settings-form" class="settings-form control-comfortable" novalidate>
      ${settingsPanel('update', settingsUpdateMarkup(s.github_update_mirror))}
      ${settingsPanel('log-retention', `<div class="settings-fields">
        <div class="field-group f">
          <label for="s-log-retention">调用记录保留天数</label>
          <input id="s-log-retention" type="number" min="1" step="1" required value="${esc(s.usage_log_retention_days)}" aria-describedby="s-log-retention-hint">
          <small id="s-log-retention-hint">超过保留期的记录会自动删除，删除后无法恢复。</small>
        </div>
      </div>`)}
      ${settingsPanel('template', `
        <div class="settings-fields"><label class="f" for="s-template-mode"><span>模板类型 ${infoTip('main 固定使用原始三段式模板。subagent 可独立勾选正文内容；保存后同时切换模板与请求头。', '请求模板')}</span><select id="s-template-mode" required><option value="main">main（默认原始三段式）</option><option value="subagent">subagent</option></select></label></div>
        <div class="settings-stack template-custom-fields" id="s-subagent-options" hidden>
          <div class="template-identity"><code aria-label="固定身份标识">You are ZCode, an interactive agent</code>${infoTip('身份标识不可修改。全部取消勾选时，模板正文为空；勾选正文时保留固定的代理开头。', '固定身份标识')}</div>
          <div class="settings-fields" role="group" aria-label="subagent 可选正文">
            <label class="settings-check"><input id="s-subagent-insert" type="checkbox"><span>Web 插入内容</span></label>
            <label class="settings-check"><input id="s-subagent-harness" type="checkbox"><span>Harness</span></label>
            <label class="settings-check"><input id="s-subagent-files" type="checkbox"><span>Files &amp; URLs</span></label>
            <label class="settings-check"><input id="s-subagent-comments" type="checkbox"><span>Inline Code Comments</span></label>
          </div>
          <div class="code-field template-custom-fields" id="s-template-custom-fields" hidden><label class="f" for="s-subagent-template"><span>插入内容 ${infoTip('留空使用内置的 internal/server/system-prompt.md。此处内容排在勾选的固定段落之前；客户端 system / developer 指令仍会追加。', '插入内容')}</span></label><textarea id="s-subagent-template" class="code-input" rows="10" spellcheck="false" maxlength="262144" placeholder="留空使用 internal/server/system-prompt.md">${esc(s.zcode_subagent_template || '')}</textarea></div>
        </div>
      `)}
      ${settingsPanel('poll', `<div class="settings-fields">
        <label class="f">轮询间隔（秒）<input id="s-poll" type="number" min="1" step="1" required value="${s.poll_interval_sec}"></label>
        <label class="f" for="s-jitter"><span>随机抖动（秒） ${infoTip('分散多个账号的余额检查时间。', '随机抖动')}</span><input id="s-jitter" type="number" min="0" step="1" required value="${s.poll_jitter_sec}"></label>
        <label class="f" for="s-conc"><span>轮询并发 ${infoTip('同时检查余额的账号数量。', '轮询并发')}</span><input id="s-conc" type="number" min="1" max="${RUNTIME?.account_operation_concurrency ?? 3}" step="1" required value="${s.poll_concurrency}"></label>
      </div>`)}
      ${settingsPanel('captcha', `
        <div class="settings-subsection"><h3>模型推理</h3><div class="settings-fields">
          <label class="f">模型推理验证策略<select id="s-captcha-mode" aria-describedby="s-captcha-mode-hint"><option value="off">关闭模型推理验证</option><option value="auto">按上游要求验证</option><option value="always">每次推理前验证</option></select><small id="s-captcha-mode-hint" aria-live="polite"></small></label>
          <label class="f" for="s-captcha-jsdom"><span>jsdom 依赖目录</span><input id="s-captcha-jsdom" value="${esc(captcha.jsdom_path)}" placeholder="留空使用 captcha_node"><small id="s-jsdom-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></label>
          <label class="settings-check" for="s-captcha-canvas"><input id="s-captcha-canvas" type="checkbox" role="switch" ${captcha.simulate_canvas_webgl ? 'checked' : ''}><span>jsdom 渲染接口模拟 ${infoTip('仅为 jsdom 验证补充 Canvas、WebGL 等兼容接口，不影响真实浏览器，也不保证通过上游验证。', '渲染接口模拟')}</span></label>
        </div></div>
        <div class="settings-subsection"><h3>领卡验证</h3><div class="settings-fields">
          <div class="field-group f"><span>定时领卡验证方式</span><strong>服务端真实浏览器（固定）</strong><small id="s-claim-browser-status" class="runtime-status" role="status" aria-live="polite">正在检测浏览器依赖…</small></div>
          <div class="mihomo-field" id="claim-browser-fields"><div class="mihomo-field-heading"><label for="s-browser-path">服务端浏览器路径</label><span>${infoTip('需要 Node.js、Playwright 及 Chromium 内核浏览器，支持 Chromium / Chrome / Edge。定时领取前 3 分钟预热，最晚保留至计划时间后 10 分钟，完成后提前退出。', '领卡浏览器')} <button type="button" class="btn proxy-small-button" id="s-browser-install" disabled>检测中…</button></span></div><input id="s-browser-path" value="${esc(captcha.browser_path)}" placeholder="自动检测 Chromium / Chrome / Edge"><small id="s-browser-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></div>
          <label class="f" for="s-playwright-path"><span>Playwright 驱动目录 ${infoTip('可填写 playwright-core、playwright 包目录，或包含它的 node_modules / 项目目录。留空自动检测。', 'Playwright 搜索路径')}</span><input id="s-playwright-path" value="${esc(captcha.playwright_path)}" placeholder="自动检测，或填写已有 Playwright 目录"><small id="s-playwright-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></label>
        </div></div>
        <div class="settings-subsection"><h3>运行环境</h3><div class="settings-fields">
          <label class="f" for="s-captcha-node"><span>Node.js 可执行文件 ${infoTip('模型推理验证和定时领卡需要 Node.js 18 或以上版本。', 'Node.js 依赖')}</span><input id="s-captcha-node" value="${esc(captcha.node_path)}" placeholder="node"><small id="s-node-status" class="runtime-status" role="status" aria-live="polite">检测中…</small></label>
          <label class="f">验证超时（毫秒）<input id="s-captcha-timeout" type="number" min="5000" max="120000" step="1" required value="${captcha.timeout_ms}"></label>
          <label class="f" for="s-captcha-ttl"><span>jsdom 票据预取有效期（毫秒） ${infoTip('连续验证成功后按需预取。设为 0 关闭预取。', '票据预取')}</span><input id="s-captcha-ttl" type="number" min="0" max="90000" step="1" required value="${captcha.ticket_ttl_ms}"></label>
        </div></div>
      `)}
      ${settingsPanel('proxy', `
        <div class="settings-fields"><label class="settings-check" for="s-proxy-enabled"><input id="s-proxy-enabled" type="checkbox" role="switch" ${proxy.enabled ? 'checked' : ''}><span>全局代理 ${infoTip('关闭后全局使用直连。', '全局代理')}</span></label>
          <label class="f">代理来源<select id="s-proxy-source"><option value="manual">HTTP / HTTPS 地址列表</option><option value="subscription">节点订阅（需要 Mihomo）</option></select></label></div>
        <div id="proxy-manual-fields" class="settings-fields"><label class="f settings-wide" for="s-proxy-urls"><span>代理地址（每行一个） ${infoTip('仅支持 HTTP/HTTPS 代理。', '代理地址')}</span><textarea id="s-proxy-urls" class="code-input" rows="3" spellcheck="false" placeholder="http://127.0.0.1:7890&#10;http://user:password@proxy.example:8080">${esc(proxy.urls.join('\n'))}</textarea></label></div>
        <div id="proxy-subscription-fields" class="settings-subsection">
          <h3>节点订阅</h3><div class="settings-fields">
          <label class="f settings-wide" for="s-proxy-subscription"><span>HTTPS 订阅地址 ${infoTip('支持 VLESS TCP/WS、Hysteria2、HTTP/HTTPS，最多 64 个节点。', '订阅地址')}</span><input id="s-proxy-subscription" type="text" autocomplete="off" spellcheck="false" value="${esc(proxy.subscription_url)}" placeholder="https://provider.example/subscribe?token=…"></label>
          <div class="mihomo-field"><div class="mihomo-field-heading"><label for="s-proxy-mihomo">Mihomo 可执行文件</label><span class="mihomo-field-actions"><a class="text-action" href="https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.33" target="_blank" rel="noopener noreferrer">手动下载</a><button type="button" class="btn proxy-small-button" id="s-mihomo-install" ${mihomo.installed || mihomo.supported === false ? 'disabled' : ''} title="${esc(mihomoTarget ? `安装到服务端 ${mihomoTarget}` : '安装 Mihomo')}">${mihomo.installed ? '已安装' : '安装 Mihomo'}</button></span></div><input id="s-proxy-mihomo" value="${esc(proxy.mihomo_path)}" placeholder="mihomo"><small id="s-mihomo-status" role="status" aria-live="polite">${mihomo.installed ? '已检测到可执行文件' : mihomo.supported === false ? '当前平台请手动安装 Mihomo' : mihomo.unavailable ? '暂时无法检测安装状态' : '可填写已有路径或点击安装'}</small></div>
          <label class="f">订阅刷新间隔（秒）<input id="s-proxy-refresh" type="number" min="60" max="86400" step="1" value="${proxy.refresh_interval_sec}"></label></div>
        </div>
        <input id="s-proxy-node" type="hidden" value="${esc(proxy.preferred_node)}">
        <section class="proxy-node-box" id="proxy-node-box" aria-labelledby="s-proxy-nodes-heading"><div class="proxy-node-heading"><h3 id="s-proxy-nodes-heading">代理节点</h3><span id="s-proxy-node-count">未加载</span></div><div class="proxy-node-content">
          <div class="proxy-test-actions"><small id="s-proxy-node-status" role="status" aria-live="polite">加载节点后可指定代理，选择后保存生效。</small><button type="button" class="btn proxy-small-button" id="s-proxy-load">加载节点</button></div>
          <div id="s-proxy-nodes" class="proxy-node-list" role="radiogroup" aria-label="使用的代理节点"></div>
        </div></section>
        <div class="proxy-test-actions"><button type="button" class="btn" id="s-proxy-test">检测当前代理配置</button></div>
        <div id="s-proxy-result" class="proxy-test-results hidden" role="status" aria-live="polite"></div>
      `)}
      ${settingsPanel('gateway', `
        <div class="settings-fields"><label class="f" for="s-level"><span>默认思考强度 ${infoTip('优先级为客户端、密钥、此处设置。低强度仍开启思考，客户端关闭思考时不覆盖。始终思考的模型可能返回 1210，此时请在客户端开启思考。', '思考强度')}</span><select id="s-level" required><option value="low">低（low）</option><option value="high">高（high）</option><option value="max">最高（max）</option></select></label></div>
        <div class="settings-subsection"><h3>ZCode 请求指纹</h3>
          <div class="settings-fields">
            <label class="f"><span>已核对版本 ${infoTip('版本标识随请求指纹代码固定更新，需一并核对请求头、元数据和系统模板。', '请求指纹版本')}</span><input value="${esc(s.zcode_fingerprint_version)}" readonly></label>
          </div>
        </div>
        <div class="settings-subsection"><h3>高级参数</h3>
          <div class="settings-fields">
            <div class="code-field">
              <label class="f" for="s-aliases">模型别名（JSON 对象）</label>
              <textarea id="s-aliases" class="code-input" spellcheck="false" rows="6" aria-describedby="s-aliases-hint">${esc(formatSettingsJSON(s.aliases, SETTINGS_JSON_COMMENTS.aliases))}</textarea>
              <p id="s-aliases-hint" class="json-hint" data-for="s-aliases" aria-live="polite"></p>
            </div>
            <div class="code-field">
              <label class="f" for="s-retry">重试参数（JSON 对象）</label>
              <textarea id="s-retry" class="code-input" spellcheck="false" rows="6" aria-describedby="s-retry-hint">${esc(formatSettingsJSON(s.retry, SETTINGS_JSON_COMMENTS.retry))}</textarea>
              <p id="s-retry-hint" class="json-hint" data-for="s-retry" aria-live="polite"></p>
            </div>
            <div class="code-field">
              <label class="f" for="s-http">HTTP 超时（JSON 对象，毫秒）</label>
              <textarea id="s-http" class="code-input" spellcheck="false" rows="5" aria-describedby="s-http-hint">${esc(formatSettingsJSON(httpParams, SETTINGS_JSON_COMMENTS.http))}</textarea>
              <p id="s-http-hint" class="json-hint" data-for="s-http" aria-live="polite"></p>
            </div>
          </div>
        </div>`)}
      </form>
    </div>`;
  const form = $('#settings-form'), save = $('#s-save'), status = $('#settings-state'), dirtyIndicator = $('#settings-dirty');
  const updateMirrorMode = $('#s-update-mirror', form), updateMirrorCustom = $('#s-update-mirror-custom', form);
  const updateMirrorValue = () => selectedUpdateMirror(updateMirrorMode.value, updateMirrorCustom.value);
  void mountSettingsUpdate(main, () => updateMirrorValue() !== selectedUpdateMirror(String(s.github_update_mirror ?? ''), ''));
  const captchaTTLField = $('#s-captcha-ttl');
  const validateCaptchaTTL = () => {
    const value = Number(captchaTTLField.value);
    captchaTTLField.setCustomValidity(value === 0 || (Number.isInteger(value) && value >= 30000 && value <= 90000) ? '' : '请输入 0（关闭预取）或 30000 到 90000 的整数');
  };
  captchaTTLField.addEventListener('input', validateCaptchaTTL);
  $('#s-level').value = s.default_reasoning_level || 'low';
  $('#s-template-mode').value = s.zcode_template_mode || 'main';
  for (const [key, id] of Object.entries(subagentFields)) $('#' + id).checked = !!s[key];
  const syncTemplateFields = () => {
    const subagent = $('#s-template-mode').value === 'subagent', insert = subagent && $('#s-subagent-insert').checked, editor = $('#s-subagent-template');
    $('#s-subagent-options').hidden = !subagent;
    for (const id of Object.values(subagentFields)) $('#' + id).disabled = !subagent;
    $('#s-template-custom-fields').hidden = !insert;
    editor.disabled = !insert;
    editor.setCustomValidity(insert && new TextEncoder().encode(editor.value).length > 262144 ? '自定义插入内容不能超过 256 KiB' : '');
  };
  $('#s-captcha-mode').value = captcha.mode;
  $('#s-proxy-source').value = proxy.source;
  const captchaFields = { mode: 's-captcha-mode', playwright_path: 's-playwright-path', browser_path: 's-browser-path', timeout_ms: 's-captcha-timeout', simulate_canvas_webgl: 's-captcha-canvas', node_path: 's-captcha-node', jsdom_path: 's-captcha-jsdom', ticket_ttl_ms: 's-captcha-ttl' };
  const ids = ['s-poll', 's-jitter', 's-conc', 's-log-retention', 's-level', 's-template-mode', 's-subagent-template', ...Object.values(subagentFields), 's-aliases', 's-retry', 's-http', ...Object.values(captchaFields), 's-proxy-enabled', 's-proxy-source', 's-proxy-urls', 's-proxy-subscription', 's-proxy-node', 's-proxy-mihomo', 's-proxy-refresh', 's-update-mirror', 's-update-mirror-custom'];
  const fields = ids.map(id => $('#' + id));
  const values = () => Object.fromEntries(fields.map(input => [input.id, input.type === 'checkbox' ? input.checked : input.value]));
  const apply = data => fields.forEach(input => { if (Object.hasOwn(data, input.id)) { if (input.type === 'checkbox') input.checked = !!data[input.id]; else input.value = data[input.id]; syncSelect(input); } });
  const captchaValues = data => Object.fromEntries(Object.entries(captchaFields).map(([key, id]) => {
    let value = data[id];
    if (key.endsWith('_path')) value = value.trim() || (key === 'node_path' ? 'node' : '');
    else if (key.endsWith('_ms')) value = Number(value);
    return [key, value];
  }));
  let fingerprint = JSON.stringify(values()), savedAt = '';
  const draftKey = 'z2a_settings_draft';
  const updateDirty = () => {
    const customMirror = updateMirrorMode.value === 'custom';
    $('#update-mirror-custom', form).hidden = !customMirror;
    updateMirrorCustom.disabled = !customMirror;
    settingsDirty = JSON.stringify(values()) !== fingerprint;
    dirtyIndicator.hidden = !settingsDirty || settingsSaving;
    save.disabled = !settingsDirty || settingsSaving;
    save.textContent = settingsSaving ? '正在保存…' : savedAt && !settingsDirty ? '已保存' : '保存设置';
    save.setAttribute('aria-busy', String(settingsSaving));
    status.textContent = settingsSaving ? '正在保存…' : settingsDirty ? '有未保存修改 · 普通参数草稿已保留' : savedAt || '所有设置已保存';
    try { if (settingsDirty) { const draft = values(); delete draft['s-proxy-urls']; delete draft['s-proxy-subscription']; sessionStorage.setItem(draftKey, JSON.stringify(draft)); } else sessionStorage.removeItem(draftKey); }
    catch { if (settingsDirty) status.textContent = '有未保存修改 · 浏览器暂时无法保存草稿，请及时保存'; }
    save.title = status.textContent;
  };
  try {
    const draft = JSON.parse(sessionStorage.getItem(draftKey) || 'null');
    if (draft && typeof draft === 'object' && !Array.isArray(draft)) {
      if (draft['s-template-mode'] === 'subagent_custom') {
        draft['s-template-mode'] = 'subagent';
        for (const id of Object.values(subagentFields)) if (!Object.hasOwn(draft, id)) draft[id] = true;
      }
      try {
        const oldHttp = parseSettingsJSON(draft['s-http']);
        if (oldHttp && Object.hasOwn(oldHttp, 'client_version')) {
          delete oldHttp.client_version; draft['s-http'] = formatSettingsJSON(oldHttp, SETTINGS_JSON_COMMENTS.http);
        }
        if (oldHttp?.captcha) {
          for (const [field, id] of Object.entries(captchaFields)) {
            if (Object.hasOwn(oldHttp.captcha, field) && !Object.hasOwn(draft, id)) draft[id] = oldHttp.captcha[field];
          }
          delete oldHttp.captcha; draft['s-http'] = formatSettingsJSON(oldHttp, SETTINGS_JSON_COMMENTS.http);
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
        const parsed = parseSettingsJSON(box.value);
        if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('请输入 JSON 对象，而非数组或其他值');
        if (id === 's-http' && Object.hasOwn(parsed, 'client_version')) throw new Error('ZCode 版本随请求指纹代码固定更新，请移除 client_version');
        if (id === 's-http' && Object.hasOwn(parsed, 'captcha')) throw new Error('请在“上游安全验证”设置验证码，并从此处移除 captcha');
        box.classList.remove('invalid'); box.setAttribute('aria-invalid', 'false'); hint.textContent = ''; hint.classList.remove('error');
      } catch (error) {
        valid = false; box.classList.add('invalid'); box.setAttribute('aria-invalid', 'true'); hint.textContent = '配置格式错误：' + error.message; hint.classList.add('error');
      }
    }
    return valid;
  };
  const outboundValues = (data = values()) => ({ enabled: data['s-proxy-enabled'], source: data['s-proxy-source'], urls: [...new Set(data['s-proxy-urls'].split(/\r?\n/).map(x => x.trim()).filter(Boolean))], subscription_url: data['s-proxy-subscription'].trim(), subscription_filter: '', preferred_node: data['s-proxy-node'], mihomo_path: data['s-proxy-mihomo'].trim() || 'mihomo', refresh_interval_sec: Number(data['s-proxy-refresh']) || 3600 });
  const runtimeFields = { node: $('#s-node-status'), jsdom: $('#s-jsdom-status'), playwright: $('#s-playwright-status'), browser: $('#s-browser-status') };
  const browserInstall = $('#s-browser-install');
  const claimBrowserStatus = $('#s-claim-browser-status');
  const captchaPathIDs = Object.entries(captchaFields).filter(([key]) => key.endsWith('_path')).map(([, id]) => id);
  const captchaRuntimePaths = () => Object.fromEntries(Object.entries(captchaValues(values())).filter(([key]) => key.endsWith('_path')));
  let captchaRuntimeRequest = 0, captchaRuntimeTimer, browserInstalling = false, lastRuntime = null;
  const runtimePending = () => {
    browserInstall.disabled = true; lastRuntime = null;
    claimBrowserStatus.textContent = '正在检测浏览器依赖…'; claimBrowserStatus.classList.remove('warn-text');
    Object.values(runtimeFields).forEach(note => { note.textContent = '检测中…'; note.classList.remove('ok-text', 'warn-text'); note.title = ''; });
  };
  const checkCaptchaRuntime = async () => {
    clearTimeout(captchaRuntimeTimer);
    if (!form.isConnected || browserInstalling) return;
    const paths = captchaRuntimePaths(), snapshot = JSON.stringify(paths), request = ++captchaRuntimeRequest;
    runtimePending();
    try {
      const info = await api('/api/settings/captcha/runtime?' + new URLSearchParams(paths));
      if (!form.isConnected || request !== captchaRuntimeRequest || snapshot !== JSON.stringify(captchaRuntimePaths())) return;
      if (info.ok === false) throw new Error(info.error || '检测失败');
      for (const [key, note] of Object.entries(runtimeFields)) {
        const state = info[key] || {};
        note.textContent = state.available ? `${key === 'browser' ? '已找到 · ' + state.path.split(/[/\\]/).at(-1) : '可用'}${state.version ? ' · ' + (key === 'node' ? 'v' : '') + state.version : ''}` : state.message || '不可用';
        note.title = state.path || ''; note.classList.toggle('ok-text', !!state.available); note.classList.toggle('warn-text', !state.available);
      }
      lastRuntime = info;
      const missing = [['node', 'Node.js 18+'], ['playwright', 'Playwright 驱动'], ['browser', 'Chromium / Chrome / Edge']].filter(([key]) => !info[key]?.available).map(([, name]) => name);
      claimBrowserStatus.textContent = missing.length ? '定时领卡无法运行：请配置 ' + missing.join('、') : '浏览器依赖已找到，执行时检查能否启动';
      claimBrowserStatus.classList.toggle('warn-text', missing.length > 0);
      browserInstall.disabled = !!info.browser_ready || !info.install_available || (!!paths.playwright_path && !info.playwright?.available) || (!!paths.browser_path && !info.browser?.installed);
      browserInstall.textContent = info.browser_ready ? '依赖已找到' : info.browser?.installed ? '安装 Playwright' : '安装浏览器与驱动';
    } catch (error) {
      if (form.isConnected && request === captchaRuntimeRequest) {
        Object.values(runtimeFields).forEach(note => { note.textContent = '检测失败'; note.title = error.message; note.classList.add('warn-text'); });
        claimBrowserStatus.textContent = '依赖检测失败，请重新展开此面板'; claimBrowserStatus.classList.add('warn-text');
      }
    }
  };
  browserInstall.addEventListener('click', async () => {
    if (browserInstall.disabled || !lastRuntime || browserInstalling) return;
    const paths = captchaRuntimePaths(), snapshot = JSON.stringify(paths), installBrowser = !lastRuntime.browser?.installed;
    const message = installBrowser
      ? '将在服务端下载 Chromium 无头浏览器及缺失的 Playwright 驱动，占用磁盘空间；下载文件会保留，不自动安装系统依赖。是否安装？'
      : '已找到现有浏览器，仅在服务端安装 Playwright 驱动，占用少量磁盘空间。是否安装？';
    if (!await confirmAction('安装浏览器运行时', message, '安装', false) || !form.isConnected || snapshot !== JSON.stringify(captchaRuntimePaths())) return;
    browserInstalling = true; captchaRuntimeRequest++; clearTimeout(captchaRuntimeTimer);
    browserInstall.disabled = true; btnLoading(browserInstall, true); browserInstall.textContent = '安装中…';
    runtimeFields.browser.textContent = installBrowser ? '正在安装 Chromium…' : '正在安装 Playwright 驱动…';
    try {
      const response = await api('/api/settings/captcha/browser/install', { method: 'POST', body: { ...paths, install_browser: installBrowser } });
      if (response.ok === false) throw new Error(response.error || '安装失败');
      if (form.isConnected) toast(response.browser_ready ? '浏览器运行时已安装' : '驱动已安装，仍需配置浏览器', 'ok');
    } catch (error) { if (form.isConnected) toast(error.message, 'err'); }
    finally { browserInstalling = false; btnLoading(browserInstall, false); if (form.isConnected) void checkCaptchaRuntime(); }
  });
  const mihomoField = $('#s-proxy-mihomo'), mihomoButton = $('#s-mihomo-install'), mihomoNote = $('#s-mihomo-status');
  const mihomoPath = () => mihomoField.value.trim() || 'mihomo';
  let mihomoRequest = 0, mihomoTimer, mihomoInstalling = false, lastMihomo = mihomo;
  const showMihomoStatus = (info, note = '') => {
    lastMihomo = info;
    mihomoButton.disabled = mihomoInstalling || !!info.installed || info.supported === false;
    mihomoButton.textContent = info.installed ? '已检测到' : '安装 Mihomo';
    mihomoNote.textContent = note || (info.unavailable ? '暂时无法检测 Mihomo。' : info.installed ? `已检测到可执行文件${info.path ? '：' + info.path : ''}` : info.supported === false ? '未找到 Mihomo，当前平台请手动安装。' : '未找到 Mihomo，可填写已有路径或点击安装。');
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
    off: '仅关闭模型推理验证，不关闭领卡验证。',
    auto: '按上游要求使用 jsdom 验证，不影响领卡。',
    always: '上游启用验证时，每次模型推理前用 jsdom 取票。',
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
    if (captchaPathIDs.includes(e.target.id)) {
      clearTimeout(captchaRuntimeTimer); captchaRuntimeRequest++; runtimePending(); captchaRuntimeTimer = setTimeout(checkCaptchaRuntime, 250);
    }
    validateJSON(); syncTemplateFields(); syncNetworkFields(); updateDirty(); if (e.target.id.startsWith('s-proxy-')) $('#s-proxy-result').classList.add('hidden');
  } });
  form.addEventListener('change', e => { if (ids.includes(e.target.id)) {
    if (e.target.id === 's-proxy-source') { resetProxyNodeSelection(); $('#s-proxy-result').classList.add('hidden'); if ($('#s-proxy-source').value === 'subscription') void checkMihomo(); }
    if (e.target.id === 's-proxy-mihomo') void checkMihomo();
    if (captchaPathIDs.includes(e.target.id)) void checkCaptchaRuntime();
    syncTemplateFields(); syncNetworkFields(); updateDirty();
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
      result.innerHTML = `<details class="proxy-test-details"><summary class="disclosure-heading"><strong>检测结果</strong><span>${rows.length} 个节点 · 可达 ${reachable} · 失败 ${rows.length - reachable}</span></summary><div class="proxy-test-body"><ul>${rows.map(row => `<li><strong>${esc(row.name)}</strong><span class="${row.reachable ? 'ok-text' : 'warn-text'}">${esc(row.reachable ? '出口可达' : row.message)}${row.status ? ` · HTTP ${Number(row.status)}` : ''} · ${Number(row.elapsed_ms)} ms</span></li>`).join('')}</ul></div></details>`;
    } catch (error) { result.textContent = '检测失败：' + error.message; }
    finally { button.disabled = false; btnLoading(button, false); }
  });
  validateJSON(); validateCaptchaTTL(); syncTemplateFields(); syncNetworkFields(); updateDirty();
  bindSettingsSections(main, section => {
    if (section === 'captcha') void checkCaptchaRuntime();
    if (section === 'proxy' && outboundValues().source === 'subscription') void checkMihomo();
  });
  void checkCaptchaRuntime();
  if (mihomoPath() !== (proxy.mihomo_path.trim() || 'mihomo')) void checkMihomo();
  const initialProxy = outboundValues();
  if (initialProxy.source === 'subscription' ? initialProxy.subscription_url : initialProxy.urls.length) void loadProxyNodes();
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (settingsSaving || !settingsDirty) return;
    const error = $('#settings-error'); error.classList.add('hidden');
    const revealField = field => {
      const panel = field?.closest('[data-settings-panel]');
      if (panel) selectSettingsSection(panel.dataset.settingsPanel, { focus: false, replace: true });
    };
    if (!validateJSON()) {
      const invalid = $('.invalid', form);
      revealField(invalid);
      invalid?.focus(); return;
    }
    const invalidField = form.querySelector(':invalid');
    if (invalidField) {
      revealField(invalidField);
      invalidField.reportValidity();
      return;
    }
    const submitted = values();
    const submittedMirror = updateMirrorValue();
    settingsSaving = true; btnLoading(save, true); updateDirty();
    try {
      const response = await api('/api/settings', { method: 'PUT', body: {
        poll_interval_sec: Number(submitted['s-poll']), poll_jitter_sec: Number(submitted['s-jitter']), poll_concurrency: Number(submitted['s-conc']),
        usage_log_retention_days: Number(submitted['s-log-retention']),
        github_update_mirror: submittedMirror,
        default_reasoning_level: submitted['s-level'],
        zcode_template_mode: submitted['s-template-mode'], zcode_subagent_template: submitted['s-subagent-template'],
        ...Object.fromEntries(Object.entries(subagentFields).map(([key, id]) => [key, submitted[id]])),
        http: { ...parseSettingsJSON(submitted['s-http']), captcha: captchaValues(submitted) },
        outbound_proxy: outboundValues(submitted), aliases: parseSettingsJSON(submitted['s-aliases']), retry: parseSettingsJSON(submitted['s-retry']),
      } });
      if (response.ok === false) throw new Error(response.error || '保存失败');
      s.github_update_mirror = submittedMirror;
      fingerprint = JSON.stringify(submitted);
      savedAt = '已保存并生效 · ' + new Date().toLocaleTimeString('zh-CN', { hour12: false });
      toast('设置已保存并生效', 'ok');
    } catch (err) { error.textContent = '保存失败，修改已保留：' + err.message; error.classList.remove('hidden'); }
    finally { settingsSaving = false; btnLoading(save, false); updateDirty(); }
  });
}

