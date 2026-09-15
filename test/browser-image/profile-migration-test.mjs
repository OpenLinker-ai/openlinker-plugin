#!/usr/bin/env node
// Opt-in real Chromium test. Every mutable byte lives in a newly allocated
// host build directory or this operation's tmpfs-only network-none container.
import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '../..');
const args = process.argv.slice(2);
assert((args.length === 4 || (args.length === 6 && args[4] === '--image')) && args[0] === '--cli-repo' && args[2] === '--report', 'usage: --cli-repo <historical-clone> --report <new-json-path> [--image sha256:<immutable-digest>]');
const cliRepo = resolve(args[1]);
const reportPath = resolve(args[3]);
assert(!existsSync(reportPath), 'report must not overwrite prior evidence');
const image = args[5] || 'sha256:b38361af310a081fc2bdbba266ee98eeaecf659e97a1b2b1c574af1d6e98f4e9';
assert(/^sha256:[a-f0-9]{64}$/.test(image), 'mutable image tags are forbidden');
const operation = randomUUID();
const name = `openlinker-profile-synthetic-${operation}`;
const artifacts = mkdtempSync(join(tmpdir(), 'openlinker-profile-synthetic-'));
chmodSync(artifacts, 0o755);
const sha = value => createHash('sha256').update(value).digest('hex');
const goEnv = { ...process.env, GOCACHE: process.env.GOCACHE || join(tmpdir(), 'openlinker-profile-golden-go-cache'), GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', GOTOOLCHAIN: 'local', GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' };
const docker = args => execFileSync('docker', ['--context', 'desktop-linux', ...args], { timeout: 240_000, maxBuffer: 1 << 20, encoding: 'utf8' });
const report = { schema: 'openlinker.browser.profile-migration-chrome-test.v1', status: 'failed', operation_sha256: sha(operation), image, historical_producer_commit: 'bd07dc4a25ba331b9ab80e14ccb88399888ab26c', synthetic_only: true, platform: 'linux/arm64', source_or_credentials_from_live_environment: false, artifact_directory: artifacts };
const started = Date.now();
let stage = 'build';
try {
  const inspect = JSON.parse(docker(['image', 'inspect', image]))[0];
  assert.equal(inspect.Id, image); assert.equal(inspect.Os, 'linux'); assert.equal(inspect.Architecture, 'arm64');
  report.image_source_revision = inspect.Config.Labels?.['org.opencontainers.image.revision'] || null;
  const generated = execFileSync(process.execPath, [join(root, 'packages/browser-runtime/browserprofile/testdata/regenerate-goldens.mjs'), '--cli-repo', cliRepo, '--plugin-repo', root, '--build-chrome-helper', join(artifacts, 'legacy-helper')], { cwd: root, env: goEnv, timeout: 180_000, maxBuffer: 1 << 20, encoding: 'utf8' });
  report.historical_producer_provenance = JSON.parse(generated.trim());
  copyFileSync(join(here, 'profile-migration-live-helper.go.txt'), join(artifacts, 'helper.go'));
  execFileSync('go', ['build', '-mod=readonly', '-trimpath', '-o', join(artifacts, 'new-helper'), join(artifacts, 'helper.go')], { cwd: root, env: goEnv, timeout: 180_000, maxBuffer: 1 << 20 });
  report.new_helper_sha256 = sha(readFileSync(join(artifacts, 'new-helper')));
  report.fixture_sha256 = Object.fromEntries(['profile-migration-live-helper.go.txt', 'profile-migration-live.mjs', 'profile-migration-engine-driver.mjs', 'profile-migration-test.mjs'].map(filename => [filename, sha(readFileSync(join(here, filename)))]));
  for (const filename of ['profile-migration-live.mjs', 'profile-migration-engine-driver.mjs']) { copyFileSync(join(here, filename), join(artifacts, filename)); chmodSync(join(artifacts, filename), 0o444); }
  for (const filename of ['legacy-helper', 'new-helper']) chmodSync(join(artifacts, filename), 0o555);
  stage = 'linux-chromium-execution';
  const output = docker(['run', '--rm', '--init', '--name', name, '--label', `openlinker.synthetic-profile-operation=${operation}`, '--read-only', '--network', 'none', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--user', '10001:10001', '--mount', `type=bind,source=${artifacts},target=/test,readonly`, '--tmpfs', '/scratch:rw,nosuid,nodev,size=1g,mode=0700,uid=10001,gid=10001', '--tmpfs', '/tmp:rw,nosuid,nodev,size=64m,mode=1777', '--env', 'HOME=/scratch/home', '--env', 'TMPDIR=/scratch/tmp', '--entrypoint', '/usr/bin/node', image, '/test/profile-migration-live.mjs']);
  report.observation = JSON.parse(output.trim());
  assert.equal(report.observation.status, 'pass');
  report.status = 'pass';
} catch (error) {
  report.failed_stage = stage; report.failure_type = error.name;
  // All children here see only public code and fresh synthetic state.
  if (error.stdout) { try { report.observation = JSON.parse(String(error.stdout).trim()); } catch {} }
  if (error.stderr) report.synthetic_diagnostic = String(error.stderr).slice(-2000);
  process.exitCode = 1;
} finally {
  // A timed-out client does not guarantee its container stopped. Reconcile only
  // our exact random operation label before removing the synthetic container.
  try {
    const container = JSON.parse(docker(['container', 'inspect', name]))[0];
    assert.equal(container.Config.Labels['openlinker.synthetic-profile-operation'], operation);
    docker(['container', 'rm', '-f', name]);
    report.synthetic_container_removed = true;
  } catch (error) {
    if (String(error.stderr).includes('No such container')) report.synthetic_container_removed = true;
    else if (report.status === 'pass') { report.status = 'failed'; report.cleanup_failure_type = error.name; process.exitCode = 1; }
  }
  report.elapsed_ms = Date.now() - started;
  writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  console.log(JSON.stringify({ status: report.status, report: reportPath, elapsed_ms: report.elapsed_ms, failure_type: report.failure_type, failed_stage: report.failed_stage }));
}
