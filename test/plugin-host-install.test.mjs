import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cp, mkdtemp, mkdir, readFile, realpath, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { installPluginHost } from "../platforms/codex/openlinker/scripts/install-plugin-host.mjs";
import { normalizePlatform } from "../platforms/codex/openlinker/scripts/install-openlinker-cli.mjs";
import { binaryName, platforms, hostCapabilities, validateHostLock } from "../platforms/codex/openlinker/scripts/plugin-host-lock.mjs";
import { resolvePluginHost } from "../platforms/codex/openlinker/scripts/resolve-plugin-host.mjs";

async function fixture(t, { omittedMetadata = false, linkedBinary = false, wrongSource = false } = {}) {
  const root = await realpath(await mkdtemp(join(tmpdir(), "host install ")));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = join(root, "source"), dataDir = join(root, "private data");
  await mkdir(source);
  const target = normalizePlatform(), version = "v0.1.58-rc.1", commit = "1".repeat(40);
  const context = { surface_version: "openlinker.plugin-host.v1", host_version: version, plugin_commit: commit, capabilities: hostCapabilities };
  const bytes = `#!/bin/sh\nprintf '%s\\n' '${JSON.stringify(context)}'\n`;
  const binaryHash = createHash("sha256").update(bytes).digest("hex");
  if (linkedBinary) {
    await writeFile(join(source, "target"), bytes, { mode: 0o700 });
    await symlink("target", join(source, binaryName(target.key)));
  } else await writeFile(join(source, binaryName(target.key)), bytes, { mode: 0o700 });
  const metadata = { source_contract_id: "openlinker.plugin-host-sources.v1", host_platform: target.key, plugin_commit: wrongSource ? "2".repeat(40) : commit, plugin_host_version: version, plugin_host_sha256: binaryHash };
  if (!omittedMetadata) await writeFile(join(source, "host-build-info.json"), JSON.stringify(metadata));
  const archivePath = join(root, "release.tar.gz");
  const archived = spawnSync("tar", ["-czf", archivePath, "-C", source, binaryName(target.key), ...(omittedMetadata ? [] : ["host-build-info.json"])]);
  assert.equal(archived.status, 0, archived.stderr?.toString());
  const archiveBytes = await readFile(archivePath), archiveHash = createHash("sha256").update(archiveBytes).digest("hex");
  const lock = { schema_version: 1, repository: "OpenLinker-ai/openlinker-plugin", surface_version: context.surface_version, version, plugin_commit: commit, capabilities: hostCapabilities, assets: {} };
  for (const platform of platforms) {
    const archive = `openlinker-plugin-host-${version}-${platform}.tar.gz`;
    const url = `https://github.com/${lock.repository}/releases/download/${version}/${archive}`;
    lock.assets[platform] = { archive, archive_url: url, checksum_url: `${url}.sha256`, sha256: archiveHash, binary_sha256: binaryHash, executable_path: binaryName(platform), metadata_path: "host-build-info.json" };
  }
  const downloads = [];
  const downloader = async (url, path) => {
    downloads.push(url);
    await writeFile(path, url.endsWith(".sha256") ? `${archiveHash}  ${lock.assets[target.key].archive}\n` : archiveBytes);
  };
  return { root, dataDir, target, lock, downloader, downloads };
}

test("host lock rejects floating, forged, foreign-platform and traversal inputs", async t => {
  const { lock } = await fixture(t);
  for (const mutate of [l => l.version = "latest", l => l.plugin_commit = "short", l => l.test_fixture = true, l => delete l.assets["linux-arm64"], l => l.capabilities = [], l => l.assets["linux-amd64"].archive_url = "https://evil.test/file", l => l.assets["linux-amd64"].executable_path = "../openlinker-plugin-host", l => l.assets["linux-amd64"].metadata_path = "../host-build-info.json"]) {
    const candidate = structuredClone(lock); mutate(candidate);
    assert.throws(() => validateHostLock(candidate));
  }
});

test("plan has no download or filesystem mutation", async t => {
  const options = await fixture(t);
  const plan = await installPluginHost({ ...options, planOnly: true });
  assert.equal(plan.platform, options.target.key);
  assert.equal(options.downloads.length, 0);
  await assert.rejects(readdir(options.dataDir), { code: "ENOENT" });
});

test("Git marketplace packages resolve an explicitly installed host without bundled binaries or CLI", { skip: process.platform === "win32" }, async t => {
  const options = await fixture(t);
  const result = await installPluginHost(options);
  assert.equal(result.installed, true);
  assert.equal(options.downloads.length, 2);
  const second = await installPluginHost({ ...options, downloader: () => assert.fail("must reuse verified pin") });
  assert.equal(second.reused, true);
  for (const host of ["codex", "claude"]) {
    const pluginRoot = join(options.root, host, "openlinker");
    await cp(resolve(import.meta.dirname, `../platforms/${host}/openlinker`), pluginRoot, { recursive: true });
    await writeFile(join(pluginRoot, "host-lock.json"), JSON.stringify(options.lock));
    assert.equal(resolvePluginHost({ pluginRoot, dataDir: options.dataDir, explicit: "", required: hostCapabilities }), result.destination);
    const response = spawnSync(join(pluginRoot, "bin/openlinker-plugin"), ["context"], { encoding: "utf8", env: { ...process.env, PLUGIN_ROOT: pluginRoot, OPENLINKER_PLUGIN_HOST_BIN: "", OPENLINKER_PLUGIN_DATA: options.dataDir, OPENLINKER_CLI_BIN: "/nonexistent" } });
    assert.equal(response.status, 0, response.stderr);
    assert.equal(JSON.parse(response.stdout).host_version, options.lock.version);
  }
  await writeFile(result.destination, "tampered");
  await assert.rejects(installPluginHost(options), /checksum mismatch/);
});

for (const failure of ["archive", "checksum", "metadata", "source", "symlink", "network"]) {
  test(`failed ${failure} verification leaves previous versions and no partial install`, { skip: process.platform === "win32" }, async t => {
    const options = await fixture(t, { omittedMetadata: failure === "metadata", linkedBinary: failure === "symlink", wrongSource: failure === "source" });
    await mkdir(options.dataDir, { recursive: true });
    const previous = join(options.dataDir, "previous-host");
    await writeFile(previous, "keep");
    const downloader = async (url, path) => {
      if (failure === "network") throw new Error("network interrupted");
      await options.downloader(url, path);
      if ((failure === "archive" && !url.endsWith("sha256")) || (failure === "checksum" && url.endsWith("sha256"))) await writeFile(path, "corrupt");
    };
    await assert.rejects(installPluginHost({ ...options, downloader }));
    assert.equal(await readFile(previous, "utf8"), "keep");
    const parent = join(options.dataDir, "hosts", options.lock.version, options.target.key);
    assert.deepEqual(await readdir(parent), []);
  });
}

test("symlink installation destinations are rejected before download", { skip: process.platform === "win32" }, async t => {
  const options = await fixture(t);
  const linked = join(options.root, "linked");
  await symlink(options.root, linked);
  await assert.rejects(installPluginHost({ ...options, dataDir: linked }), /symlink/);
  assert.equal(options.downloads.length, 0);
});
