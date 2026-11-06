'use strict';

const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');

async function request(url, status, options = {}) {
  const response = await fetch(url, { ...options, signal: AbortSignal.timeout(2000) });
  assert.equal(response.status, status, url);
  return response;
}

async function main() {
  const binary = path.resolve(process.argv[2] || 'bin/zcode2api');
  const version = process.argv[3] || 'ci';
  assert.equal(execFileSync(binary, ['--version'], { encoding: 'utf8', timeout: 5000 }).trim(), `zcode2api ${version}`);

  // Use a fresh database and random loopback ports; no upstream accounts are added.
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'zcode2api-smoke-'));
  const data = path.join(root, 'data');
  await fs.writeFile(path.join(root, 'config.json'), JSON.stringify({ daily_claim_enabled: false }));
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('ZCODE2API_')));
  Object.assign(env, {
    ZCODE2API_DATA_DIR: data,
    ZCODE2API_HOST: '127.0.0.1',
    ZCODE2API_WEB_PORT: '0',
    ZCODE2API_GATEWAY_PORT: '0',
  });
  const child = spawn(binary, ['--root', root], { cwd: root, env, timeout: 60000, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '', spawnError, exited = false;
  for (const stream of [child.stdout, child.stderr]) {
    stream.on('data', chunk => { output = (output + chunk).slice(-16000); });
  }
  child.on('error', error => { spawnError = error; });
  const closed = new Promise(resolve => child.once('close', code => { exited = true; resolve(code); }));
  let passed = false;

  try {
    let ports, ready = false, lastError;
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      if (spawnError) throw spawnError;
      assert.equal(exited, false, 'Service exited before becoming healthy');
      try {
        ports = JSON.parse(await fs.readFile(path.join(data, 'ports.json'), 'utf8'));
        const health = await (await request(`http://127.0.0.1:${ports.gateway_port}/healthz`, 200)).json();
        assert.equal(health.ok, true);
        assert.equal(health.scope, 'gateway');
        ready = true;
        break;
      } catch (error) {
        lastError = error;
        await delay(100);
      }
    }
    assert.ok(ready, `Service did not become healthy: ${lastError}`);
    const web = `http://127.0.0.1:${ports.web_port}`;
    const gateway = `http://127.0.0.1:${ports.gateway_port}`;
    assert.equal((await (await request(`${web}/healthz`, 200)).json()).ok, true);
    assert.equal((await (await request(`${gateway}/readyz`, 503)).json()).active_accounts, 0);

    const page = await request(`${web}/`, 200);
    assert.match(page.headers.get('content-type'), /text\/html/);
    const html = await page.text();
    assert.match(html, /id="login-form"/);
    const assets = [...html.matchAll(/(?:src|href)="(\/[^"\s]+)"/g)].map(match => match[1]);
    assert.ok(assets.includes('/app.js'), 'Console must load its entry script');
    for (const asset of new Set(assets)) {
      assert.ok((await (await request(`${web}${asset}`, 200)).text()).length > 0, asset);
    }

    await request(`${web}/api/accounts`, 401);
    await request(`${gateway}/v1/models`, 401);
    const password = (await fs.readFile(path.join(data, 'admin-password.txt'), 'utf8')).trim();
    const login = await (await request(`${web}/api/auth/login`, 200, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }),
    })).json();
    assert.ok(login.token, 'Admin login must return a session token');
    const key = await (await request(`${web}/api/keys`, 200, {
      method: 'POST', headers: { Authorization: `Bearer ${login.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'smoke' }),
    })).json();
    assert.ok(key.secret, 'Creating a key must return its secret');
    const models = await (await request(`${gateway}/v1/models`, 200, {
      headers: { Authorization: `Bearer ${key.secret}` },
    })).json();
    assert.equal(models.object, 'list');
    assert.deepEqual(models.data, []);
    passed = true;
  } catch (error) {
    console.error(output);
    throw error;
  } finally {
    if (!exited) child.kill('SIGTERM');
    const force = setTimeout(() => child.kill('SIGKILL'), 5000);
    force.unref();
    const code = await closed;
    clearTimeout(force);
    await fs.rm(root, { recursive: true, force: true });
    if (passed) assert.equal(code, 0, 'Service must shut down cleanly');
  }
  console.log('Smoke passed: startup, health, console assets, admin login and gateway keys.');
}

main().catch(error => { console.error(error); process.exitCode = 1; });
