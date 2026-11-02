'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn, execFileSync } = require('node:child_process');
const { once } = require('node:events');

async function main() {
  const binary = path.resolve(process.argv[2]);
  const version = process.argv[3] || 'ci';
  assert.equal(execFileSync(binary, ['--version'], { encoding: 'utf8' }).trim(), `zcode2api ${version}`);
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'zcode2api-smoke-'));
  const data = path.join(root, 'data with spaces');
  const child = spawn(binary, ['--root', root], {
    windowsHide: true,
    env: { ...process.env, ZCODE2API_DATA_DIR: data, ZCODE2API_WEB_PORT: '0', ZCODE2API_GATEWAY_PORT: '0', ZCODE2API_HOST: '127.0.0.1', ZCODE2API_ADMIN_PASSWORD: 'offline-smoke-password' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let logs = '';
  child.stdout.on('data', chunk => { logs += chunk; });
  child.stderr.on('data', chunk => { logs += chunk; });
  child.on('error', error => { logs += String(error); });
  const exit = once(child, 'exit');
  const json = async (url, options) => {
    const response = await fetch(url, { ...options, signal: AbortSignal.timeout(5000) });
    assert.equal(response.status, 200, await response.clone().text());
    return response.json();
  };
  try {
    let ports;
    for (let attempt = 0; attempt < 150; attempt++) {
      assert.equal(child.exitCode, null, logs);
      try { ports = JSON.parse(await fs.readFile(path.join(data, 'ports.json'), 'utf8')); break; } catch { }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    assert.ok(ports?.web_port && ports?.gateway_port, logs);
    const web = `http://127.0.0.1:${ports.web_port}`;
    const gateway = `http://127.0.0.1:${ports.gateway_port}`;
    assert.equal((await json(web + '/healthz')).ok, true);
    assert.equal((await json(gateway + '/healthz')).ok, true);
    for (const asset of ['/', '/app.js', '/style.css', '/ui-state.js', '/claims-view.js', '/logs-view.js', '/favicon.svg']) {
      const response = await fetch(web + asset);
      assert.equal(response.status, 200, asset);
      assert.ok((await response.text()).length > 20, asset);
    }
    assert.equal((await fetch(web + '/api/settings')).status, 401);
    assert.equal((await fetch(gateway + '/v1/models')).status, 401);
    const login = await json(web + '/api/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: 'offline-smoke-password' }) });
    assert.ok(login.token);
    const headers = { Authorization: `Bearer ${login.token}`, 'Content-Type': 'application/json' };
    const settings = await json(web + '/api/settings', { headers });
    assert.equal(settings.outbound_proxy.enabled, false);
    assert.equal((await json(web + '/api/settings', { method: 'PUT', headers, body: JSON.stringify({ poll_interval_sec: 73, http: { captcha: { mode: 'auto' } } }) })).ok, true);
    assert.equal((await json(web + '/api/settings', { headers })).poll_interval_sec, 73);
    const mihomo = await json(web + '/api/settings/proxy/mihomo', { headers });
    const goOS = { win32: 'windows', darwin: 'darwin', linux: 'linux' }[process.platform];
    assert.equal(mihomo.platform, `${goOS}/${process.arch === 'x64' ? 'amd64' : process.arch}`);
    execFileSync(binary, ['--healthcheck', `${gateway}/healthz`]);
    console.log(`Offline startup, embedded assets, authentication, settings, platform detection and healthcheck passed (${process.platform}/${process.arch})`);
  } finally {
    child.kill();
    let timer;
    try { await Promise.race([exit, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Service did not stop')), 10000); })]); }
    finally {
      clearTimeout(timer);
      child.kill('SIGKILL');
      assert.equal(path.dirname(root), path.resolve(os.tmpdir()));
      assert.match(path.basename(root), /^zcode2api-smoke-/);
      await fs.rm(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    }
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
