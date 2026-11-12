'use strict';

const fs = require('node:fs');
const path = require('node:path');

const SDK_URL = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js';

// Synthetic rendering interfaces for jsdom; no host GPU is queried.
function installCanvasWebGL(window) {
  const noop = () => {};
  const png = window.Uint8Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64'));
  const contexts = new WeakMap();
  const context2D = canvas => {
    const image = (width = 1, height = 1) => ({ data: new window.Uint8ClampedArray(width * height * 4), width, height });
    const gradient = () => ({ addColorStop: noop });
    const context = {
      canvas, createImageData: image, getImageData: (x, y, width, height) => image(width, height),
      measureText: text => ({ width: String(text).length * 8 }),
      createLinearGradient: gradient, createRadialGradient: gradient, createPattern: () => ({}),
      isPointInPath: () => false, font: '10px sans-serif', textBaseline: 'alphabetic',
      textAlign: 'start', fillStyle: '#000', strokeStyle: '#000', globalAlpha: 1, lineWidth: 1,
    };
    for (const name of [
      'arc', 'beginPath', 'bezierCurveTo', 'clearRect', 'clip', 'closePath', 'drawImage',
      'ellipse', 'fill', 'fillRect', 'fillText', 'lineTo', 'moveTo', 'putImageData',
      'quadraticCurveTo', 'rect', 'resetTransform', 'restore', 'rotate', 'save', 'scale',
      'setTransform', 'stroke', 'strokeText', 'transform', 'translate',
    ]) context[name] = noop;
    return context;
  };
  const contextGL = canvas => ({
    canvas, VENDOR: 0x1f00, RENDERER: 0x1f01, VERSION: 0x1f02,
    SHADING_LANGUAGE_VERSION: 0x8b8c, ARRAY_BUFFER: 0x8892, STATIC_DRAW: 0x88e4,
    FLOAT: 0x1406, TRIANGLES: 4, TRIANGLE_STRIP: 5, VERTEX_SHADER: 0x8b31,
    FRAGMENT_SHADER: 0x8b30, COMPILE_STATUS: 0x8b81, LINK_STATUS: 0x8b82,
    COLOR_BUFFER_BIT: 0x4000, RGBA: 0x1908, UNSIGNED_BYTE: 0x1401,
    getParameter: parameter => ({
      7936: 'WebKit', 7937: 'WebKit WebGL', 7938: 'WebGL 1.0',
      35724: 'WebGL GLSL ES 1.0', 37445: 'Intel', 37446: 'Intel Graphics',
      3379: 4096, 34076: 4096, 34921: 16,
    })[parameter] ?? 0,
    getExtension: name => name === 'WEBGL_debug_renderer_info'
      ? { UNMASKED_VENDOR_WEBGL: 37445, UNMASKED_RENDERER_WEBGL: 37446 } : null,
    getSupportedExtensions: () => ['WEBGL_debug_renderer_info'],
    getContextAttributes: () => ({ alpha: true, antialias: true, depth: true, stencil: false }),
    getShaderPrecisionFormat: () => ({ precision: 23, rangeMin: 127, rangeMax: 127 }),
    createBuffer: () => ({}), createShader: () => ({}), createProgram: () => ({}),
    shaderSource: noop, compileShader: noop, attachShader: noop, linkProgram: noop,
    useProgram: noop, bindBuffer: noop, bufferData: noop, enableVertexAttribArray: noop,
    vertexAttribPointer: noop, drawArrays: noop, viewport: noop, clearColor: noop, clear: noop,
    uniform2f: noop, uniform4f: noop, deleteBuffer: noop, deleteShader: noop, deleteProgram: noop,
    getShaderParameter: () => true, getProgramParameter: () => true,
    getShaderInfoLog: () => '', getProgramInfoLog: () => '', getAttribLocation: () => 0,
    getUniformLocation: () => ({}), getError: () => 0,
    readPixels: (x, y, width, height, format, type, pixels) => pixels?.fill(0),
    isContextLost: () => false,
  });
  const getContext = function (kind) {
    kind = String(kind).toLowerCase();
    if (!['2d', 'webgl', 'experimental-webgl', 'webgl2'].includes(kind)) return null;
    let saved = contexts.get(this);
    if (!saved) { saved = new Map(); contexts.set(this, saved); }
    const key = kind === '2d' ? '2d' : 'webgl';
    if (!saved.has(key)) saved.set(key, key === '2d' ? context2D(this) : contextGL(this));
    return saved.get(key);
  };
  window.HTMLCanvasElement.prototype.getContext = getContext;
  window.HTMLCanvasElement.prototype.toDataURL = () => 'data:image/png;base64,' + Buffer.from(png).toString('base64');
  window.HTMLCanvasElement.prototype.toBlob = callback => window.setTimeout(() => callback(new window.Blob([png], { type: 'image/png' })), 0);
  window.OffscreenCanvas ??= class {
    constructor(width, height) { this.width = width; this.height = height; }
    getContext(kind) { return getContext.call(this, kind); }
    convertToBlob() { return Promise.resolve(new window.Blob([png], { type: 'image/png' })); }
  };
  window.matchMedia ??= media => Object.assign(new window.EventTarget(), {
    matches: false, media: String(media), onchange: null, addListener: noop, removeListener: noop,
  });
}

// This function is evaluated inside the jsdom realm.
async function verifyInWindow(config, outcome) {
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
  const progress = window.__captchaProgress = { stage: 'sdk_load', sdk_initialized: false, sdk_executed: false };
  const loadingAt = performance.now();
  document.body.replaceChildren();
  const container = document.createElement('div'); container.id = 'z2a-captcha'; document.body.appendChild(container);
  const button = document.createElement('button'); button.id = 'z2a-captcha-button'; document.body.appendChild(button);
  window.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix };
  if (typeof window.initAliyunCaptcha !== 'function') {
    await new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = config.sdkURL;
      script.onload = () => typeof window.initAliyunCaptcha === 'function' ? resolve() : reject(new Error('SDK unavailable'));
      script.onerror = () => reject(new Error('SDK load failed'));
      document.head.appendChild(script);
    });
  }
  progress.sdk_load_ms = Math.round(performance.now() - loadingAt);
  return new Promise(resolve => {
    let settled = false;
    let instance, verificationStartedAt;
    const finish = value => {
      if (!settled) {
        settled = true;
        if (verificationStartedAt) progress.sdk_verify_ms = Date.now() - verificationStartedAt;
        resolve(value.ok ? { ...value, issued_at_ms: verificationStartedAt || Date.now() } : value);
      }
    };
    const failed = error => {
      const code = String(error?.code || '');
      finish({ ok: false, code: 'captcha_sdk_error', sdk_error_code: /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/.test(code) ? code : '', message: '验证码 SDK 初始化失败' });
    };
    window.addEventListener('error', failed);
    window.addEventListener('unhandledrejection', failed);
    window.__disposeCaptcha = () => {
      settled = true;
      window.removeEventListener('error', failed);
      window.removeEventListener('unhandledrejection', failed);
      try { instance?.destroy?.(); } catch {}
    };
    progress.stage = 'sdk_init';
    const initializedAt = performance.now();
    const initialization = window.initAliyunCaptcha({
      SceneId: config.sceneId, mode: 'popup', element: '#z2a-captcha', button: '#z2a-captcha-button',
      language: 'cn', delayBeforeSuccess: false,
      success: param => finish(typeof param === 'string' && param.trim()
        ? { ok: true, param, region: config.region }
        : { ok: false, code: 'captcha_empty_result', message: '验证结果为空' }),
      fail: value => finish(outcome(value, config.region)),
      onError: failed,
      getInstance: value => {
        if (settled) { try { value?.destroy?.(); } catch {} return; }
        if (!value) { failed(); return; }
        if (instance) { if (value !== instance) { try { value?.destroy?.(); } catch {} } return; }
        instance = value;
        progress.sdk_initialized = true;
        progress.sdk_init_ms = Math.round(performance.now() - initializedAt);
        if (typeof instance?.startTracelessVerification !== 'function') {
          finish({ ok: false, code: 'captcha_interaction_required', message: '当前验证场景需要人工交互' });
          return;
        }
        // getInstance confirms initialization, but device collection and async
        // resources can still be settling. Slow initialization must not erase
        // the SDK's preparation interval before verification.
        progress.stage = 'sdk_ready';
        const readyAt = performance.now();
        pause(config.delayMs).then(() => {
          if (settled) return;
          progress.sdk_ready_wait_ms = Math.round(performance.now() - readyAt);
          verificationStartedAt = Date.now();
          progress.stage = 'sdk_verify';
          progress.sdk_executed = true;
          return instance.startTracelessVerification();
        }).then(value => {
          if (settled || !value || typeof value !== 'object') return;
          // Accept an explicitly successful returned ticket too. Intermediate
          // return values must not preempt the official success/fail callbacks.
          const result = outcome(value, config.region);
          if (result.ok) finish(result);
        }).catch(failed);
      },
    });
    Promise.resolve(initialization).catch(failed);
  });
}

function captchaExpression(config, delayMs = 2100) {
  const parameters = JSON.stringify({ sceneId: config.scene_id, region: config.region, prefix: config.prefix, sdkURL: SDK_URL, delayMs });
  return `(${verifyInWindow.toString()})(${parameters}, ${captchaOutcome.toString()})`;
}

function loadJSDOM(config) {
  return require(path.join(path.resolve(config.module_path), 'node_modules', 'jsdom'));
}

function proxyConnectionError(error, address) {
  if (!address) return false;
  const proxy = new URL(address);
  const hostname = proxy.hostname.replace(/^\[|\]$/g, '');
  const port = Number(proxy.port || (proxy.protocol === 'https:' ? 443 : 80));
  const inspect = error => {
    if (!error || typeof error !== 'object') return false;
    if (['ENOTFOUND', 'EAI_AGAIN'].includes(error.code) && error.hostname === hostname) return true;
    if (error.syscall === 'connect' && ['ECONNREFUSED', 'ETIMEDOUT', 'EHOSTUNREACH', 'ENETUNREACH'].includes(error.code) && error.address === hostname && Number(error.port) === port) return true;
    return inspect(error.cause) || (Array.isArray(error.errors) && error.errors.some(inspect));
  };
  return inspect(error);
}

function captchaSessionState(dom) {
  if (!dom) return;
  try {
    const localStorage = [], storage = dom.window.localStorage;
    for (let index = 0; index < storage.length; index++) {
      const key = storage.key(index);
      localStorage.push([key, storage.getItem(key)]);
    }
    const cookieJar = dom.cookieJar.serializeSync();
    const state = { cookieJar, localStorage };
    // Leave room for the fresh ticket in the bounded process output. State
    // stays in the account's memory pool; it is never written to disk/logs.
    if (Buffer.byteLength(JSON.stringify(state), 'utf8') <= (16 << 10)) return state;
  } catch {}
}

async function run(config, dependencies = {}) {
  let dom, timer, interrupt, networkFailed = false, proxyFailed = false, resourceStatus = 0, resourceRetryAfter = '';
  const withProgress = result => ({
    stage: 'runtime', sdk_initialized: false, sdk_executed: false,
    ...dom?.window.__captchaProgress, ...result, state: captchaSessionState(dom),
  });
  try {
    if (new URL(config.origin).protocol !== 'https:') throw new Error('invalid origin');
  } catch { return withProgress({ ok: false, code: 'captcha_origin_invalid', message: '验证码页面必须使用 HTTPS' }); }
  if (config.proxy_url) {
    try {
      const proxy = new URL(config.proxy_url);
      if (!['http:', 'https:'].includes(proxy.protocol) || !proxy.hostname || (proxy.pathname && proxy.pathname !== '/') || proxy.search || proxy.hash || /[\r\n\x00]/.test(config.proxy_url)) throw new Error('unsupported proxy');
    } catch { return withProgress({ ok: false, code: 'captcha_proxy_unsupported', message: '验证需要有效的 HTTP/HTTPS 代理' }); }
  }
  let library;
  try { library = (dependencies.loadJSDOM || loadJSDOM)(config); }
  catch { return withProgress({ ok: false, code: 'captcha_jsdom_missing', message: '未找到 jsdom，请安装验证码运行时依赖' }); }
  const { JSDOM, ResourceLoader, VirtualConsole, CookieJar } = library;
  try {
    let cookieJar = new CookieJar();
    if (config.state?.cookieJar) {
      try { cookieJar = CookieJar.deserializeSync(config.state.cookieJar); } catch {}
    }
    const origin = new URL(config.origin).origin + '/';
    // Reuse the account's upstream cookies only for the upstream origin. The
    // jar enforces domains for subsequent SDK resource and XHR requests.
    for (const cookie of String(config.cookies || '').split(';')) {
      if (cookie.trim()) cookieJar.setCookieSync(cookie.trim() + '; Path=/; Secure', origin, { ignoreError: true });
    }
    const canceled = new Promise(resolve => {
      interrupt = () => resolve({ ok: false, code: 'captcha_canceled', message: '验证码请求已取消' });
      process.once('SIGINT', interrupt);
      process.once('SIGTERM', interrupt);
    });
    class VerificationResources extends ResourceLoader {
      fetch(url, options) {
        const request = super.fetch(url, options);
        request?.catch(error => {
          const status = request.response?.statusCode;
          if (status) {
            resourceStatus = status;
            resourceRetryAfter = String(request.response?.headers?.['retry-after'] || '');
          }
          else {
            networkFailed = true;
            proxyFailed ||= proxyConnectionError(error, config.proxy_url);
          }
        });
        return request;
      }
    }
    const console = new VirtualConsole();
    console.on('jsdomError', error => { if (error.type === 'XMLHttpRequest') networkFailed = true; });
    dom = new JSDOM('<!doctype html><html><head></head><body></body></html>', {
      url: origin, cookieJar, runScripts: 'dangerously', pretendToBeVisual: true,
      resources: new VerificationResources({ ...(config.proxy_url ? { proxy: config.proxy_url } : {}), strictSSL: true }),
      virtualConsole: console,
      beforeParse(window) {
        for (const entry of Array.isArray(config.state?.localStorage) ? config.state.localStorage : []) {
          if (Array.isArray(entry) && entry.length === 2 && entry.every(value => typeof value === 'string')) {
            try { window.localStorage.setItem(entry[0], entry[1]); } catch {}
          }
        }
        if (config.simulate_canvas_webgl !== false) installCanvasWebGL(window);
      },
    });
    const timeoutMs = dependencies.timeoutMs ?? Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
    const operation = dom.window.eval(captchaExpression(config, dependencies.delayMs ?? 2100));
    const result = await Promise.race([
      operation,
      canceled,
      new Promise(resolve => { timer = setTimeout(() => resolve({ ok: false, code: 'captcha_timeout', message: '验证码请求超时或需要人工交互' }), timeoutMs); }),
    ]);
    if (resourceStatus && !result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      return withProgress({ ok: false, code: resourceStatus === 407 ? 'proxy_auth_failed' : 'captcha_resource_http_error', status: resourceStatus, retry_after: resourceRetryAfter, message: '验证码 SDK 资源返回 HTTP 错误' });
    }
    if (proxyFailed && !result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      return withProgress({ ok: false, code: 'proxy_connection_failed', message: '验证码代理连接失败' });
    }
    if (networkFailed && !result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      return withProgress({ ok: false, code: 'captcha_network_error', message: '验证码 SDK 资源连接失败' });
    }
    return withProgress(result);
  } catch {
    if (resourceStatus) return withProgress({ ok: false, code: resourceStatus === 407 ? 'proxy_auth_failed' : 'captcha_resource_http_error', status: resourceStatus, retry_after: resourceRetryAfter, message: '验证码 SDK 资源返回 HTTP 错误' });
    if (proxyFailed) return withProgress({ ok: false, code: 'proxy_connection_failed', message: '验证码代理连接失败' });
    return withProgress({ ok: false, code: networkFailed ? 'captcha_network_error' : 'captcha_sdk_error', message: '验证码 SDK 初始化或网络请求失败' });
  } finally {
    clearTimeout(timer);
    if (interrupt) {
      process.removeListener('SIGINT', interrupt);
      process.removeListener('SIGTERM', interrupt);
    }
    try { dom?.window.__disposeCaptcha?.(); } catch {}
    try { dom?.window.close(); } catch {}
  }
}

function captchaOutcome(value, region) {
  const result = value?.Result || value?.result || value || {};
  const rawCode = String(result.VerifyCode || result.verifyCode || value?.verifyCode || 'unknown');
  const code = /^[FT][0-9]{3}$/.test(rawCode) ? rawCode : 'unknown';
  const param = value?.captchaVerifyParam || value?.CaptchaVerifyParam || result.captchaVerifyParam || result.CaptchaVerifyParam;
  const accepted = (value?.success === true || result.success === true) && (result.VerifyResult === true || result.verifyResult === true);
  if ((accepted || code === 'T006') && typeof param === 'string' && param.trim()) return { ok: true, param, region };
  return { ok: false, code: code === 'F015' ? 'captcha_interaction_required' : 'captcha_verification_failed', verify_code: code, message: '阿里云验证未通过，需要按官方验证流程重新验证' };
}


if (require.main === module) {
  // SDK requests may leave Node handles alive even after window.close(). A
  // completed result must reach Go immediately, while the ticket is fresh.
  const output = (result, status = 0) => {
    process.stdout.write(JSON.stringify(result) + '\n', () => process.exit(status));
  };
  Promise.resolve().then(() => run(JSON.parse(fs.readFileSync(0, 'utf8')))).then(result => {
    output(result);
  }, () => {
    output({ ok: false, code: 'captcha_runtime_error', message: '验证码运行环境不可用' }, 1);
  });
}

module.exports = { captchaExpression, captchaOutcome, installCanvasWebGL, run };
