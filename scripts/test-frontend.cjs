// Invoked by TestFrontendIntegration: real frontend adapter + Next.js proxy +
// real Go API/worker/MongoDB. OpenStack and RDP readiness use testinfra.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const frontend = path.resolve(process.argv[2]);
const ts = require(path.join(frontend, 'node_modules/typescript'));
require.extensions['.ts'] = (module, filename) => {
 const output = ts.transpileModule(fs.readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
 module._compile(output.outputText, filename);
};
process.env.NEXT_PUBLIC_VDI_DEMO = 'false';
process.env.NEXT_PUBLIC_API_BASE_URL = '/backend';
const values = new Map();
global.sessionStorage = { getItem: k => values.get(k) ?? null, setItem: (k,v) => values.set(k,v), removeItem: k => values.delete(k) };
let redirected;
global.window = { location: { assign: value => { redirected = value; } } };
const origin = 'http://127.0.0.1:3107';
const realFetch = global.fetch;
global.fetch = (url, options) => realFetch(new URL(url, origin), options);
const { request, storeSession, getSession, ApiError, isDemo } = require(path.join(frontend, 'lib/vdi/api.ts'));
const delay = ms => new Promise(r => setTimeout(r, ms));
let logs = '';
const server = spawn(process.execPath, ['node_modules/next/dist/bin/next', 'start', '--port', '3107', '--hostname', '127.0.0.1'], { cwd: frontend, stdio: ['ignore','pipe','pipe'] });
server.stdout.on('data', d => { logs += d; });
server.stderr.on('data', d => { logs += d; });
async function waitFor(load, condition) {
 for (let i = 0; i < 150; i++) {
  const value = await load();
  if (condition(value)) return value;
  await delay(100);
 }
 throw new Error('Timed out waiting for state');
}
async function run() {
 await waitFor(async () => { try { return await realFetch(origin+'/login'); } catch { return null; } }, r => r?.ok);
 assert.equal(isDemo, false);
 await assert.rejects(request('/api/auth/login', 'POST', { email: 'admin@example.com', password: 'wrong' }), e => e instanceof ApiError && e.status === 401);
 const admin = await request('/api/auth/login', 'POST', { email: 'admin@example.com', password: 'secret' });
 storeSession(admin);
 assert.equal((await request('/api/me')).role, 'ADMIN');
 console.log('PASS real API login, session storage and /me');
 const user = await request('/api/admin/users', 'POST', { name: 'Frontend User', email: 'frontend-test@example.com', password: 'frontend-test-password', role: 'USER' });
 assert.ok((await request('/api/admin/users')).some(u => u.id === user.id));
 const session = await request('/api/auth/login', 'POST', { email: user.email, password: 'frontend-test-password' });
 storeSession(session);
 assert.equal((await request('/api/me')).id, user.id);
 await assert.rejects(request('/api/admin/summary'), e => e.status === 403);
 console.log('PASS administrator creates user, user login and access control');
 const images = await request('/api/images');
 assert.equal(images.length, 4);
 const os = images.find(im => im.type === 'UBUNTU');
 const spec = await request(`/api/images/${os.id}/spec`);
 assert.deepEqual(spec, { osId: os.id, cpuCores: 1, memoryGb: 1, storageGb: 10 });
 const body = { name: '프론트 연동 테스트', ...spec };
 const key = require('node:crypto').randomUUID();
 const desktop = await request('/api/desktops', 'POST', body, key);
 assert.equal(desktop.status, 'CREATING');
 const replay = await request('/api/desktops', 'POST', body, key);
 assert.equal(replay.id, desktop.id);
 assert.equal((await request('/api/desktops')).length, 1);
 console.log('PASS image/spec lookup, desktop creation and idempotency');
 const ready = await waitFor(() => request(`/api/desktops/${desktop.id}`), d => d.canConnect && d.connectionState === 'READY');
 assert.equal(ready.status, 'RUNNING');
 const connection = await request(`/api/desktops/${desktop.id}/connect`, 'POST');
 assert.ok(new URL(connection.url).searchParams.get('data'));
 assert.ok(Date.parse(connection.expiresAt) > Date.now());
 console.log('PASS Go worker completes provisioning, connection capability issued');
 const deletion = await request(`/api/desktops/${desktop.id}`, 'DELETE');
 assert.equal(deletion.status, 'DELETING');
 await waitFor(() => request('/api/desktops'), items => items.length === 0);
 const expiredToken = session.token;
 await request('/api/auth/logout', 'POST');
 storeSession(null);
 assert.equal(getSession(), null);
 const check = await realFetch(origin+'/backend/api/me', { headers: { Authorization: 'Bearer '+expiredToken } });
 assert.equal(check.status, 401);
 console.log('PASS deletion completes and logout revokes the server session');
 storeSession(admin);
 const assigned = await request('/api/admin/desktops', 'POST', { ...body, name: '관리자 할당 테스트', userId: user.id }, require('node:crypto').randomUUID());
 const filtered = await request(`/api/admin/desktops?userId=${user.id}`);
 assert.equal(filtered.length, 1);
 assert.equal(filtered[0].id, assigned.id);
 const events = await request(`/api/admin/events?limit=100&desktopId=${assigned.id}`);
 assert.ok(events.some(event => event.action === 'DESKTOP_ASSIGN'));
 assert.equal((await request('/api/admin/summary')).users, 2);
 await request(`/api/admin/desktops/${assigned.id}`, 'DELETE');
 await waitFor(() => request(`/api/admin/desktops?userId=${user.id}`), items => items.length === 0);
 await request(`/api/admin/users/${user.id}`, 'DELETE');
 console.log('PASS admin assignment, query filters, audit events, summary and release');
 storeSession({ ...admin, token: 'revoked-token' });
 await assert.rejects(request('/api/me'), e => e.status === 401);
 assert.equal(getSession(), null);
 assert.equal(redirected, '/login');
 console.log('PASS unauthorized response clears the frontend session and redirects');
}
run().catch(e => { console.error(e); console.error(logs); process.exitCode = 1; }).finally(() => { server.kill('SIGTERM'); });
