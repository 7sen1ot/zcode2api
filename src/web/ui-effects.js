'use strict';

globalThis.Z2AEffects = (() => {
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const copies = new WeakMap();
  const toastConfig = { width: 208, gap: 8, maxVisible: 3, duration: 220, lifetime: 4000, errorLifetime: 6500 };
  const exitingNotices = new Set();
  let region, stack, announcements, toastEasing, notices = [], announcementFrame = 0;
  let hovered = false, paused = false;

  function updateNoticePause() {
    const nextPaused = hovered || stack.contains(document.activeElement);
    if (nextPaused === paused) return;
    paused = nextPaused;
    const now = performance.now();
    notices.forEach(notice => {
      if (!notice.slot) return;
      if (paused) {
        clearTimeout(notice.timer);
        notice.remaining = Math.max(0, notice.remaining - (now - notice.started));
      } else armNotice(notice);
    });
  }

  function armNotice(notice) {
    clearTimeout(notice.timer);
    notice.started = performance.now();
    notice.timer = setTimeout(() => dismissNotice(notice), notice.remaining);
  }

  function animateNotice(notice, frames, finish) {
    notice.animation?.cancel();
    if (reducedMotion.matches || !notice.element.animate) {
      notice.animation = null;
      finish();
      return;
    }
    const animation = notice.element.animate(frames, { duration: toastConfig.duration, easing: toastEasing, fill: 'both' });
    notice.animation = animation;
    animation.finished.then(() => {
      if (notice.animation === animation) finish();
    }, () => {});
  }

  function removeNoticeSlot(notice) {
    const slot = notice.slot;
    slot.classList.remove('is-settled');
    slot.classList.add('is-collapsed');
    const remove = () => {
      clearTimeout(notice.cleanupTimer);
      slot.removeEventListener('transitionend', onEnd);
      slot.remove();
      notice.animation?.cancel();
      if (!stack.childElementCount) {
        region.hidden = true;
        hovered = false;
        updateNoticePause();
      }
    };
    const onEnd = event => {
      if (event.target === slot && event.propertyName === 'grid-template-rows') remove();
    };
    if (reducedMotion.matches) remove();
    else {
      slot.addEventListener('transitionend', onEnd);
      // 后台标签页或过渡被取消时也要释放节点。
      notice.cleanupTimer = setTimeout(remove, toastConfig.duration * 2);
    }
  }

  function showNotices() {
    // 淡出的卡片仍占用显示名额；目标列表始终只保留最新三条。
    let visible = notices.filter(notice => notice.slot).length + exitingNotices.size;
    for (const notice of notices) {
      if (notice.slot || visible >= toastConfig.maxVisible) continue;
      visible++;
      const slot = h('<div class="toast-slot is-collapsed"><div class="toast-clip"></div></div>');
      notice.slot = slot;
      slot.firstElementChild.append(notice.element);
      stack.append(slot);
      region.hidden = false;
      // 建立零高度起点，让浏览器按内容实际高度展开，不固定卡片高度。
      slot.getBoundingClientRect();
      slot.classList.remove('is-collapsed');
      animateNotice(notice, [
        { opacity: 0, transform: `translateY(${toastConfig.gap}px)` },
        { opacity: 1, transform: 'translateY(0)' },
      ], () => {
        slot.classList.add('is-settled');
        notice.animation?.cancel();
        notice.animation = null;
      });
      if (!paused) armNotice(notice);
      cancelAnimationFrame(announcementFrame);
      announcements.textContent = '';
      announcementFrame = requestAnimationFrame(() => {
        if (notices.includes(notice)) announcements.textContent = notice.message;
      });
    }
  }

  function dismissNotice(notice) {
    if (!notices.includes(notice)) return;
    const focused = notice.element.contains(document.activeElement);
    clearTimeout(notice.timer);
    notices = notices.filter(item => item !== notice);
    if (!notice.slot) return;
    exitingNotices.add(notice);
    if (focused) {
      const next = notices.find(item => item.slot);
      if (next) next.element.querySelector('button').focus({ preventScroll: true });
      else document.activeElement.blur();
    }
    notice.element.setAttribute('aria-hidden', 'true');
    notice.element.inert = true;
    notice.element.classList.add('out');
    const { opacity, transform } = getComputedStyle(notice.element);
    animateNotice(notice, [{ opacity, transform }, { opacity: 0, transform }], () => {
      exitingNotices.delete(notice);
      removeNoticeSlot(notice);
      showNotices();
    });
  }

  function toast(message, type) {
    if (!region) {
      region = h('<aside class="toast-region" aria-label="通知" hidden><div class="toast-stack"></div><div class="visually-hidden" role="status" aria-live="polite" aria-atomic="true"></div></aside>');
      for (const [name, value] of Object.entries({ width: `${toastConfig.width}px`, gap: `${toastConfig.gap}px`, duration: `${toastConfig.duration}ms` })) {
        region.style.setProperty(`--toast-${name}`, value);
      }
      stack = region.querySelector('.toast-stack');
      announcements = region.querySelector('[role="status"]');
      region.addEventListener('pointerenter', event => { if (event.pointerType !== 'touch') { hovered = true; updateNoticePause(); } });
      region.addEventListener('pointerleave', () => { hovered = false; updateNoticePause(); });
      region.addEventListener('focusin', updateNoticePause);
      region.addEventListener('focusout', () => requestAnimationFrame(updateNoticePause));
      document.body.append(region);
      toastEasing = getComputedStyle(region).getPropertyValue('--ease-out').trim();
    }
    const duplicate = notices.find(item => item.message === message && item.type === type);
    if (duplicate) {
      clearTimeout(duplicate.timer);
      duplicate.remaining = type === 'err' ? toastConfig.errorLifetime : toastConfig.lifetime;
      if (duplicate.slot && !paused) armNotice(duplicate);
      return;
    }
    if (notices.length >= toastConfig.maxVisible) dismissNotice(notices[0]);
    const element = h(`<div class="toast" data-tone="${type === 'err' ? 'error' : type === 'ok' ? 'success' : 'info'}"><span class="toast-symbol">${icon(type === 'ok' ? 'check' : type === 'err' ? 'zap' : 'info', 16)}</span><span class="toast-message">${esc(message)}</span><button type="button" class="toast-close" aria-label="关闭通知">×</button></div>`);
    const notice = { element, message, type, remaining: type === 'err' ? toastConfig.errorLifetime : toastConfig.lifetime, timer: 0, slot: null, animation: null };
    element.querySelector('button').addEventListener('click', () => dismissNotice(notice));
    notices.push(notice);
    showNotices();
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
