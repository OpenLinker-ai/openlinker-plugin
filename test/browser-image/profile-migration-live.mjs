import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createServer } from 'node:http';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import { lstat, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
const run = promisify(execFile);
const require = createRequire('/opt/openlinker/browser-engine/package.json');
const { chromium } = require('playwright-core');
async function sourceFingerprint() {
  const hash = createHash('sha256');
  async function visit(path) {
    const info = await lstat(path, { bigint: true });
    hash.update(`${path}:${info.mode}:${info.size}:${info.mtimeNs}\n`);
    if (info.isDirectory()) for (const name of (await readdir(path)).sort()) await visit(`${path}/${name}`);
    else { assert(info.isFile()); hash.update(await readFile(path)); }
  }
  await visit('/scratch/source'); return hash.digest('hex');
}
for (const directory of ['home', 'tmp', 'seed-profile', 'empty-profile']) await mkdir(`/scratch/${directory}`, { mode: 0o700 });
await writeFile('/scratch/synthetic-marker', 'fresh-browser-fixture-only', { mode: 0o600, flag: 'wx' });
let verifiedCookieRequests = 0;
const server = createServer((request, response) => {
  assert.equal(request.headers.host, '127.0.0.1:18181');
  response.setHeader('Content-Type', 'text/html');
  if (request.url === '/seed') {
    response.setHeader('Set-Cookie', 'synthetic-profile=synthetic-cookie-v1; HttpOnly; Path=/; Max-Age=3600; SameSite=Lax');
    response.end('<script>localStorage.setItem("synthetic-storage","synthetic-localstorage-v1")</script><p>seeded</p>');
  } else if (request.url === '/check') {
    const matched = request.headers.cookie === 'synthetic-profile=synthetic-cookie-v1';
    if (matched) verifiedCookieRequests++;
    response.end(`<div id="cookie">${matched ? 'synthetic-cookie-v1' : 'MISSING'}</div>`);
  } else { response.statusCode = 404; response.end('not found'); }
});
await new Promise(resolve => server.listen(18181, '127.0.0.1', resolve));
let stage = 'fresh-profile-negative-control';
try {
  const emptyBrowser = await chromium.launchPersistentContext('/scratch/empty-profile', { headless: true, channel: 'chromium', viewport: { width: 1280, height: 720 } });
  const emptyPage = emptyBrowser.pages()[0] || await emptyBrowser.newPage();
  await emptyPage.goto('http://127.0.0.1:18181/check', { waitUntil: 'load', timeout: 15_000 });
  assert.deepEqual(await emptyPage.evaluate(() => ({ cookie: document.querySelector('#cookie').textContent, storage: localStorage.getItem('synthetic-storage') })), { cookie: 'MISSING', storage: null });
  assert.equal(verifiedCookieRequests, 0);
  await emptyBrowser.close();
  stage = 'seed';
  const browser = await chromium.launchPersistentContext('/scratch/seed-profile', { headless: true, channel: 'chromium', viewport: { width: 1280, height: 720 } });
  assert.equal(browser.browser().version(), '149.0.7827.0');
  const page = browser.pages()[0] || await browser.newPage();
  await page.goto('http://127.0.0.1:18181/seed', { waitUntil: 'load', timeout: 15_000 });
  assert.equal(await page.evaluate(() => localStorage.getItem('synthetic-storage')), 'synthetic-localstorage-v1');
  const cookies = await browser.cookies();
  assert(cookies.some(cookie => cookie.name === 'synthetic-profile' && cookie.httpOnly && cookie.expires > Date.now() / 1000));
  await browser.close();
  stage = 'historical-v1-production-encryption';
  await run('/test/legacy-helper', ['-test.run', '^TestExportSyntheticChromeProfile$', '-test.count=1'], { timeout: 60_000, maxBuffer: 64 << 10 });
  const sourceBefore = await sourceFingerprint();
  stage = 'image-runtime-migration-command';
  const migrated = await run('/usr/local/bin/openlinker-browser-runtime', ['migrate-profile', '--request-file', '/scratch/request.json'], { timeout: 60_000, maxBuffer: 64 << 10 });
  const migrationReport = JSON.parse(migrated.stdout.trim());
  assert.equal(migrationReport.status, 'success'); assert.equal(migrationReport.activation_ready, true);
  await writeFile('/scratch/migration-execute-report.json', migrated.stdout, { mode: 0o600, flag: 'wx' });
  const checked = await run('/usr/local/bin/openlinker-browser-runtime', ['migrate-profile', '--check', '--request-file', '/scratch/request.json'], { timeout: 60_000, maxBuffer: 64 << 10 });
  assert.equal(JSON.parse(checked.stdout).activation_ready, true);
  stage = 'migrate-and-profile-engine-restore';
  const result = await run('/test/new-helper', [], { timeout: 150_000, maxBuffer: 64 << 10 });
  const report = JSON.parse(result.stdout.trim());
  assert.equal(report.status, 'pass');
  assert(verifiedCookieRequests >= 1);
  assert.equal(await sourceFingerprint(), sourceBefore);
  console.log(JSON.stringify({ ...report, image_runtime_cli_execute_and_check: true, source_tree_bytes_mode_mtime_preserved: true, browser_version: '149.0.7827.0', persistent_cookie_positive_control: true, fresh_profile_negative_control: true, server_cookie_receipts: verifiedCookieRequests, container_network: 'none', synthetic_only: true }));
} catch (error) {
  console.log(JSON.stringify({ status: 'failed', stage, failure_type: error.name, exit_code: typeof error.code === 'number' ? error.code : null, safe_detail: typeof error.stderr === 'string' ? error.stderr.slice(-1500) : undefined }));
  process.exitCode = 1;
} finally { await new Promise(resolve => server.close(resolve)); }
