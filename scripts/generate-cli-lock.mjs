#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const repository = "OpenLinker-ai/openlinker-cli";
const args = process.argv.slice(2);
const tag = args.find((arg) => !arg.startsWith("--"));
const write = args.includes("--write");
if (!tag || args.some((arg) => arg.startsWith("--") && arg !== "--write")) {
  process.stderr.write("usage: generate-cli-lock.mjs <v0.2.x[-rc.N]> [--write]\n");
  process.exit(2);
}
if (!/^v0\.2\.\d+(?:-rc\.[1-9]\d*)?$/.test(tag)) {
  process.stderr.write(`refusing incompatible CLI tag: ${tag}\n`);
  process.exit(2);
}

const release = spawnSync("gh", [
  "release", "view", tag,
  "--repo", repository,
  "--json", "tagName,assets,url,isDraft",
], { encoding: "utf8" });
if (release.status !== 0) {
  process.stderr.write(release.stderr || `cannot read ${repository} release ${tag}\n`);
  process.exit(2);
}
const metadata = JSON.parse(release.stdout);
if (metadata.tagName !== tag || metadata.isDraft) {
  process.stderr.write("CLI release must exist and must not be a draft\n");
  process.exit(2);
}

const contract = JSON.parse(await readFile(join(repoRoot, "contracts", "plugin-surface.json"), "utf8"));
const capabilities = [...new Set(Object.values(contract.operations).map((operation) => operation.cli_capability))].sort();
const releaseAssets = new Map(metadata.assets.map((asset) => [asset.name, asset]));
const assets = {};
for (const os of ["darwin", "linux", "windows"]) {
  for (const arch of ["amd64", "arm64"]) {
    const key = `${os}-${arch}`;
    const extension = os === "windows" ? "zip" : "tar.gz";
    const archive = `openlinker-cli-${tag}-${key}.${extension}`;
    const archiveAsset = releaseAssets.get(archive);
    const checksumAsset = releaseAssets.get(`${archive}.sha256`);
    const digest = archiveAsset?.digest?.replace(/^sha256:/, "");
    if (!archiveAsset || !checksumAsset || !/^[a-f0-9]{64}$/.test(digest || "")) {
      process.stderr.write(`release is missing archive, adjacent checksum, or GitHub digest for ${key}\n`);
      process.exit(2);
    }
    assets[key] = {
      archive,
      archive_url: archiveAsset.url,
      checksum_url: checksumAsset.url,
      sha256: digest,
      executable_path: `openlinker-cli-${tag}-${key}/${os === "windows" ? "openlinker.exe" : "openlinker"}`,
    };
  }
}

const lock = {
  schema_version: 1,
  version: tag,
  surface_version: contract.cli.surface_version,
  capabilities,
  repository,
  release_url: metadata.url,
  assets,
};
const output = `${JSON.stringify(lock, null, 2)}\n`;
if (write) {
  const destination = join(repoRoot, "plugins", "openlinker", "cli-lock.json");
  await writeFile(destination, output);
  process.stdout.write(`${destination}\n`);
} else {
  process.stdout.write(output);
}
