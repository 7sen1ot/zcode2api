'use strict';

globalThis.Z2AUI = (() => {
  const pages = ['dashboard', 'accounts', 'claims', 'keys', 'usage', 'logs', 'groups', 'settings'];
  function readRoute(hash) {
    const [path, search = ''] = String(hash || '').replace(/^#\/?/, '').split('?');
    return { page: pages.includes(path) ? path : 'dashboard', query: Object.fromEntries(new URLSearchParams(search)) };
  }
  function routeHash(page, values = {}) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(values)) {
      if (value !== undefined && value !== null && value !== '') query.set(key, String(value));
    }
    return `#/${pages.includes(page) ? page : 'dashboard'}${query.size ? '?' + query : ''}`;
  }
  function usageTimeline(stats, fallbackNow = Date.now()) {
    const end = Number(stats.range_end) || fallbackNow;
    const start = Number(stats.range_start) || end - (Number(stats.hours) || 24) * 3600000;
    const offset = Number.isFinite(stats.utc_offset_minutes) ? stats.utc_offset_minutes : -new Date(end).getTimezoneOffset();
    const step = stats.time_unit === 'day' ? 86400000 : 3600000;
    const shifted = end + offset * 60000;
    const last = Math.floor(shifted / step) * step;
    const first = Math.floor((start + offset * 60000) / step) * step;
    const known = new Map((stats.byHour || []).map(row => [row.bucket, row]));
    const rows = [];
    for (let ts = first; ts <= last; ts += step) {
      const iso = new Date(ts).toISOString();
      const bucket = step === 86400000 ? iso.slice(0, 10) : iso.slice(0, 13) + ':00:00';
      const row = known.get(bucket);
      rows.push({ bucket, reqs: Number(row?.reqs) || 0, ok: Number(row?.ok) || 0,
        tokens: (Number(row?.tin) || 0) + (Number(row?.tout) || 0), partial: ts < start + offset * 60000 || ts + step > shifted });
    }
    return { rows, offset, start, end };
  }
  function paginate(items, page = 1, pageSize = 25) {
    const size = Math.max(1, Math.min(100, Math.floor(Number(pageSize)) || 25));
    const pages = Math.max(1, Math.ceil(items.length / size));
    const current = Math.max(1, Math.min(pages, Math.floor(Number(page)) || 1));
    const start = (current - 1) * size;
    return { items: items.slice(start, start + size), page: current, pageSize: size, pages, total: items.length, start: items.length ? start + 1 : 0, end: Math.min(start + size, items.length) };
  }
  return Object.freeze({ readRoute, routeHash, usageTimeline, paginate });
})();
