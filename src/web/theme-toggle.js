'use strict';

// Native toolbar adaptation of the pixel day/night interaction:
// https://leibox.pantoria.cn/docs/animations/pixel-toggle (design: Zsolt Kacso).
function createPixelThemeToggle(button, { checked, onChange }) {
  const canvas = button.querySelector('canvas');
  const context = canvas.getContext('2d');
  const track = button.querySelector('.theme-toggle-track');
  const thumb = button.querySelector('.theme-toggle-thumb');
  const motion = matchMedia('(prefers-reduced-motion: reduce)');
  const clamp = value => Math.max(0, Math.min(1, value));
  const shapes = [[], []];
  for (let y = -7; y <= 7; y++) {
    for (let x = -7; x <= 7; x++) {
      if (Math.hypot(x, y) < 6.5 && Math.hypot(x - 2.8, y + 2.2) > 5.3) shapes[0].push([x, y]);
      if (Math.hypot(x, y) < 3.5) shapes[1].push([x, y]);
    }
  }
  shapes[0].push([5, -5], [5, -6], [5, -4], [4, -5], [6, -5]);
  for (let ray = 0; ray < 8; ray++) {
    for (const radius of [5.5, 6.8]) shapes[1].push([
      Math.round(Math.cos(ray * Math.PI / 4) * radius),
      Math.round(Math.sin(ray * Math.PI / 4) * radius),
    ]);
  }
  let position = Number(checked), velocity = 0, scatter = 0;
  let angle = checked ? 180 : 0, targetAngle = angle;
  let frame = 0, lastTime = 0, drag = null, suppressClick = false;
  const particles = Array.from({ length: Math.max(...shapes.map(shape => shape.length)) }, (_, index) => {
    const point = shapes[Number(checked)][index];
    const phase = index * 2.39996, radius = 4 + Math.sqrt(index) * .9;
    return { x: point?.[0] || 0, y: point?.[1] || 0, opacity: point ? 1 : 0, vx: 0, vy: 0,
      scatterX: Math.cos(phase) * radius, scatterY: Math.sin(phase) * radius };
  });
  button.classList.toggle('has-pixels', !!context);

  function paint() {
    const travel = (track.clientWidth || 40) - (thumb.offsetWidth || 16) - 4;
    button.style.setProperty('--theme-position', `${position * travel}px`);
    button.style.setProperty('--theme-orbit', `${angle}deg`);
    if (!context) return;
    const ratio = Math.min(devicePixelRatio || 1, 3);
    const size = Math.round(24 * ratio);
    if (canvas.width !== size || canvas.height !== size) canvas.width = canvas.height = size;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, 24, 24);
    const light = clamp(position);
    context.fillStyle = `rgb(${Math.round(240 - light * 181)}, ${Math.round(234 - light * 183)}, ${Math.round(222 - light * 181)})`;
    for (const particle of particles) {
      context.globalAlpha = particle.opacity;
      context.fillRect(12 + particle.x * 1.15 - .5, 12 + particle.y * 1.15 - .5, 1, 1);
    }
    context.globalAlpha = 1;
  }

  function settle() {
    cancelAnimationFrame(frame);
    frame = lastTime = 0;
    position = Number(checked);
    velocity = scatter = 0;
    angle = targetAngle = checked ? 180 : 0;
    particles.forEach((particle, index) => {
      const point = shapes[Number(checked)][index];
      Object.assign(particle, { x: point?.[0] || 0, y: point?.[1] || 0, opacity: point ? 1 : 0, vx: 0, vy: 0 });
    });
    paint();
  }

  function animate(time) {
    frame = 0;
    const dt = Math.min((time - lastTime) / 1000 || 1 / 60, .032);
    lastTime = time;
    let moving = !!drag?.active;
    if (!drag?.active) {
      for (let step = 0; step < 3; step++) {
        velocity += ((Number(checked) - position) * 360 - velocity * 30) * dt / 3;
        position += velocity * dt / 3;
      }
      moving ||= Math.abs(position - Number(checked)) > .001 || Math.abs(velocity) > .01;
    }
    scatter = Math.max(0, scatter - dt * 3.5);
    angle += (targetAngle - angle) * (1 - Math.exp(-dt * 10));
    moving ||= scatter > 0 || Math.abs(targetAngle - angle) > .1;
    const shape = shapes[drag?.active ? Number(position >= .5) : Number(checked)];
    particles.forEach((particle, index) => {
      const point = shape[index];
      const spread = Math.max(scatter, drag?.active ? Math.sin(clamp(position) * Math.PI) * .8 : 0);
      const x = (point?.[0] || 0) * (1 - spread) + particle.scatterX * spread;
      const y = (point?.[1] || 0) * (1 - spread) + particle.scatterY * spread;
      const opacity = point ? 1 - spread * .2 : spread * .6;
      for (let step = 0; step < 3; step++) {
        particle.vx += ((x - particle.x) * 420 - particle.vx * 32) * dt / 3;
        particle.vy += ((y - particle.y) * 420 - particle.vy * 32) * dt / 3;
        particle.x += particle.vx * dt / 3;
        particle.y += particle.vy * dt / 3;
      }
      particle.opacity += (opacity - particle.opacity) * Math.min(1, dt * 18);
      moving ||= Math.abs(x - particle.x) + Math.abs(y - particle.y) > .01 || Math.abs(opacity - particle.opacity) > .01;
    });
    paint();
    if (moving) schedule();
    else settle();
  }

  function schedule() {
    if (motion.matches || document.hidden) { settle(); return; }
    if (!frame) frame = requestAnimationFrame(animate);
  }

  function setChecked(value) {
    const next = !!value, changed = checked !== next;
    checked = next;
    button.setAttribute('aria-checked', String(checked));
    if (!changed && !drag && position === Number(checked)) return;
    if (changed) { scatter = .95; targetAngle = (Math.floor(angle / 360) + 1) * 360 + (checked ? 180 : 0); }
    schedule();
  }

  function commit(value) {
    const changed = checked !== value;
    setChecked(value);
    if (changed) onChange(value);
  }

  button.addEventListener('click', event => {
    if (event.detail && suppressClick) { suppressClick = false; return; }
    commit(!checked);
  });
  button.addEventListener('keydown', event => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    event.preventDefault();
    commit(event.key === 'ArrowRight');
  });
  button.addEventListener('pointerdown', event => {
    if (!event.isPrimary || event.button !== 0 || drag) return;
    suppressClick = false;
    drag = { id: event.pointerId, x: event.clientX, position, active: false };
    button.setPointerCapture(event.pointerId);
  });
  button.addEventListener('pointermove', event => {
    if (!drag || event.pointerId !== drag.id) return;
    const delta = event.clientX - drag.x;
    if (!drag.active && Math.abs(delta) < 4) return;
    drag.active = true;
    button.dataset.dragging = '';
    if (event.cancelable) event.preventDefault();
    const travel = (track.clientWidth || 40) - (thumb.offsetWidth || 16) - 4;
    position = clamp(drag.position + delta / travel);
    velocity = 0;
    if (motion.matches) {
      const shape = shapes[Number(position >= .5)];
      particles.forEach((particle, index) => Object.assign(particle, {
        x: shape[index]?.[0] || 0, y: shape[index]?.[1] || 0, opacity: shape[index] ? 1 : 0,
      }));
      paint();
    } else schedule();
  });
  function endDrag(event) {
    if (!drag || event.pointerId !== drag.id) return;
    const active = drag.active;
    drag = null;
    delete button.dataset.dragging;
    if (button.hasPointerCapture(event.pointerId)) button.releasePointerCapture(event.pointerId);
    if (!active) return;
    suppressClick = true;
    commit(event.type === 'pointerup' ? position >= .5 : checked);
  }
  for (const type of ['pointerup', 'pointercancel', 'lostpointercapture']) button.addEventListener(type, endDrag);
  function cancelDrag() {
    if (drag) {
      const id = drag.id;
      suppressClick = drag.active;
      drag = null;
      delete button.dataset.dragging;
      if (button.hasPointerCapture(id)) button.releasePointerCapture(id);
    }
    settle();
  }
  document.addEventListener('visibilitychange', () => { if (document.hidden) cancelDrag(); });
  motion.addEventListener('change', cancelDrag);
  window.addEventListener('resize', paint);
  button.setAttribute('aria-checked', String(checked));
  settle();
  return { setChecked };
}
