import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { download, sha256 } from "../platforms/codex/openlinker/scripts/install-openlinker-cli.mjs";
import { platforms, hostCapabilities, binaryName, validateHostLock } from "../platforms/codex/openlinker/scripts/plugin-host-lock.mjs";

const tag = process.argv[2];
assert.match(tag ?? "", /^v\d+\.\d+\.\d+(?:-rc\.\d+)?$/);
assert.ok(process.argv.slice(3).every(arg => arg === "--write"), "usage: generate-host-lock.mjs TAG [--write]");
const repository = "OpenLinker-ai/openlinker-plugin";
const root = resolve(import.meta.dirname, "..");
const commit = execFileSync("git", ["rev-parse", `${tag}^{commit}`], { cwd: root, encoding: "utf8" }).trim();
const lock = { schema_version: 1, repository, version: tag, plugin_commit: commit, surface_version: "openlinker.plugin-host.v1", capabilities: hostCapabilities, assets: {} };
const work = await mkdtemp(join(tmpdir(), "openlinker-host-lock-"));
try {
  for (const target of platforms) {
    const archive = `openlinker-plugin-host-${tag}-${target}.tar.gz`;
    const url = `https://github.com/${repository}/releases/download/${tag}/${archive}`;
    const path = join(work, archive);
    await download(url, path);
    await download(`${url}.sha256`, `${path}.sha256`);
    const digest = await sha256(path);
    assert.equal((await readFile(`${path}.sha256`, "utf8")).trim(), `${digest}  ${archive}`);
    const metadata = JSON.parse(execFileSync("tar", ["-xOzf", path, "host-build-info.json"], { encoding: "utf8" }));
    assert.equal(metadata.plugin_commit, commit);
    assert.equal(metadata.plugin_host_version, tag);
    assert.equal(metadata.host_platform, target);
    const { createHash } = await import("node:crypto");
    const binary = execFileSync("tar", ["-xOzf", path, binaryName(target)], { maxBuffer: 100 * 1024 * 1024 });
    assert.equal(createHash("sha256").update(binary).digest("hex"), metadata.plugin_host_sha256);
    lock.assets[target] = { archive, archive_url: url, checksum_url: `${url}.sha256`, sha256: digest, binary_sha256: metadata.plugin_host_sha256, executable_path: binaryName(target), metadata_path: "host-build-info.json" };
  }
  validateHostLock(lock);
  const output = `${JSON.stringify(lock, null, 2)}\n`;
  if (process.argv.includes("--write")) {
    for (const file of ["shared/host-lock.json", "platforms/codex/openlinker/host-lock.json", "platforms/claude/openlinker/host-lock.json"]) await writeFile(join(root, file), output);
    console.log(`Locked six verified public Plugin host archives: ${tag} (${commit})`);
  } else console.log(output);
} finally {
  await rm(work, { recursive: true, force: true });
}
