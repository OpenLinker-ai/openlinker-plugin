#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lookup } from "node:dns/promises";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  rename,
  rm,
} from "node:fs/promises";
import { homedir, platform as hostPlatform, arch as hostArch, tmpdir } from "node:os";
import { basename, dirname, join, parse, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(scriptDir, "..");
const lockPath = join(pluginRoot, "cli-lock.json");
const planOnly = process.argv.slice(2).includes("--plan");

class InstallerError extends Error {
  constructor(message) {
    super(message);
    this.name = "InstallerError";
  }
}

function fail(message) {
  throw new InstallerError(message);
}

export function normalizePlatform() {
  const os = hostPlatform();
  const cpu = hostArch();
  const platform = os === "win32" ? "windows" : os;
  const arch = cpu === "x64" ? "amd64" : cpu;
  if (!["darwin", "linux", "windows"].includes(platform) || !["amd64", "arm64"].includes(arch)) {
    fail(`unsupported platform: ${os}/${cpu}`);
  }
  return { platform, arch, key: `${platform}-${arch}` };
}

export function defaultDataDir() {
  for (const name of ["OPENLINKER_PLUGIN_DATA", "PLUGIN_DATA", "CLAUDE_PLUGIN_DATA"]) {
    if (process.env[name]) return resolve(process.env[name]);
  }
  if (hostPlatform() === "win32") {
    const root = process.env.LOCALAPPDATA || join(homedir(), "AppData", "Local");
    return join(root, "OpenLinker", "Plugin");
  }
  if (hostPlatform() === "darwin") {
    return join(homedir(), "Library", "Application Support", "OpenLinker", "Plugin");
  }
  return join(process.env.XDG_DATA_HOME || join(homedir(), ".local", "share"), "openlinker", "plugin");
}

function validateLock(lock, allowFixture = false) {
  if (lock.schema_version !== 1 || typeof lock.version !== "string" || !lock.assets) {
    fail("cli-lock.json does not match schema version 1");
  }
  if (lock.test_fixture === true && !allowFixture) {
    fail("test fixture locks are disabled");
  }
  return lock;
}

async function readLock() {
  let raw;
  try {
    raw = await readFile(lockPath, "utf8");
  } catch (error) {
    if (error?.code === "ENOENT") {
      fail("cli-lock.json is not populated; publish and lock the compatible CLI release first");
    }
    throw error;
  }
  let lock;
  try {
    lock = JSON.parse(raw);
  } catch {
    fail("cli-lock.json is not valid JSON");
  }
  return validateLock(lock);
}

function validateAsset(lock, target) {
  const asset = lock.assets[target.key];
  if (!asset) fail(`cli-lock.json has no asset for ${target.key}`);
  for (const key of ["archive", "archive_url", "checksum_url", "sha256", "executable_path"]) {
    if (typeof asset[key] !== "string" || !asset[key]) fail(`invalid ${target.key}.${key} in cli-lock.json`);
  }
  if (!/^[a-f0-9]{64}$/.test(asset.sha256)) fail(`invalid SHA-256 for ${target.key}`);
  const expectedBinary = target.platform === "windows" ? "openlinker.exe" : "openlinker";
  if (basename(asset.executable_path) !== expectedBinary || asset.executable_path.includes("..")) {
    fail(`unsafe executable_path for ${target.key}`);
  }
  return asset;
}

function isPrivateIPv4(address) {
  const parts = address.split(".").map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return true;
  const [a, b] = parts;
  return a === 0 || a === 10 || a === 127 || (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) ||
    (a === 100 && b >= 64 && b <= 127) || a >= 224;
}

function isPrivateAddress(address) {
  const value = address.toLowerCase().split("%")[0];
  if (value.includes(".")) {
    const mapped = value.lastIndexOf(":");
    return isPrivateIPv4(mapped >= 0 ? value.slice(mapped + 1) : value);
  }
  return value === "::" || value === "::1" || value.startsWith("fc") || value.startsWith("fd") ||
    value.startsWith("fe8") || value.startsWith("fe9") || value.startsWith("fea") || value.startsWith("feb");
}

function isAllowedReleaseHost(hostname) {
  const host = hostname.toLowerCase();
  return host === "github.com" || host.endsWith(".githubusercontent.com") ||
    /^github-production-release-asset-[a-z0-9-]+\.s3\.amazonaws\.com$/.test(host);
}

async function assertSafeDownloadURL(rawURL) {
  let url;
  try {
    url = new URL(rawURL);
  } catch {
    fail(`invalid download URL: ${rawURL}`);
  }
  if (url.protocol !== "https:" || url.username || url.password || !isAllowedReleaseHost(url.hostname)) {
    fail(`download URL is outside the approved GitHub release hosts: ${url.origin}`);
  }
  let addresses;
  try {
    addresses = await lookup(url.hostname, { all: true, verbatim: true });
  } catch (error) {
    fail(`cannot resolve release host ${url.hostname}: ${error.message}`);
  }
  if (addresses.length === 0 || addresses.some(({ address }) => isPrivateAddress(address))) {
    fail(`release host resolved to a private or reserved address: ${url.hostname}`);
  }
  return url;
}

function parseResponseHeaders(raw) {
  const blocks = raw.replace(/\r\n/g, "\n").split(/\n\n+/).filter((block) => /^HTTP\//m.test(block));
  const block = blocks.at(-1) || "";
  const status = Number(block.match(/^HTTP\/\S+\s+(\d{3})/m)?.[1] || 0);
  const location = block.match(/^location:\s*(.+)$/im)?.[1]?.trim();
  return { status, location };
}

export async function download(rawURL, destination) {
  let current = await assertSafeDownloadURL(rawURL);
  for (let redirect = 0; redirect <= 5; redirect += 1) {
    const hopDir = await mkdtemp(join(tmpdir(), "openlinker-download-hop-"));
    const headers = join(hopDir, "headers");
    const body = join(hopDir, "body");
    const result = spawnSync("curl", [
      "--proto", "=https",
      "--proto-redir", "=https",
      "--silent",
      "--show-error",
      "--max-redirs", "0",
      "--connect-timeout", "20",
      "--max-time", "300",
      "--dump-header", headers,
      "--output", body,
      current.href,
    ], { encoding: "utf8" });
    const headerRaw = await readFile(headers, "utf8").catch(() => "");
    const { status, location } = parseResponseHeaders(headerRaw);
    if (status >= 200 && status < 300 && result.status === 0) {
      await rename(body, destination);
      await rm(hopDir, { recursive: true, force: true });
      return;
    }
    await rm(hopDir, { recursive: true, force: true });
    if (status >= 300 && status < 400 && location) {
      current = await assertSafeDownloadURL(new URL(location, current).href);
      continue;
    }
    fail(`download failed with HTTP ${status || "unknown"}: ${result.stderr.trim() || current.href}`);
  }
  fail("download exceeded five redirects");
}

export async function sha256(path) {
  const bytes = await readFile(path);
  return createHash("sha256").update(bytes).digest("hex");
}

export async function extractExecutable(archive, asset, target, destination) {
  const isZip = asset.archive.endsWith(".zip");
  const list = spawnSync(isZip ? "unzip" : "tar", isZip ? ["-Z1", archive] : ["-tzf", archive], { encoding: "utf8" });
  if (list.status !== 0) fail(`cannot inspect ${asset.archive}: ${list.stderr.trim()}`);
  const entries = list.stdout.split(/\r?\n/).filter(Boolean);
  if (entries.filter((entry) => entry === asset.executable_path).length !== 1) {
    fail(`archive does not contain exactly one ${asset.executable_path}`);
  }
  if (!isZip) {
    const verbose = spawnSync("tar", ["-tvzf", archive, asset.executable_path], { encoding: "utf8" });
    if (verbose.status !== 0 || !verbose.stdout.trimStart().startsWith("-")) {
      fail("archive executable entry is not a regular file");
    }
  }
  const handle = await open(destination, "wx", 0o700);
  try {
    const result = spawnSync(
      isZip ? "unzip" : "tar",
      isZip ? ["-p", archive, asset.executable_path] : ["-xOzf", archive, asset.executable_path],
      { stdio: ["ignore", handle.fd, "pipe"], encoding: "utf8" },
    );
    if (result.status !== 0) fail(`cannot extract CLI executable: ${result.stderr?.trim() || "archive command failed"}`);
  } finally {
    await handle.close();
  }
  if (target.platform !== "windows") await chmod(destination, 0o700);
}

export async function assertNoSymlinkPath(path) {
  const absolute = resolve(path);
  const root = parse(absolute).root;
  const relative = absolute.slice(root.length).split(/[\\/]+/).filter(Boolean);
  let current = root;
  for (const part of relative) {
    current = join(current, part);
    try {
      const stat = await lstat(current);
      if (stat.isSymbolicLink()) fail(`destination path contains a symlink: ${current}`);
    } catch (error) {
      if (error?.code === "ENOENT") return;
      throw error;
    }
  }
}

function normalizeVersion(value) {
  return String(value || "").replace(/^v/, "");
}

async function validateCandidate(path, lock) {
  const result = spawnSync(path, ["context"], { encoding: "utf8", env: process.env });
  if (result.status !== 0) fail("downloaded CLI failed its context self-check");
  let context;
  try {
    context = JSON.parse(result.stdout);
  } catch {
    fail("downloaded CLI returned invalid context JSON");
  }
  if (context.surface_version !== lock.surface_version || normalizeVersion(context.cli_version) !== normalizeVersion(lock.version)) {
    fail("downloaded CLI version or surface does not match cli-lock.json");
  }
  const capabilities = new Set(context.capabilities || []);
  for (const capability of lock.capabilities || []) {
    if (!capabilities.has(capability)) fail(`downloaded CLI lacks locked capability: ${capability}`);
  }
}

export async function installForTest(options = {}) {
  const lock = options.lock ? validateLock(options.lock, true) : await readLock();
  const target = options.target || normalizePlatform();
  const asset = validateAsset(lock, target);
  const dataDir = options.dataDir ? resolve(options.dataDir) : defaultDataDir();
  const downloader = options.downloader || download;
  const isPlanOnly = options.planOnly ?? planOnly;
  const binDir = join(dataDir, "bin");
  const binaryName = target.platform === "windows" ? "openlinker.exe" : "openlinker";
  const destination = join(binDir, binaryName);
  const plan = {
    version: lock.version,
    repository: lock.repository,
    release_url: lock.release_url,
    platform: target.key,
    archive: asset.archive,
    destination,
  };
  if (isPlanOnly) return plan;

  await assertNoSymlinkPath(dataDir);
  await mkdir(binDir, { recursive: true, mode: 0o700 });
  await assertNoSymlinkPath(binDir);
  await assertNoSymlinkPath(destination);
  try {
    const existing = await lstat(destination);
    if (!existing.isFile()) fail("existing CLI destination is not a regular file");
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }

  const workDir = await mkdtemp(join(tmpdir(), "openlinker-cli-install-"));
  const archive = join(workDir, asset.archive);
  const checksum = `${archive}.sha256`;
  const candidate = join(binDir, `.openlinker.new-${process.pid}`);
  const backup = join(binDir, `.openlinker.previous-${process.pid}`);
  let backedUp = false;
  try {
    await downloader(asset.archive_url, archive);
    await downloader(asset.checksum_url, checksum);
    const checksumText = await readFile(checksum, "utf8");
    const adjacentDigest = checksumText.match(/\b([a-fA-F0-9]{64})\b/)?.[1]?.toLowerCase();
    const actualDigest = await sha256(archive);
    if (adjacentDigest !== asset.sha256 || actualDigest !== asset.sha256) {
      fail("archive checksum does not match the adjacent checksum and pinned lock");
    }
    await extractExecutable(archive, asset, target, candidate);
    await validateCandidate(candidate, lock);
    try {
      await rename(destination, backup);
      backedUp = true;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    try {
      await rename(candidate, destination);
    } catch (error) {
      if (backedUp) await rename(backup, destination);
      throw error;
    }
    if (backedUp) await rm(backup, { force: true });
  } finally {
    await rm(candidate, { force: true });
    await rm(workDir, { recursive: true, force: true });
  }
  return { ...plan, installed: true };
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  if (process.argv.slice(2).some((arg) => arg !== "--plan")) {
    process.stderr.write("openlinker plugin installer: usage: install-openlinker-cli.mjs [--plan]\n");
    process.exitCode = 2;
  } else {
    await installForTest().then((result) => {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    }).catch((error) => {
      process.stderr.write(`openlinker plugin installer: ${error?.message || String(error)}\n`);
      process.exitCode = 2;
    });
  }
}
