'use strict';

const tokenCacheCharts = new WeakMap();
const tokenCacheMotion = matchMedia('(prefers-reduced-motion: reduce)');
let tokenCacheChartID = 0;
window.addEventListener('resize', () => {
  document.querySelectorAll('.combo-chart-card').forEach(root => tokenCacheCharts.get(root)?.());
});

function tokenCacheCurve(points) {
  let previous = null;
  return points.map(point => {
    if (!point) { previous = null; return ''; }
    const middle = previous ? (previous.x + point.x) / 2 : 0;
    const command = previous ? `C${middle},${previous.y} ${middle},${point.y} ${point.x},${point.y}` : `M${point.x},${point.y}`;
    previous = point;
    return command;
  }).join(' ');
}

function renderTokenCacheChart(root, stats = {}, { rangeId = '', showTotals = true } = {}) {
  stats ||= {};
  const { rows, offset } = UI.usageTimeline(stats);
  const total = rows.reduce((sum, row) => ({ input: sum.input + row.input, cached: sum.cached + row.cached, tokens: sum.tokens + row.tokens }), { input: 0, cached: 0, tokens: 0 });
  total.rate = total.input > 0 ? total.cached / total.input * 100 : null;
  const hours = Number(stats.hours) || 24, daily = stats.time_unit === 'day';
  const range = hours === 168 ? '近 7 天' : hours === 720 ? '近 30 天' : '近 24 小时';
  const zone = `UTC${offset >= 0 ? '+' : '−'}${String(Math.floor(Math.abs(offset) / 60)).padStart(2, '0')}:${String(Math.abs(offset) % 60).padStart(2, '0')}`;
  const rateText = value => value === null ? '—' : `${value.toFixed(1)}%`;
  const caption = row => row.bucket.slice(5).replace('T', ' ').replace(':00:00', ':00') + (row.partial ? '（部分时段）' : '');
  const id = `token-cache-${++tokenCacheChartID}`;
  root.setAttribute('aria-labelledby', `${id}-title`);
  root.innerHTML = `<header class="combo-chart-heading">
    <div class="combo-heading-main"><div class="combo-chart-title"><h2 id="${id}-title">Token 与缓存</h2></div>
      <div class="combo-chart-context"><span data-combo-caption>${range}</span><span>命中率 <b data-combo-rate>${rateText(total.rate)}</b></span></div>
      <div class="combo-chart-amount"><strong data-combo-tokens title="${fmtInt(total.tokens)} Token">${fmtQuota(total.tokens)}</strong><span>Token</span></div>
    </div>
    <div class="combo-chart-range">${rangeId ? `<select id="${esc(rangeId)}" class="round" aria-label="统计时间范围"><option value="24">近 24 小时</option><option value="168">近 7 天</option><option value="720">近 30 天</option></select>` : `<span class="combo-range-label">${range}</span>`}</div>
  </header>
  <div class="combo-chart-plot"></div>
  <div class="combo-chart-tooltip" id="${id}-tooltip" role="tooltip" hidden></div>
  ${showTotals ? `<div class="combo-chart-tiles"><div class="combo-chart-tile"><span><i class="combo-bar-key" aria-hidden="true"></i>Token 合计</span><strong>${fmtInt(total.tokens)}</strong></div><div class="combo-chart-tile"><span><i class="combo-line-key" aria-hidden="true"></i>缓存命中率</span><strong>${rateText(total.rate)}</strong></div></div>` : ''}`;
  if (rangeId) root.querySelector('select').value = String(hours);
  const plot = root.querySelector('.combo-chart-plot'), tooltip = root.querySelector('.combo-chart-tooltip');
  const headingCaption = root.querySelector('[data-combo-caption]');
  const headingRate = root.querySelector('[data-combo-rate]');
  const headingTokens = root.querySelector('[data-combo-tokens]');
  let active = -1, hits = [], columns = [], pulse = null, width = 0, painted = false;
  const current = index => {
    const row = rows[index] || total;
    headingCaption.textContent = index < 0 ? range : row.bucket.slice(5).replace('T', ' ').replace(':00:00', ':00');
    headingRate.textContent = rateText(row.rate);
    headingTokens.textContent = fmtQuota(row.tokens);
    headingTokens.title = `${fmtInt(row.tokens)} Token`;
  };
  const clear = () => {
    active = -1;
    pulse?.cancel(); pulse = null;
    root.classList.remove('has-active-point');
    tooltip.hidden = true;
    hits.forEach(hit => { hit.classList.remove('is-active'); hit.removeAttribute('aria-describedby'); });
    columns.forEach(column => column.classList.remove('is-active'));
    current(-1);
  };
  const show = (index, event) => {
    const row = rows[index], hit = hits[index];
    if (!row || !hit) return;
    if (index !== active) {
      active = index;
      pulse?.cancel(); pulse = null;
      root.classList.add('has-active-point');
      columns.forEach((column, i) => column.classList.toggle('is-active', i === index));
      hits.forEach((item, i) => {
        item.classList.toggle('is-active', i === index);
        item.tabIndex = i === index ? 0 : -1;
        if (i === index) item.setAttribute('aria-describedby', tooltip.id);
        else item.removeAttribute('aria-describedby');
      });
      current(index);
      tooltip.innerHTML = `<strong>${esc(caption(row))}</strong><div><span><i class="combo-bar-key"></i>Token 消耗</span><b>${fmtInt(row.tokens)}</b></div><div><span><i class="combo-line-key"></i>缓存命中率</span><b>${rateText(row.rate)}</b></div><div class="combo-tooltip-divider"><span>缓存读取</span><b>${fmtInt(row.cached)}</b></div><div><span>输入 Token</span><b>${fmtInt(row.input)}</b></div><div><span>输出 Token</span><b>${fmtInt(row.output)}</b></div><div><span>请求数</span><b>${fmtInt(row.reqs)}</b></div>`;
      const ring = hit.querySelector('.combo-point-pulse');
      if (ring?.animate && !tokenCacheMotion.matches) pulse = ring.animate([{ transform: 'scale(.55)', opacity: .6 }, { transform: 'scale(1.5)', opacity: 0 }], { duration: 1000, iterations: 2 });
    }
    tooltip.hidden = false;
    const rect = root.getBoundingClientRect(), target = hit.getBoundingClientRect();
    const x = event?.clientX == null ? target.left + target.width / 2 - rect.left : event.clientX - rect.left;
    const y = event?.clientY == null ? plot.getBoundingClientRect().top - rect.top + 40 : event.clientY - rect.top;
    tooltip.style.left = `${Math.max(8, Math.min(x + 14, rect.width - tooltip.offsetWidth - 8))}px`;
    tooltip.style.top = `${Math.max(8, Math.min(y - tooltip.offsetHeight - 12, rect.height - tooltip.offsetHeight - 8))}px`;
  };
  function draw() {
    const nextWidth = Math.max(280, Math.round(plot.clientWidth));
    if (painted && nextWidth === width) return;
    const focused = plot.contains(document.activeElement), selected = active;
    pulse?.cancel(); pulse = null;
    width = nextWidth;
    const layout = { left: 54, right: width - 48, top: 28, bottom: 208, height: 244 };
    const { left, right, top, bottom, height } = layout;
    const step = (right - left) / Math.max(1, rows.length), barWidth = Math.min(30, Math.max(3, step * .58));
    const maximum = Math.max(4, ...rows.map(row => row.tokens));
    const power = 10 ** Math.floor(Math.log10(maximum));
    const ceiling = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find(value => value * power >= maximum) * power;
    const x = index => left + step * (index + .5);
    const y = rate => bottom - Math.max(0, Math.min(100, rate)) / 100 * (bottom - top);
    const points = rows.map((row, index) => row.rate === null ? null : { x: x(index), y: y(row.rate) });
    const lastUsed = rows.findLastIndex(row => row.reqs > 0 || row.tokens > 0);
    const tab = selected >= 0 ? selected : Math.max(0, lastUsed < 0 ? rows.length - 1 : lastUsed);
    const labelEvery = Math.max(1, Math.ceil(rows.length / Math.max(2, Math.floor((right - left) / (daily ? 60 : 54)))));
    const grid = Array.from({ length: 5 }, (_, index) => {
      const fraction = 1 - index / 4, yy = top + (bottom - top) * index / 4;
      return `<line class="combo-grid${index === 4 ? ' baseline' : ''}" x1="${left}" x2="${right}" y1="${yy}" y2="${yy}"/><text class="combo-axis" x="${left - 10}" y="${yy + 4}" text-anchor="end">${fmtQuota(ceiling * fraction)}</text><text class="combo-axis" x="${right + 10}" y="${yy + 4}">${(fraction * 100).toFixed(0)}%</text>`;
    }).join('');
    plot.innerHTML = `<svg class="combo-chart-svg" viewBox="0 0 ${width} ${height}" role="group" aria-label="${range} Token 消耗和缓存命中率，${zone}"><g aria-hidden="true"><text class="combo-axis-label" x="${left}" y="14">Token</text><text class="combo-axis-label" x="${right}" y="14" text-anchor="end">命中率</text>${grid}
      ${rows.map((row, index) => {
        const barHeight = row.tokens / ceiling * (bottom - top);
        const showLabel = index === rows.length - 1 || (index % labelEvery === 0 && (index === 0 || rows.length - 1 - index > labelEvery / 2));
        return `<g class="combo-column" data-combo-column="${index}">${row.tokens ? `<rect class="combo-bar" x="${x(index) - barWidth / 2}" y="${bottom - barHeight}" width="${barWidth}" height="${barHeight}" rx="${Math.min(4, barHeight / 2)}"/>` : ''}</g>${showLabel ? `<text class="combo-axis" x="${x(index)}" y="${bottom + 24}" text-anchor="middle">${daily ? row.bucket.slice(5).replace('-', '/') : row.bucket.slice(11, 16)}</text>` : ''}`;
      }).join('')}<path class="combo-line" d="${tokenCacheCurve(points)}"/>${points.filter(Boolean).map(point => `<circle class="combo-line-dot" cx="${point.x}" cy="${point.y}" r="2"/>`).join('')}</g>
      ${rows.map((row, index) => `<g class="combo-hit" data-combo-index="${index}" role="button" tabindex="${index === tab ? '0' : '-1'}" aria-label="${esc(caption(row))}，${fmtInt(row.tokens)} Token，缓存命中率${row.rate === null ? '无输入数据' : rateText(row.rate)}"><rect class="combo-hit-area" x="${left + step * index}" y="${top}" width="${step}" height="${bottom - top}"/><line class="combo-cursor" x1="${x(index)}" x2="${x(index)}" y1="${top}" y2="${bottom}"/>${row.rate !== null ? `<circle class="combo-point-pulse" cx="${x(index)}" cy="${y(row.rate)}" r="7"/><circle class="combo-active-dot" cx="${x(index)}" cy="${y(row.rate)}" r="4"/>` : ''}</g>`).join('')}</svg>${rows.some(row => row.reqs || row.tokens) ? '' : '<div class="combo-chart-empty">暂无调用数据</div>'}`;
    hits = [...plot.querySelectorAll('.combo-hit')];
    columns = [...plot.querySelectorAll('.combo-column')];
    if (!painted && !tokenCacheMotion.matches && !root.closest('.no-anim')) {
      plot.querySelectorAll('.combo-bar').forEach((bar, index) => bar.animate?.([{ transform: 'scaleY(.03)', opacity: .3 }, { transform: 'scaleY(1)', opacity: 1 }], { duration: 360, delay: Math.min(index * 8, 120), fill: 'backwards', easing: 'cubic-bezier(.22, 1, .36, 1)' }));
    }
    painted = true;
    active = -1;
    if (selected >= 0) show(selected);
    if (focused) hits[tab]?.focus({ preventScroll: true });
  }
  plot.addEventListener('pointermove', event => {
    const hit = event.target.closest('[data-combo-index]');
    if (hit) show(Number(hit.dataset.comboIndex), event);
    else if (!plot.contains(document.activeElement)) clear();
  });
  plot.addEventListener('pointerleave', () => {
    const focused = plot.querySelector('.combo-hit:focus');
    if (focused) show(Number(focused.dataset.comboIndex));
    else clear();
  });
  plot.addEventListener('focusin', event => {
    const hit = event.target.closest('[data-combo-index]');
    if (hit) show(Number(hit.dataset.comboIndex));
  });
  plot.addEventListener('focusout', event => { if (!plot.contains(event.relatedTarget)) clear(); });
  plot.addEventListener('click', event => {
    const hit = event.target.closest('[data-combo-index]');
    if (hit) { hit.focus({ preventScroll: true }); show(Number(hit.dataset.comboIndex), event); }
  });
  plot.addEventListener('keydown', event => {
    const hit = event.target.closest('[data-combo-index]');
    if (!hit) return;
    if (event.key === 'Escape') { clear(); return; }
    if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); show(Number(hit.dataset.comboIndex)); return; }
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = Number(hit.dataset.comboIndex);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, index + (event.key === 'ArrowRight' ? 1 : -1)));
    hits[next]?.focus({ preventScroll: true });
  });
  plot.addEventListener('scroll', () => {
    const hit = plot.querySelector('.combo-hit:focus');
    if (hit) show(Number(hit.dataset.comboIndex));
    else clear();
  });
  tokenCacheCharts.set(root, draw);
  draw();
}
