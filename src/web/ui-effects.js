'use strict';

globalThis.Z2AEffects = (() => {
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const copies = new WeakMap();
  let region, stack, expand, announcements, notices = [];
  let hovered = false, pinned = false, paused = false;

  function layoutNotices() {
    if (!region) return;
    const expanded = hovered || pinned || stack.contains(document.activeElement);
    region.classList.toggle('is-expanded', expanded);
    region.hidden = !notices.length;
    expand.hidden = notices.length < 2;
    expand.textContent = `${expanded ? '收起' : '展开'} ${notices.length} 条通知`;
    expand.setAttribute('aria-expanded', String(expanded));
    notices.forEach((notice, index) => {
      notice.element.hidden = !expanded && index > 2;
      notice.element.style.setProperty('--toast-index', index);
      notice.element.style.zIndex = notices.length - index;
      const now = performance.now();
      if (expanded && !paused) {
        clearTimeout(notice.timer);
        notice.remaining = Math.max(0, notice.remaining - (now - notice.started));
      } else if (!expanded && paused) armNotice(notice);
    });
    paused = expanded;
    stack.style.setProperty('--toast-height', `${(notices[0]?.element.offsetHeight || 48) + Math.min(2, notices.length - 1) * 8}px`);
  }

  function armNotice(notice) {
    notice.started = performance.now();
    notice.timer = setTimeout(() => dismissNotice(notice), notice.remaining);
  }

  function dismissNotice(notice) {
    const focused = notice.element.contains(document.activeElement);
    clearTimeout(notice.timer);
    notices = notices.filter(item => item !== notice);
    notice.element.classList.add('out');
    notice.element.setAttribute('aria-hidden', 'true');
    notice.element.querySelector('button').tabIndex = -1;
    setTimeout(() => notice.element.remove(), 180);
    if (!notices.length) pinned = hovered = false;
    if (focused) notices[0]?.element.querySelector('button').focus({ preventScroll: true });
    layoutNotices();
  }

  function toast(message, type) {
    if (!region) {
      region = h('<aside class="toast-region" aria-label="通知"><button type="button" class="toast-expand" aria-expanded="false" hidden></button><div class="toast-stack"></div><div class="visually-hidden" role="status" aria-live="polite" aria-atomic="true"></div></aside>');
      stack = region.querySelector('.toast-stack');
      expand = region.querySelector('.toast-expand');
      announcements = region.querySelector('[role="status"]');
      expand.addEventListener('click', () => { pinned = !pinned; layoutNotices(); });
      region.addEventListener('pointerenter', event => { if (event.pointerType !== 'touch') { hovered = true; layoutNotices(); } });
      region.addEventListener('pointerleave', () => { hovered = false; layoutNotices(); });
      region.addEventListener('focusin', layoutNotices);
      region.addEventListener('focusout', () => requestAnimationFrame(layoutNotices));
      region.addEventListener('keydown', event => {
        if (event.key !== 'Escape') return;
        pinned = hovered = false;
        region.querySelectorAll(':focus').forEach(element => element.blur());
        layoutNotices();
      });
      document.body.append(region);
    }
    const duplicate = notices.find(item => item.message === message && item.type === type);
    if (duplicate) {
      clearTimeout(duplicate.timer);
      duplicate.remaining = type === 'err' ? 6500 : 4000;
      if (!paused) armNotice(duplicate);
      return;
    }
    if (notices.length >= 5) dismissNotice(notices.at(-1));
    const element = h(`<div class="toast" data-tone="${type === 'err' ? 'error' : type === 'ok' ? 'success' : 'info'}"><span class="toast-symbol">${icon(type === 'ok' ? 'check' : type === 'err' ? 'zap' : 'info', 16)}</span><span class="toast-message">${esc(message)}</span><button type="button" class="toast-close" aria-label="关闭通知">×</button></div>`);
    const notice = { element, message, type, remaining: type === 'err' ? 6500 : 4000, started: performance.now(), timer: 0 };
    element.querySelector('button').addEventListener('click', () => dismissNotice(notice));
    notices.unshift(notice);
    stack.prepend(element);
    if (!paused) armNotice(notice);
    layoutNotices();
    announcements.textContent = '';
    requestAnimationFrame(() => { announcements.textContent = message; });
  }

  function copyFeedback(button, message) {
    if (!button?.isConnected) return false;
    let state = copies.get(button);
    if (!state) {
      const content = document.createElement('span');
      content.className = 'copy-original';
      content.append(...button.childNodes);
      const result = h(`<span class="copy-result" role="status">${icon('check', 15)}<span>${esc(message)}</span></span>`);
      result.querySelector('span').classList.toggle('visually-hidden', button.offsetWidth < 100);
      state = { content, label: button.getAttribute('aria-label'), title: button.getAttribute('title') };
      button.append(content, result);
      copies.set(button, state);
    }
    clearTimeout(state.timer);
    button.setAttribute('aria-label', message);
    button.title = message;
    button.classList.add('is-copied');
    state.timer = setTimeout(() => {
      button.classList.remove('is-copied');
      button.replaceChildren(...state.content.childNodes);
      for (const [attribute, value] of [['aria-label', state.label], ['title', state.title]]) {
        if (value === null) button.removeAttribute(attribute);
        else button.setAttribute(attribute, value);
      }
      copies.delete(button);
    }, 2000);
    return true;
  }

  function tabs(nav) {
    if (!nav) return;
    const active = nav.querySelector('.tab.active');
    if (!active) return;
    let indicator = nav.querySelector('.tab-indicator');
    const fresh = !indicator;
    if (fresh) {
      indicator = document.createElement('span');
      indicator.className = 'tab-indicator';
      indicator.setAttribute('aria-hidden', 'true');
      nav.prepend(indicator);
    }
    if (fresh) indicator.style.transition = 'none';
    indicator.style.width = `${active.offsetWidth}px`;
    indicator.style.transform = `translateX(${active.offsetLeft - nav.clientLeft}px)`;
    nav.classList.add('has-tab-indicator');
    if (fresh) requestAnimationFrame(() => { indicator.style.transition = ''; });
    if (active.offsetLeft < nav.scrollLeft || active.offsetLeft + active.offsetWidth > nav.scrollLeft + nav.clientWidth) {
      nav.scrollTo({ left: Math.max(0, active.offsetLeft - 4), behavior: reducedMotion.matches ? 'instant' : 'smooth' });
    }
  }
  window.addEventListener('resize', () => {
    document.querySelectorAll('.has-tab-indicator').forEach(tabs);
    layoutNotices();
  });

  function skeleton(page) {
    const line = (width = '100%', extra = '') => `<span class="skeleton-line ${extra}" style="width:${width}"></span>`;
    const card = `<div class="statcard skeleton-surface">${line('46%')}${line('64%', 'skeleton-value')}${line('72%')}</div>`;
    const chart = `<div class="skeleton-surface skeleton-chart">${line('22%')}${line('44%', 'skeleton-value')}<div class="skeleton-plot">${[32, 49, 40, 68, 53, 76, 60, 90, 70, 82, 62, 74].map(height => `<span class="skeleton-line" style="height:${height}%"></span>`).join('')}</div></div>`;
    const table = `<div class="skeleton-surface skeleton-table">${Array.from({ length: 6 }, () => `<div>${line('42%')}${line('65%')}${line('48%')}${line('56%')}</div>`).join('')}</div>`;
    if (page === 'dashboard') return `<div class="page-skeleton" aria-hidden="true"><div class="dashboard-overview"><div class="quota-hero skeleton-surface">${line('28%')}${line('56%', 'skeleton-hero')}<div class="skeleton-pair">${line('70%')}${line('70%')}</div></div><div class="pool-health skeleton-surface">${line('46%')}${line('70%', 'skeleton-hero')}${line('32%')}</div></div><div class="dashboard-secondary">${card.repeat(3)}</div>${chart}</div>`;
    if (page === 'usage') return `<div class="page-skeleton" aria-hidden="true"><div class="cards multicol usage-summary">${card.repeat(4)}</div>${chart}${table}</div>`;
    if (page === 'settings') return `<div class="page-skeleton skeleton-settings" aria-hidden="true"><div class="skeleton-surface">${Array.from({ length: 7 }, () => line('80%')).join('')}</div><div class="skeleton-surface">${Array.from({ length: 5 }, () => `<div class="skeleton-pair">${line('60%')}${line('100%', 'skeleton-control')}</div>`).join('')}</div></div>`;
    return `<div class="page-skeleton" aria-hidden="true"><div class="skeleton-pair skeleton-toolbar">${line('40%', 'skeleton-control')}${line('60%', 'skeleton-control')}</div>${table}</div>`;
  }

  function reveal(root) {
    if (reducedMotion.matches || !root.animate) return;
    const elements = root.id === 'main' ? [...root.children].filter(element => !element.matches('.pagehead')).slice(0, 5) : [root];
    elements.forEach((element, index) => element.animate(
      [{ opacity: .25, transform: 'translateY(5px)' }, { opacity: 1, transform: 'translateY(0)' }],
      { duration: 220, delay: index * 24, easing: 'cubic-bezier(.22, 1, .36, 1)' },
    ));
  }
  return { toast, copyFeedback, tabs, skeleton, reveal };
})();
