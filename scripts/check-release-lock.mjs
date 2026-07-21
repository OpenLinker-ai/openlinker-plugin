import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const lockPath = join(repoRoot, "plugins", "openlinker", "cli-lock.json");
let lock;
try {
  lock = JSON.parse(await readFile(lockPath, "utf8"));
} catch (error) {
  if (error?.code === "ENOENT") {
    throw new Error("release gate: generate plugins/openlinker/cli-lock.json from the published CLI release");
  }
  throw error;
}
assert.equal(lock.schema_version, 1);
assert.match(lock.version, /^v0\.2\.\d+(?:-rc\.[1-9]\d*)?$/);
assert.equal(lock.surface_version, "openlinker.cli.v1");
assert.equal(lock.repository, "OpenLinker-ai/openlinker-cli");
assert.equal(lock.test_fixture, undefined);
assert.ok(Array.isArray(lock.capabilities) && lock.capabilities.length > 0);
for (const key of ["darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"]) {
  const asset = lock.assets?.[key];
  assert.ok(asset, `missing ${key}`);
  assert.match(asset.sha256, /^[a-f0-9]{64}$/);
  assert.match(asset.archive_url, /^https:\/\/github\.com\/OpenLinker-ai\/openlinker-cli\/releases\/download\//);
  assert.match(asset.checksum_url, /^https:\/\/github\.com\/OpenLinker-ai\/openlinker-cli\/releases\/download\//);
  assert.ok(asset.executable_path.endsWith(key.startsWith("windows-") ? "/openlinker.exe" : "/openlinker"));
}
console.log(`release lock passed: ${lock.version}`);
