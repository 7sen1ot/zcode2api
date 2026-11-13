'use strict';

globalThis.Z2ACacheDetails = (() => {
  function usage(row) {
    const input = Number(row.tokens_in), read = Number(row.cache_read), write = Number(row.cache_write);
    const known = row.tokens_in != null && row.cache_read != null && Number.isFinite(input) && Number.isFinite(read) && input > 0 && read >= 0 && read <= input;
    const ordinary = known && row.cache_write != null && Number.isFinite(write) && write >= 0 && read + write <= input ? input - read - write : null;
    return { input, read, write, ordinary, rate: known ? read / input * 100 : null };
  }
  const percent = value => value == null ? '—' : Number(value).toFixed(2) + '%';

  function breakdown(row, helpers) {
    const { esc, fmtInt } = helpers, u = usage(row);
    if (u.ordinary === null) return '';
    const segments = [['ordinary', '普通输入', u.ordinary], ['read', '缓存读取', u.read], ['write', '缓存写入', u.write]];
    return `<div class="cache-breakdown" role="img" aria-label="${esc(segments.map(([, name, n]) => `${name} ${fmtInt(n)} Token`).join('，'))}">${segments.filter(([, , n]) => n > 0).map(([kind, name, n]) => `<span class="${kind}" style="width:${n / u.input * 100}%" title="${esc(name + ' ' + fmtInt(n))}"></span>`).join('')}</div><div class="cache-breakdown-legend">${segments.map(([kind, name, n]) => `<span><i class="${kind}"></i>${name}<b>${fmtInt(n)}</b></span>`).join('')}</div>`;
  }

  function detailFields(row, helpers) {
    const { esc, fmtInt } = helpers, u = usage(row);
    const fields = [['总输入 Token', fmtInt(row.tokens_in)], ['普通输入', fmtInt(u.ordinary)], ['缓存读取', fmtInt(row.cache_read)], ['缓存写入', fmtInt(row.cache_write)], ['输出 Token', fmtInt(row.tokens_out)], ['命中率', percent(u.rate)], ['首 Token 时间', row.ttft_ms == null ? '—' : fmtInt(row.ttft_ms) + ' ms'], ['尝试次数', fmtInt(row.attempt_count)], ['密钥', row.key_name || row.key_id || '—'], ['账号', row.account_label || row.account_id || '—'], ['请求 ID', row.request_id], ['追踪 ID', row.trace_id || '—']];
    const detail = row.error_detail || {};
    for (const [label, value] of [['错误码', row.error_code], ['上游 HTTP 状态', detail.http_status], ['上游请求 ID', detail.request_id], ['ZCode 查询 ID', detail.query_id], ['ZCode 会话 ID', detail.session_id]]) {
      if (value != null && value !== '') fields.push([label, value]);
    }
    return fields.map(([label, value]) => `<div${label.endsWith(' ID') ? ' class="cache-detail-wide"' : ''}><dt>${label}</dt><dd>${esc(value)}</dd></div>`).join('');
  }

  function errorDetails(row, esc) {
    const detail = row.error_detail || {}, summary = row.error_message || '';
    const message = detail.message && detail.message !== summary ? `<pre class="request-error-message">${esc(detail.message)}</pre>` : '';
    let response = '';
    if (detail.body != null) {
      let body = String(detail.body);
      try { body = JSON.stringify(JSON.parse(body), null, 2); } catch { /* Preserve non-JSON upstream responses. */ }
      response = `<details class="request-error-response" open><summary>上游响应</summary><pre>${esc(body || '（空响应体）')}</pre></details>`;
    }
    const headers = detail.response_headers && Object.keys(detail.response_headers).length ? `<details class="request-error-response"><summary>响应头</summary><pre>${esc(JSON.stringify(detail.response_headers, null, 2))}</pre></details>` : '';
    const readError = detail.read_error ? `<p class="cache-detail-error">${esc(detail.read_error)}</p>` : '';
    return `${summary ? `<p class="cache-detail-error">${esc(summary)}</p>` : ''}${message}${response}${headers}${readError}`;
  }

  function dialogMarkup() {
    return `<dialog class="cache-detail-dialog" aria-labelledby="cache-detail-title"><header class="cache-detail-header"><div class="cache-detail-heading"><h2 id="cache-detail-title">请求详情</h2><span class="cache-detail-caption"></span></div><button type="button" class="cache-detail-close" aria-label="关闭请求详情">×</button></header><div class="cache-detail-body"></div></dialog>`;
  }

  function bind(main, rows, helpers) {
    let dialog = main.querySelector('.cache-detail-dialog');
    if (!dialog && main.querySelector('[data-cache-detail]')) {
      main.insertAdjacentHTML('beforeend', dialogMarkup());
      dialog = main.querySelector('.cache-detail-dialog');
    }
    if (!dialog) return;
    const { esc, fmtTime, badge } = helpers;
    main.querySelectorAll('[data-cache-detail]').forEach(button => button.addEventListener('click', () => {
      const row = rows[Number(button.dataset.cacheDetail)];
      const caption = dialog.querySelector('.cache-detail-caption');
      caption.textContent = `${row.model || '未知模型'} · ${fmtTime(row.ts)}`;
      caption.title = caption.textContent;
      dialog.querySelector('.cache-detail-body').innerHTML = `<div class="cache-detail-status">${badge(row.status)}</div>${breakdown(row, helpers)}<dl>${detailFields(row, helpers)}</dl>${errorDetails(row, esc)}`;
      dialog.showModal();
    }));
    dialog.querySelector('.cache-detail-close').addEventListener('click', () => dialog.close());
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
  }

  return Object.freeze({ bind, hitRate: row => usage(row).rate });
})();
