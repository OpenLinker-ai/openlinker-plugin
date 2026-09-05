import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(import.meta.dirname, "..");
const pluginModule = "github.com/OpenLinker-ai/openlinker-plugin";
const sdkModule = "github.com/OpenLinker-ai/openlinker-go";
const maximumArchiveBytes = 128 * 1024 * 1024;

export function verifyBuildInfo(info, platform, expectedSDK) {
  const lines = info.split(/\r?\n/).map((line) => line.trim().split(/\s+/));
  const versions = {};
  assert.ok(!lines.some((line) => line[0] === "=>"), "CLI build contains a replaced module; publish an immutable dependency first");
  for (const moduleName of [pluginModule, sdkModule]) {
    const dependency = lines.find((line) => line[0] === "dep" && line[1] === moduleName);
    assert.ok(dependency, `incompatible CLI: missing ${moduleName}; publish the migrated Plugin module and CLI, then regenerate shared/cli-lock.json`);
    assert.match(dependency[2], /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/, `CLI dependency is not immutable: ${moduleName}`);
    assert.notEqual(dependency[2], "v0.0.0", `CLI dependency is a placeholder: ${moduleName}`);
    assert.match(dependency[3] ?? "", /^h1:[A-Za-z0-9+/]{43}=$/, `CLI dependency checksum missing: ${moduleName}`);
    if (moduleName === sdkModule && expectedSDK) assert.equal(dependency[2], expectedSDK, "CLI and image SDK versions differ");
    versions[moduleName] = dependency[2];
  }
  const [goos, goarch] = platform.split("-");
  assert.ok(lines.some((line) => line[0] === "build" && line[1] === `GOOS=${goos}`), "CLI GOOS differs from image target");
  assert.ok(lines.some((line) => line[0] === "build" && line[1] === `GOARCH=${goarch}`), "CLI GOARCH differs from image target");
  const revision = lines.find((line) => line[0] === "build" && line[1]?.startsWith("vcs.revision="))?.[1].slice("vcs.revision=".length);
  assert.match(revision ?? "", /^[a-f0-9]{40}$/, "CLI has no immutable vcs.revision build metadata");
  assert.ok(lines.some((line) => line[0] === "build" && line[1] === "vcs=git"), "CLI has no Git source metadata");
  assert.ok(lines.some((line) => line[0] === "build" && line[1] === "vcs.modified=false"), "CLI was not built from a clean immutable checkout");
  return { cli_commit: revision, plugin_module_version: versions[pluginModule], openlinker_go_version: versions[sdkModule] };
}

export function validateAsset(lock, platform) {
  assert.match(platform, /^linux-(?:amd64|arm64)$/, "Provider images require a supported Linux target");
  assert.equal(lock.schema_version, 1);
  assert.equal(lock.test_fixture, undefined, "fixture locks cannot build production images");
  assert.equal(lock.repository, "OpenLinker-ai/openlinker-cli");
  assert.equal(lock.surface_version, "openlinker.cli.v1");
  assert.match(lock.version, /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/);
  const asset = lock.assets?.[platform];
  assert.ok(asset, `CLI lock has no ${platform} artifact`);
  const archiveRoot = `openlinker-cli-${lock.version}-${platform}`;
  assert.equal(asset.archive, `${archiveRoot}.tar.gz`);
  assert.equal(asset.archive_url, `https://github.com/${lock.repository}/releases/download/${lock.version}/${asset.archive}`);
  assert.equal(asset.executable_path, `${archiveRoot}/openlinker`);
  assert.match(asset.sha256, /^[a-f0-9]{64}$/);
  return { ...asset, archiveRoot };
}

async function fetchArchive(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(120_000) });
  assert.ok(response.ok, `CLI artifact download failed: HTTP ${response.status}; publish the compatible CLI before building Provider images`);
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    assert.ok(size <= maximumArchiveBytes, "CLI archive exceeds size limit");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

export async function downloadPinnedCLI({ platform, output, lockPath, archivePath, expectedSDK, metadataOutput }) {
  const lock = JSON.parse(await readFile(lockPath ?? join(repositoryRoot, "shared/cli-lock.json"), "utf8"));
  const asset = validateAsset(lock, platform);
  const bytes = archivePath ? await readFile(archivePath) : await fetchArchive(asset.archive_url);
  assert.ok(bytes.length > 0 && bytes.length <= maximumArchiveBytes, "CLI archive size is invalid");
  assert.equal(createHash("sha256").update(bytes).digest("hex"), asset.sha256, "CLI archive SHA-256 differs from shared/cli-lock.json");
  const staging = await mkdtemp(join(tmpdir(), "openlinker-cli-artifact-"));
  try {
    const archive = join(staging, "cli.tar.gz");
    await writeFile(archive, bytes, { mode: 0o600 });
    const entries = execFileSync("tar", ["-tzf", archive], { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 }).trim().split(/\r?\n/);
    assert.ok(entries.length > 0 && entries.length <= 4096, "CLI archive member count is invalid");
    for (const entry of entries) {
      assert.ok(entry === asset.archiveRoot || entry.startsWith(`${asset.archiveRoot}/`), "CLI archive member escapes its release root");
      assert.ok(!entry.split("/").some((part) => part === ".." || part === ".") && !entry.includes("\\"), "CLI archive member has an unsafe path");
    }
    const details = execFileSync("tar", ["-tvzf", archive], { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 });
    assert.ok(details.trim().split(/\r?\n/).every((line) => /^[d-]/.test(line)), "CLI archive contains links or special files");
    execFileSync("tar", ["-xzf", archive, "-C", staging], { stdio: "pipe" });
    const binary = join(staging, asset.executable_path);
    const info = execFileSync("go", ["version", "-m", binary], { encoding: "utf8" });
    const buildInfo = verifyBuildInfo(info, platform, expectedSDK);
    await mkdir(dirname(resolve(output)), { recursive: true });
    await copyFile(binary, resolve(output));
    await chmod(resolve(output), 0o555);
    const metadata = { ...buildInfo, cli_release: lock.version, cli_archive_sha256: asset.sha256 };
    const metadataPath = resolve(metadataOutput ?? join(dirname(resolve(output)), "cli-build-info.json"));
    await mkdir(dirname(metadataPath), { recursive: true });
    await writeFile(metadataPath, `${JSON.stringify(metadata, null, 2)}\n`, { mode: 0o444 });
    return { version: lock.version, platform, sha256: asset.sha256 };
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    const value = (flag) => { const index = args.indexOf(flag); return index < 0 ? undefined : args[index + 1]; };
    assert.ok(value("--platform") && value("--out"), "usage: download-pinned-cli.mjs --platform linux-amd64|linux-arm64 --out PATH [--lock PATH] [--archive PATH] [--expected-sdk VERSION]");
    const result = await downloadPinnedCLI({ platform: value("--platform"), output: value("--out"), lockPath: value("--lock"), archivePath: value("--archive"), expectedSDK: value("--expected-sdk"), metadataOutput: value("--metadata-out") });
    console.log(`Verified compatible CLI artifact: ${result.version} ${result.platform}`);
  } catch (error) {
    console.error(`Provider CLI release gate: ${error.message}`);
    process.exitCode = 1;
  }
}
