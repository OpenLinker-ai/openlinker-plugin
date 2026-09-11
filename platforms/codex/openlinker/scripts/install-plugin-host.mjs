#!/usr/bin/env node
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rename, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { normalizePlatform, defaultDataDir, download, sha256, extractExecutable, assertNoSymlinkPath } from "./install-openlinker-cli.mjs";
import { binaryName, installedHostDirectory, readHostLock, validateHostLock } from "./plugin-host-lock.mjs";
import { verifyPluginHost } from "./resolve-plugin-host.mjs";

export async function installPluginHost({ pluginRoot = resolve(import.meta.dirname, ".."), lock = readHostLock(pluginRoot), target = normalizePlatform(), dataDir = defaultDataDir(), downloader = download, planOnly = false } = {}) {
  validateHostLock(lock);
  assert.ok(lock.assets[target.key], "unsupported Plugin host platform");
  const asset = lock.assets[target.key];
  const directory = installedHostDirectory(resolve(dataDir), lock, target.key);
  const destination = join(directory, binaryName(target.key));
  const plan = { version: lock.version, plugin_commit: lock.plugin_commit, repository: lock.repository, platform: target.key, archive_url: asset.archive_url, sha256: asset.sha256, destination };
  if (planOnly) return plan;
  await assertNoSymlinkPath(directory);
  try {
    verifyPluginHost(destination, target.key, lock.capabilities, lock);
    return { ...plan, installed: true, reused: true };
  } catch (error) {
    // A damaged immutable installation needs explicit removal, never silent replacement.
    if (error.code !== "ENOENT") throw error;
  }
  const parent = resolve(directory, "..");
  await mkdir(parent, { recursive: true, mode: 0o700 });
  await assertNoSymlinkPath(parent);
  const staging = await mkdtemp(join(parent, ".install-"));
  try {
    const archive = join(staging, asset.archive);
    const checksum = `${archive}.sha256`;
    await downloader(asset.archive_url, archive);
    await downloader(asset.checksum_url, checksum);
    const adjacent = (await readFile(checksum, "utf8")).trim().split(/\s+/);
    assert.equal(adjacent.length, 2, "invalid adjacent checksum file");
    assert.equal(adjacent[0], asset.sha256, "adjacent checksum differs from host lock");
    assert.equal(adjacent[1].replace(/^\*/, ""), asset.archive, "adjacent checksum names another archive");
    assert.equal(await sha256(archive), asset.sha256, "archive checksum differs from host lock");
    await extractExecutable(archive, asset, target, join(staging, binaryName(target.key)));
    await extractExecutable(archive, { ...asset, executable_path: asset.metadata_path }, target, join(staging, asset.metadata_path));
    verifyPluginHost(join(staging, binaryName(target.key)), target.key, lock.capabilities, lock);
    await rm(archive);
    await rm(checksum);
    try {
      // Binary and metadata become visible together; concurrent installs use the same immutable pin.
      await rename(staging, directory);
    } catch (error) {
      if (!["EEXIST", "ENOTEMPTY"].includes(error.code)) throw error;
      verifyPluginHost(destination, target.key, lock.capabilities, lock);
    }
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
  return { ...plan, installed: true };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    assert.ok(process.argv.slice(2).every(arg => arg === "--plan"), "usage: install-plugin-host.mjs [--plan]");
    console.log(JSON.stringify(await installPluginHost({ planOnly: process.argv.includes("--plan") }), null, 2));
  } catch (error) {
    console.error(`openlinker Plugin host installation failed: ${error.message}`);
    process.exitCode = 2;
  }
}
