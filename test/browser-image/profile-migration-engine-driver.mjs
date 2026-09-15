// Test-only private JSON driver; REAL Chromium, production Go ProfileEngine.
// Deliberately not the production JS policy/native-extension/egress engine.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createInterface } from 'node:readline';
const require = createRequire('/opt/openlinker/browser-engine/package.json');
const { chromium } = require('playwright-core');
assert.equal(process.env.OPENLINKER_BROWSER_PROFILE_DIR, '/scratch/work/active');
const context = await chromium.launchPersistentContext('/scratch/work/active', { headless: true, channel: 'chromium', viewport: { width: 1280, height: 720 } });
assert.equal(context.browser().version(), '149.0.7827.0');
const page = context.pages()[0] || await context.newPage();
let sequence = 0;
try {
  for await (const line of createInterface({ input: process.stdin })) {
    const request = JSON.parse(line);
    assert.equal(request.contract_id, 'openlinker.browser.engine.v2');
    assert(['preflight', 'screenshot', 'checkpoint'].includes(request.action.kind));
    const observation = { page_state_id: `synthetic-page-${++sequence}`, viewport: { width: 1280, height: 720 }, navigation_generation: sequence };
    if (request.action.kind === 'preflight') observation.environment = { browser_engine: 'chromium', browser_distribution: 'playwright_chromium', browser_version: context.browser().version(), browser_major_version: 149, browser_locale: 'en-US', browser_timezone: 'UTC', font_contract_version: 'openlinker.browser.fonts.v1', font_manifest_sha256: process.env.OPENLINKER_BROWSER_FONT_MANIFEST_SHA256 };
    if (request.action.kind === 'screenshot') {
      await page.goto('http://127.0.0.1:18181/check', { waitUntil: 'load', timeout: 15_000 });
      const proof = await page.evaluate(() => ({ cookie: document.querySelector('#cookie').textContent, storage: localStorage.getItem('synthetic-storage') }));
      assert.deepEqual(proof, { cookie: 'synthetic-cookie-v1', storage: 'synthetic-localstorage-v1' });
      observation.title = 'SYNTHETIC_COOKIE_AND_STORAGE_RESTORED';
    }
    process.stdout.write(JSON.stringify({ contract_id: request.contract_id, action_id: request.action_id, status: 'ok', observation }) + '\n');
  }
} finally { await context.close(); }
