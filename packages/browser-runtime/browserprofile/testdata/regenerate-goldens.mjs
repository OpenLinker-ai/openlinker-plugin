#!/usr/bin/env node
// Synthetic fixtures only. Never accepts an existing Profile/key/store path.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const write = args.includes('--write');
const value = name => { const at = args.indexOf(name); assert(at >= 0 && args[at + 1] && !args[at + 1].startsWith('--'), `${name} required`); return resolve(args[at + 1]); };
const valuedFlags = ['--cli-repo', '--plugin-repo', '--build-chrome-helper'];
assert(args.every((arg, i) => arg === '--write' || valuedFlags.includes(arg) || valuedFlags.includes(args[i - 1])), 'unknown argument');
const chromeHelper = args.includes('--build-chrome-helper') ? value('--build-chrome-helper') : undefined;
if (chromeHelper) assert(!existsSync(chromeHelper), 'helper destination must not exist');
const specifications = [
  { name: 'v1', repo: value('--cli-repo'), commit: 'bd07dc4a25ba331b9ab80e14ccb88399888ab26c', module: 'github.com/OpenLinker-ai/openlinker-cli', prefix: 'pkg', xsys: 'v0.46.0' },
  { name: 'v2', repo: value('--plugin-repo'), commit: '07d03eb28a5c3e530acabdc167638a8c33cce20c', module: 'github.com/OpenLinker-ai/openlinker-plugin', prefix: 'packages/browser-runtime', xsys: 'v0.47.0' },
];
const sha = value => createHash('sha256').update(value).digest('hex');
const git = (repo, args) => execFileSync('git', ['-C', repo, ...args], { timeout: 10_000, maxBuffer: 4 << 20 });
const harness = readFileSync(join(here, 'export-golden.go.txt'));
for (const spec of specifications) {
  if (chromeHelper && spec.name !== 'v1') continue;
  assert.equal(git(spec.repo, ['rev-parse', `${spec.commit}^{commit}`]).toString().trim(), spec.commit);
  const root = mkdtempSync(join(tmpdir(), `openlinker-synthetic-${spec.name}-`));
  try {
    const paths = git(spec.repo, ['ls-tree', '-r', '--name-only', spec.commit, ...['browserprofile', 'browserprotocol', 'netpolicy'].map(name => `${spec.prefix}/${name}`)]).toString().trim().split('\n').filter(path => path.endsWith('.go') && !path.endsWith('_test.go') && !path.endsWith('/runtime_viewer.go')).sort();
    assert(paths.some(path => path.endsWith('/browserprofile/types.go')));
    const sourceFiles = paths.map(path => {
      const raw = git(spec.repo, ['show', `${spec.commit}:${path}`]);
      mkdirSync(dirname(join(root, path)), { recursive: true, mode: 0o700 });
      writeFileSync(join(root, path), raw, { mode: 0o600 });
      return { path, git_blob: git(spec.repo, ['rev-parse', `${spec.commit}:${path}`]).toString().trim(), sha256: sha(raw) };
    });
    const dependencies = [`golang.org/x/sys ${spec.xsys}`, ...(spec.name === 'v2' ? ['golang.org/x/net v0.56.0', 'golang.org/x/text v0.39.0'] : [])];
    const module = `module ${spec.module}\n\ngo 1.26.4\n\nrequire (\n${dependencies.map(dep => `\t${dep}`).join('\n')}\n)\n`;
    const sums = git(spec.repo, ['show', `${spec.commit}:go.sum`]).toString().split('\n').filter(line => dependencies.some(dep => line.startsWith(`${dep} `) || line.startsWith(`${dep}/go.mod `))).join('\n') + '\n';
    assert.equal(sums.trim().split('\n').length, dependencies.length * 2);
    writeFileSync(join(root, 'go.mod'), module, { mode: 0o600 });
    writeFileSync(join(root, 'go.sum'), sums, { mode: 0o600 });
    writeFileSync(join(root, spec.prefix, 'browserprofile', 'historical_export_test.go'), harness, { mode: 0o600 });
    const output = join(root, 'synthetic-golden.json');
    const env = { ...process.env, GOCACHE: process.env.GOCACHE || join(tmpdir(), 'openlinker-profile-golden-go-cache'), GOWORK: 'off', GOPROXY: 'off', GOSUMDB: 'off', GOTOOLCHAIN: 'local', GOLDEN_EXPORT_FILE: output };
    if (chromeHelper) {
      const chromeHarness = readFileSync(join(here, 'export-chrome-profile.go.txt'));
      writeFileSync(join(root, spec.prefix, 'browserprofile', 'historical_chrome_export_test.go'), chromeHarness, { mode: 0o600 });
      execFileSync('go', ['test', '-mod=readonly', '-c', '-o', chromeHelper, `./${spec.prefix}/browserprofile`], { cwd: root, env: { ...env, GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' }, timeout: 120_000, maxBuffer: 1 << 20 });
      process.stdout.write(JSON.stringify({ status: 'compiled', producer_commit: spec.commit, source_files: sourceFiles, harness_sha256: sha(chromeHarness), binary_sha256: sha(readFileSync(chromeHelper)), platform: 'linux/arm64', executed: false }) + '\n');
      continue;
    }
    execFileSync('go', ['test', '-mod=readonly', `./${spec.prefix}/browserprofile`, '-run', '^TestExportHistoricalGolden$', '-count=1'], { cwd: root, env, timeout: 120_000, maxBuffer: 1 << 20 });
    assert.equal(readFileSync(join(root, 'go.mod'), 'utf8'), module);
    assert.equal(readFileSync(join(root, 'go.sum'), 'utf8'), sums);
    const fixture = JSON.parse(readFileSync(output, 'utf8'));
    assert.equal(fixture.contract_id, `openlinker.browser.${spec.name}`);
    fixture.provenance = {
      producer_commit: spec.commit, producer_module: spec.module,
      source_files_unmodified: true, source_files: sourceFiles,
      excluded_unrelated_protocol_files: [`${spec.prefix}/browserprotocol/runtime_viewer.go`],
      harness_sha256: sha(harness), synthetic_module: module, synthetic_module_sum_sha256: sha(sums),
      production_algorithms_in_harness: false, network_used: false,
    };
    const serialized = `${JSON.stringify(fixture, null, 2)}\n`;
    const destination = join(here, `golden-${spec.name}.json`);
    if (write) writeFileSync(destination, serialized, { mode: 0o644 });
    else assert.equal(readFileSync(destination, 'utf8'), serialized, `${spec.name}: frozen bytes changed`);
    process.stdout.write(JSON.stringify({ fixture: spec.name, producer_commit: spec.commit, files: paths.length, sha256: sha(serialized), status: write ? 'generated' : 'reproduced' }) + '\n');
  } finally {
    // Only our freshly allocated synthetic scratch module is removed.
    rmSync(root, { recursive: true, force: true });
  }
}
