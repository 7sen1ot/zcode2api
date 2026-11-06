'use strict';

const dashboardChartLayout = { width: 1120, height: 306, left: 68, right: 66, top: 38, bottom: 256 };
const dashboardChartColors = ['#ff4b16', '#ffbd0b', '#438cff', '#9470ed', '#25a887', '#ec7ba5'];
const chartCoord = value => Number(value).toFixed(2);
function dashboardChartMaximum(value) {
  const raw = Math.max(4, value), power = 10 ** Math.floor(Math.log10(raw));
  return [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find(step => step * power >= raw) * power;
}
function dashboardChartGrid(maximum, dual = false) {
  const { width, left, right, top, bottom } = dashboardChartLayout;
  return Array.from({ length: 5 }, (_, index) => {
    const fraction = 1 - index / 4, y = top + (bottom - top) * index / 4;
    return `<line class="dashboard-chart-grid${index === 4 ? ' baseline' : ''}" x1="${left}" x2="${width - right}" y1="${y}" y2="${y}"/><text class="dashboard-chart-tick" x="${left - 12}" y="${y + 4}" text-anchor="end">${dual ? (fraction * 100).toFixed(0) + '%' : fmtQuota(maximum * fraction)}</text>${dual ? `<text class="dashboard-chart-tick" x="${width - right + 14}" y="${y + 4}">${fmtQuota(maximum * fraction)}</text>` : ''}`;
  }).join('');
}
function dashboardChartCurve(points) {
  // Horizontal control handles keep the curve within each pair of values.
  return points.map((point, index) => {
    if (!index) return `M${chartCoord(point.x)},${chartCoord(point.y)}`;
    const previous = points[index - 1], middle = chartCoord((previous.x + point.x) / 2);
    return `C${middle},${chartCoord(previous.y)} ${middle},${chartCoord(point.y)} ${chartCoord(point.x)},${chartCoord(point.y)}`;
  }).join(' ');
}
function dashboardChartLegend(items) {
  return `<div class="dashboard-chart-legend">${items.map(item => `<span><i style="--series-color:${item.color}"></i>${esc(item.name)}</span>`).join('')}</div>`;
}
function bindDashboardChart(root, tooltipMarkup) {
  const tooltip = $('.dashboard-chart-tooltip', root);
  let selected = -1;
  const hide = () => {
    tooltip.hidden = true;
    root.querySelector('.dashboard-chart-hit.is-active')?.classList.remove('is-active');
    selected = -1;
  };
  const show = (group, event) => {
    if (!group) return;
    const index = Number(group.dataset.chartIndex);
    if (index !== selected) {
      root.querySelector('.dashboard-chart-hit.is-active')?.classList.remove('is-active');
      group.classList.add('is-active');
      tooltip.innerHTML = tooltipMarkup(index);
      selected = index;
    }
    tooltip.hidden = false;
    const rect = root.getBoundingClientRect(), point = group.getBoundingClientRect();
    const x = event?.clientX == null ? point.x + point.width / 2 - rect.x : event.clientX - rect.x;
    const y = event?.clientY == null ? point.y - rect.y + 40 : event.clientY - rect.y;
    tooltip.style.left = `${Math.max(8, Math.min(x + 16, rect.width - tooltip.offsetWidth - 8))}px`;
    tooltip.style.top = `${Math.max(8, Math.min(y - tooltip.offsetHeight - 14, rect.height - tooltip.offsetHeight - 8))}px`;
  };
  root.addEventListener('pointermove', event => show(event.target.closest('[data-chart-index]'), event));
  root.addEventListener('pointerleave', () => { if (!root.contains(document.activeElement)) hide(); });
  root.addEventListener('focusin', event => show(event.target.closest('[data-chart-index]')));
  root.addEventListener('focusout', event => { if (!root.contains(event.relatedTarget)) hide(); });
  root.addEventListener('click', event => {
    const group = event.target.closest('[data-chart-index]');
    if (group) { group.focus({ preventScroll: true }); show(group, event); }
  });
  root.addEventListener('keydown', event => {
    const group = event.target.closest('[data-chart-index]');
    if (!group) return;
    if (event.key === 'Escape') { hide(); return; }
    if (['Enter', ' '].includes(event.key)) { event.preventDefault(); show(group); return; }
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const groups = [...root.querySelectorAll('[data-chart-index]')];
    const current = groups.indexOf(group);
    const index = event.key === 'Home' ? 0 : event.key === 'End' ? groups.length - 1 : Math.max(0, Math.min(groups.length - 1, current + (event.key === 'ArrowRight' ? 1 : -1)));
    groups.forEach((item, i) => { item.tabIndex = i === index ? 0 : -1; });
    groups[index].focus({ preventScroll: true });
    groups[index].scrollIntoView({ block: 'nearest', inline: 'nearest' });
  });
}

function renderCacheTrendChart(root, stats) {
  const timeline = UI.usageTimeline(stats || { hours: 24 });
  const known = new Map((stats?.byHour || []).map(row => [row.bucket, row]));
  const rows = timeline.rows.map(row => {
    const raw = known.get(row.bucket), input = Number(raw?.tin) || 0, cached = Number(raw?.cached_tokens) || 0;
    return { ...row, input, cached, rate: input > 0 ? cached / input * 100 : null };
  });
  const { width, height, left, right, top, bottom } = dashboardChartLayout;
  const step = (width - left - right) / Math.max(1, rows.length - 1);
  const maximum = dashboardChartMaximum(Math.max(0, ...rows.map(row => row.tokens)));
  const ratePoints = rows.map((row, index) => ({ x: left + index * step, y: bottom - Math.min(100, row.rate || 0) / 100 * (bottom - top) }));
  const tokenPoints = rows.map((row, index) => ({ x: left + index * step, y: bottom - row.tokens / maximum * (bottom - top) }));
  const rateText = row => row.rate === null ? '无输入数据' : row.rate.toFixed(1) + '%';
  const caption = row => `${row.bucket.slice(5).replace('T', ' ')}${row.partial ? '（部分小时）' : ''}`;
  let selected = rows.findLastIndex(row => row.reqs > 0);
  if (selected < 0) selected = rows.length - 1;
  root.innerHTML = `<div class="dashboard-chart-scroll"><svg class="dashboard-chart-svg" viewBox="0 0 ${width} ${height}" role="group" aria-label="近 24 小时缓存命中率与总 Token 趋势">
    <g aria-hidden="true">${dashboardChartGrid(maximum, true)}
    <path class="dashboard-chart-line cache" d="${dashboardChartCurve(ratePoints)}"/><path class="dashboard-chart-line tokens" d="${dashboardChartCurve(tokenPoints)}"/>
    ${rows.map((row, index) => {
      const rate = ratePoints[index], tokens = tokenPoints[index];
      return `${row.rate !== null ? `<circle class="dashboard-chart-point cache" cx="${chartCoord(rate.x)}" cy="${chartCoord(rate.y)}" r="3"/><text class="dashboard-chart-value cache" x="${chartCoord(rate.x)}" y="${chartCoord(rate.y < bottom - 25 ? rate.y + 18 : rate.y - 10)}" text-anchor="middle">${row.rate.toFixed(1)}%</text>` : ''}${row.tokens ? `<circle class="dashboard-chart-point tokens" cx="${chartCoord(tokens.x)}" cy="${chartCoord(tokens.y)}" r="3"/><text class="dashboard-chart-value tokens" x="${chartCoord(tokens.x)}" y="${chartCoord(tokens.y - 12)}" text-anchor="middle">${fmtQuota(row.tokens)}</text>` : ''}${index % 2 === 0 || index === rows.length - 1 ? `<text class="dashboard-chart-tick" x="${chartCoord(rate.x)}" y="${bottom + 25}" text-anchor="middle">${row.bucket.slice(11, 16)}</text>` : ''}`;
    }).join('')}</g>
    ${rows.map((row, index) => `<g class="dashboard-chart-hit" data-chart-index="${index}" role="button" tabindex="${index === selected ? 0 : -1}" aria-label="${esc(caption(row))}，缓存命中率 ${rateText(row)}，总 Token ${fmtInt(row.tokens)}"><rect class="dashboard-chart-hit-area" x="${chartCoord(Math.max(left, left + index * step - step / 2))}" y="${top}" width="${chartCoord(index === 0 || index === rows.length - 1 ? step / 2 : step)}" height="${bottom - top}"/><line class="dashboard-chart-crosshair" x1="${chartCoord(ratePoints[index].x)}" x2="${chartCoord(ratePoints[index].x)}" y1="${top}" y2="${bottom}"/></g>`).join('')}
    </svg></div>${!rows.some(row => row.reqs) ? '<span class="dashboard-chart-empty">暂无调用数据</span>' : ''}${dashboardChartLegend([{ name: '缓存命中率（左轴）', color: '#1680ff' }, { name: '总 Token（右轴）', color: '#eb8b00' }])}<div class="dashboard-chart-tooltip" role="tooltip" hidden></div>`;
  bindDashboardChart(root, index => {
    const row = rows[index];
    return `<strong>${esc(caption(row))}</strong><div><span>缓存命中率</span><b class="cache">${rateText(row)}</b></div><p>命中 ${fmtInt(row.cached)} / 输入 ${fmtInt(row.input)}</p><div><span>总 Token</span><b>${fmtInt(row.tokens)}</b></div><div><span>请求数</span><b>${fmtInt(row.reqs)}</b></div>`;
  });
}

function renderDailyTokenChart(root, stats) {
  const rows = UI.usageTimeline(stats || { time_unit: 'day', hours: 168 }).rows;
  const values = new Map(), names = new Set();
  for (const row of stats?.byModel || []) {
    const name = row.model || '未知模型';
    if (!values.has(row.bucket)) values.set(row.bucket, new Map());
    const day = values.get(row.bucket);
    day.set(name, (day.get(name) || 0) + (Number(row.tin) || 0) + (Number(row.tout) || 0));
    names.add(name);
  }
  if (!names.size && rows.some(row => row.tokens)) {
    names.add('全部模型');
    rows.forEach(row => values.set(row.bucket, new Map([['全部模型', row.tokens]])));
  }
  const models = [...names].sort().map((name, index) => ({ name, color: dashboardChartColors[index % dashboardChartColors.length] }));
  const { width, height, left, right, top, bottom } = dashboardChartLayout;
  const step = (width - left - right) / rows.length, barWidth = Math.min(38, step * .55);
  const maximum = dashboardChartMaximum(Math.max(0, ...rows.map(row => row.tokens)));
  const dateLabel = row => row.bucket.slice(5).replace('-', '/');
  const peak = rows.reduce((best, row, index) => row.tokens > rows[best].tokens ? index : best, 0);
  let selected = rows.findLastIndex(row => row.tokens > 0);
  if (selected < 0) selected = rows.length - 1;
  root.innerHTML = `<div class="dashboard-chart-scroll"><svg class="dashboard-chart-svg" viewBox="0 0 ${width} ${height}" role="group" aria-label="按天和模型统计的 Token 用量">
    <g aria-hidden="true">${dashboardChartGrid(maximum)}${rows.map((row, index) => {
      const x = left + step * (index + .5), data = values.get(row.bucket);
      let stacked = 0;
      const bars = models.map(model => {
        const value = data?.get(model.name) || 0, barHeight = value / maximum * (bottom - top);
        stacked += barHeight;
        return value ? `<rect class="dashboard-chart-bar" x="${chartCoord(x - barWidth / 2)}" y="${chartCoord(bottom - stacked)}" width="${chartCoord(barWidth)}" height="${chartCoord(barHeight)}" fill="${model.color}"/>` : '';
      }).join('');
      return `${bars}${row.tokens && (rows.length <= 14 || index === peak || index === selected) ? `<text class="dashboard-chart-value" x="${chartCoord(x)}" y="${chartCoord(bottom - stacked - 12)}" text-anchor="middle">${fmtQuota(row.tokens)}</text>` : ''}${index % Math.ceil(rows.length / 8) === 0 || index === rows.length - 1 ? `<text class="dashboard-chart-tick" x="${chartCoord(x)}" y="${bottom + 25}" text-anchor="middle">${dateLabel(row)}</text>` : ''}`;
    }).join('')}</g>
    ${rows.map((row, index) => `<g class="dashboard-chart-hit" data-chart-index="${index}" role="button" tabindex="${index === selected ? 0 : -1}" aria-label="${esc(row.bucket)}，${fmtInt(row.tokens)} Token"><rect class="dashboard-chart-hit-area" x="${chartCoord(left + step * index)}" y="${top}" width="${chartCoord(step)}" height="${bottom - top}"/><line class="dashboard-chart-crosshair" x1="${chartCoord(left + step * (index + .5))}" x2="${chartCoord(left + step * (index + .5))}" y1="${top}" y2="${bottom}"/></g>`).join('')}
    </svg></div>${!rows.some(row => row.tokens) ? '<span class="dashboard-chart-empty">暂无 Token 用量</span>' : ''}${dashboardChartLegend(models)}<div class="dashboard-chart-tooltip" role="tooltip" hidden></div>`;
  bindDashboardChart(root, index => {
    const row = rows[index], data = values.get(row.bucket);
    return `<strong>${esc(row.bucket)}${row.partial ? '（截至当前）' : ''}</strong><div><span>总 Token</span><b>${fmtInt(row.tokens)}</b></div>${models.filter(model => data?.get(model.name)).map(model => `<div><span><i style="--series-color:${model.color}"></i>${esc(model.name)}</span><b>${fmtInt(data.get(model.name))}</b></div>`).join('')}`;
  });
}
