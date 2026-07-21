import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmod, copyFile, mkdir, mkdtemp, readFile, realpath, writeFile } from "node:fs/promises";
import { arch, platform, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { installForTest } from "../../plugins/openlinker/scripts/install-openlinker-cli.mjs";

const repoRoot = resolve(import.meta.dirname, "../..");
const installer = join(repoRoot, "plugins/openlinker/scripts/install-openlinker-cli.mjs");
const resolver = join(repoRoot, "plugins/openlinker/scripts/resolve-openlinker-cli");
const testRoot = await realpath(await mkdtemp(join(tmpdir(), "openlinker-installer-test-")));
const dataDir = join(testRoot, "data");
const fixtureDir = join(testRoot, "fixture");
const version = "v0.2.0-rc.1";
const hostOS = platform() === "win32" ? "windows" : platform();
const hostArch = arch() === "x64" ? "amd64" : arch();

if (!["darwin", "linux"].includes(hostOS) || !["amd64", "arm64"].includes(hostArch)) {
  console.log(`installer fixture skipped on ${hostOS}/${hostArch}`);
  process.exit(0);
}

const artifactBase = `openlinker-cli-${version}-${hostOS}-${hostArch}`;
const artifactDir = join(fixtureDir, artifactBase);
await mkdir(artifactDir, { recursive: true });
const fakeCLI = join(artifactDir, "openlinker");
await writeFile(fakeCLI, `#!/bin/sh
if [ "$1" != "context" ]; then exit 9; fi
cat <<'JSON'
{"api_base":"https://api.openlinker.ai","cli_version":"${version}","surface_version":"openlinker.cli.v1","capabilities":["agents.search","agents.get","tasks.create","runs.sync","runs.async","runs.get","runs.events","runs.artifacts","runs.cancel"]}
JSON
`);
await chmod(fakeCLI, 0o755);

const archiveName = `${artifactBase}.tar.gz`;
const archive = join(testRoot, archiveName);
let command = spawnSync("tar", ["-C", fixtureDir, "-czf", archive, artifactBase], { encoding: "utf8" });
assert.equal(command.status, 0, command.stderr);
const digest = createHash("sha256").update(await readFile(archive)).digest("hex");
const checksum = `${archive}.sha256`;
await writeFile(checksum, `${digest}  ${archiveName}\n`);

const lock = {
  schema_version: 1,
  test_fixture: true,
  version,
  surface_version: "openlinker.cli.v1",
  capabilities: ["agents.search", "runs.async", "runs.cancel"],
  repository: "OpenLinker-ai/openlinker-cli",
  release_url: "https://github.com/OpenLinker-ai/openlinker-cli/releases/tag/v0.2.0-rc.1",
  assets: {
    [`${hostOS}-${hostArch}`]: {
      archive: archiveName,
      archive_url: pathToFileURL(archive).href,
      checksum_url: pathToFileURL(checksum).href,
      sha256: digest,
      executable_path: `${artifactBase}/openlinker`,
    },
  },
};
const env = {
  ...process.env,
  OPENLINKER_PLUGIN_DATA: dataDir,
};
const target = { platform: hostOS, arch: hostArch, key: `${hostOS}-${hostArch}` };
const fixtureDownloader = async (url, destination) => copyFile(fileURLToPath(url), destination);
const plan = await installForTest({ lock, target, dataDir, downloader: fixtureDownloader, planOnly: true });
assert.equal(plan.version, version);
assert.equal(plan.platform, `${hostOS}-${hostArch}`);
assert.equal(plan.destination, join(dataDir, "bin", "openlinker"));

const installed = await installForTest({ lock, target, dataDir, downloader: fixtureDownloader });
assert.equal(installed.installed, true);

command = spawnSync(resolver, ["--require", "runs.cancel"], { encoding: "utf8", env });
assert.equal(command.status, 0, command.stderr);
assert.equal(command.stdout.trim(), join(dataDir, "bin", "openlinker"));

const installedBeforeFailure = await readFile(join(dataDir, "bin", "openlinker"));
lock.assets[`${hostOS}-${hostArch}`].sha256 = "0".repeat(64);
await assert.rejects(
  installForTest({ lock, target, dataDir, downloader: fixtureDownloader }),
  /checksum does not match/,
);
assert.deepEqual(await readFile(join(dataDir, "bin", "openlinker")), installedBeforeFailure);

console.log("installer tests passed");
