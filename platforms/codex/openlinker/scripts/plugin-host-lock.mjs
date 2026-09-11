import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";

export const platforms = ["darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"];
export const hostCapabilities = ["agent.configure", "agent.doctor", "agent.serve", "agent.status", "plugin.browser.serve", "plugin.serve"];
export const binaryName = target => `openlinker-plugin-host${target.startsWith("windows-") ? ".exe" : ""}`;

export function validateHostLock(lock) {
  assert.equal(lock.schema_version, 1);
  assert.equal(lock.test_fixture, undefined);
  assert.equal(lock.repository, "OpenLinker-ai/openlinker-plugin");
  assert.equal(lock.surface_version, "openlinker.plugin-host.v1");
  assert.match(lock.version, /^v\d+\.\d+\.\d+(?:-rc\.\d+)?$/);
  assert.match(lock.plugin_commit, /^[a-f0-9]{40}$/);
  assert.match(lock.source_tree_sha256, /^[a-f0-9]{64}$/);
  assert.deepEqual(lock.capabilities, hostCapabilities);
  assert.deepEqual(Object.keys(lock.assets).sort(), platforms);
  for (const target of platforms) {
    const asset = lock.assets[target];
    const archive = `openlinker-plugin-host-${lock.version}-${target}.tar.gz`;
    const base = `https://github.com/${lock.repository}/releases/download/${lock.version}/${archive}`;
    assert.equal(asset.archive, archive);
    assert.equal(asset.archive_url, base);
    assert.equal(asset.checksum_url, `${base}.sha256`);
    assert.equal(asset.executable_path, binaryName(target));
    assert.equal(asset.metadata_path, "host-build-info.json");
    assert.match(asset.sha256, /^[a-f0-9]{64}$/);
    assert.match(asset.binary_sha256, /^[a-f0-9]{64}$/);
  }
  return lock;
}

export function readHostLock(pluginRoot) {
  return validateHostLock(JSON.parse(readFileSync(join(pluginRoot, "host-lock.json"), "utf8")));
}

export function installedHostDirectory(dataDir, lock, target) {
  return join(dataDir, "hosts", lock.version, target, lock.assets[target].sha256);
}
