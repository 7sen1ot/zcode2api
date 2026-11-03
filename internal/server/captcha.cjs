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
  window.Worker ??= class extends window.EventTarget {
    postMessage() {} terminate() {}
  };
  window.matchMedia ??= media => Object.assign(new window.EventTarget(), {
    matches: false, media: String(media), onchange: null, addListener: noop, removeListener: noop,
  });
}

// This function is evaluated inside the jsdom realm.
async function verifyInWindow(config, outcome) {
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
  document.body.replaceChildren();
  const container = document.createElement('div'); container.id = 'z2a-captcha'; document.body.appendChild(container);
  const button = document.createElement('button'); button.id = 'z2a-captcha-button'; document.body.appendChild(button);
  window.AliyunCaptchaConfig = { region: config.region, prefix: config.prefix };
  if (typeof window.initAliyunCaptcha !== 'function') {
    await new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = config.sdkURL;
      script.onload = resolve; script.onerror = () => reject(new Error('SDK load failed'));
      document.head.appendChild(script);
    });
  }
  await pause(config.delayMs);
  return new Promise(resolve => {
    let settled = false;
    const finish = value => { if (!settled) { settled = true; resolve(value); } };
    window.initAliyunCaptcha({
      SceneId: config.sceneId, mode: 'popup', element: '#z2a-captcha', button: '#z2a-captcha-button',
      success: param => finish(typeof param === 'string' && param.trim()
        ? { ok: true, param, region: config.region }
        : { ok: false, code: 'captcha_empty_result', message: '验证结果为空' }),
      fail: value => finish(outcome(value, config.region)),
      onError: () => finish({ ok: false, code: 'captcha_sdk_error', message: '验证码 SDK 初始化或网络请求失败' }),
      getInstance: async instance => {
        await pause(config.delayMs);
        try {
          instance.startTracelessVerification();
        } catch {
          finish({ ok: false, code: 'captcha_sdk_error', message: '验证码 SDK 启动失败' });
        }
      },
    });
  });
}

function captchaExpression(config, delayMs = 2100) {
  const parameters = JSON.stringify({ sceneId: config.scene_id, region: config.region, prefix: config.prefix, sdkURL: SDK_URL, delayMs });
  return `(${verifyInWindow.toString()})(${parameters}, ${captchaOutcome.toString()})`;
}

function loadJSDOM(config) {
  return require(path.join(path.resolve(config.module_path), 'node_modules', 'jsdom'));
}

async function run(config, dependencies = {}) {
  try {
    if (new URL(config.origin).protocol !== 'https:') throw new Error('invalid origin');
  } catch { return { ok: false, code: 'captcha_origin_invalid', message: '验证码页面必须使用 HTTPS' }; }
  if (config.proxy_url) {
    try {
      const proxy = new URL(config.proxy_url);
      if (proxy.username || proxy.password || !['http:', 'https:'].includes(proxy.protocol)) throw new Error('unsupported proxy');
    } catch { return { ok: false, code: 'captcha_proxy_unsupported', message: '验证仅支持无认证的 HTTP/HTTPS 代理' }; }
  }
  let library;
  try { library = (dependencies.loadJSDOM || loadJSDOM)(config); }
  catch { return { ok: false, code: 'captcha_jsdom_missing', message: '未找到 jsdom，请安装验证码运行时依赖' }; }
  const { JSDOM, ResourceLoader, VirtualConsole } = library;
  let dom, timer, interrupt, networkFailed = false;
  try {
    const canceled = new Promise(resolve => {
      interrupt = () => resolve({ ok: false, code: 'captcha_canceled', message: '验证码请求已取消' });
      process.once('SIGINT', interrupt);
      process.once('SIGTERM', interrupt);
    });
    class VerificationResources extends ResourceLoader {
      fetch(url, options) {
        const request = super.fetch(url, options);
        request?.catch(() => { networkFailed = true; });
        return request;
      }
    }
    const console = new VirtualConsole();
    console.on('jsdomError', error => { if (error.type === 'XMLHttpRequest') networkFailed = true; });
    dom = new JSDOM('<!doctype html><html><head></head><body></body></html>', {
      url: new URL(config.origin).origin + '/', runScripts: 'dangerously', pretendToBeVisual: true,
      resources: new VerificationResources({ ...(config.proxy_url ? { proxy: config.proxy_url } : {}), strictSSL: true }),
      virtualConsole: console,
      beforeParse(window) { if (config.simulate_canvas_webgl !== false) installCanvasWebGL(window); },
    });
    const timeoutMs = dependencies.timeoutMs ?? Math.max(5000, Math.min(120000, Number(config.timeout_ms) || 30000));
    const operation = dom.window.eval(captchaExpression(config, dependencies.delayMs ?? 2100));
    const result = await Promise.race([
      operation,
      canceled,
      new Promise(resolve => { timer = setTimeout(() => resolve({ ok: false, code: 'captcha_timeout', message: '验证码请求超时或需要人工交互' }), timeoutMs); }),
    ]);
    if (networkFailed && !result.ok && ['captcha_sdk_error', 'captcha_timeout'].includes(result.code)) {
      return { ok: false, code: 'captcha_network_error', message: '验证码 SDK 资源连接失败' };
    }
    return result;
  } catch {
    return { ok: false, code: networkFailed ? 'captcha_network_error' : 'captcha_sdk_error', message: '验证码 SDK 初始化或网络请求失败' };
  } finally {
    clearTimeout(timer);
    if (interrupt) {
      process.removeListener('SIGINT', interrupt);
      process.removeListener('SIGTERM', interrupt);
    }
    dom?.window.close();
  }
}

function captchaOutcome(value, region) {
  const result = value?.Result || value?.result || value || {};
  const rawCode = String(result.VerifyCode || result.verifyCode || 'unknown');
  const code = /^[FT][0-9]{3}$/.test(rawCode) ? rawCode : 'unknown';
  const param = value?.captchaVerifyParam || value?.CaptchaVerifyParam || result.captchaVerifyParam || result.CaptchaVerifyParam;
  const accepted = (value?.success === true || result.success === true) && (result.VerifyResult === true || result.verifyResult === true);
  if ((accepted || code === 'T006') && typeof param === 'string' && param.trim()) return { ok: true, param, region };
  return { ok: false, code: code === 'F015' ? 'captcha_interaction_required' : 'captcha_verification_failed', verify_code: code, message: '阿里云验证未通过，需要按官方验证流程重新验证' };
}


if (require.main === module) {
  Promise.resolve().then(() => run(JSON.parse(fs.readFileSync(0, 'utf8')))).then(result => {
    process.stdout.write(JSON.stringify(result) + '\n');
  }, () => {
    process.stdout.write(JSON.stringify({ ok: false, code: 'captcha_runtime_error', message: '验证码运行环境不可用' }) + '\n');
    process.exitCode = 1;
  });
}

module.exports = { captchaOutcome, installCanvasWebGL, run };
