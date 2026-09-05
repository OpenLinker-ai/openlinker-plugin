import { createHash } from "node:crypto";
import { chmod, lstat, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { readZipEntries } from "./deterministic-archive.mjs";
import { verifyCRX3 } from "./crx3.mjs";

const CAPABILITIES = [
  "act",
  "back",
  "batch",
  "checkpoint",
  "click",
  "close",
  "forward",
  "full",
  "keypress",
  "navigate",
  "observe",
  "ops_observe_frame",
  "ops_observe_status",
  "policy_evidence",
  "restricted",
  "screenshot",
  "scroll",
  "select",
  "semantic",
  "type_non_secret",
  "wait",
];
const NATIVE_MESSAGING_HOST_NAME = "ai.openlinker.browser";

async function regularFiles(root) {
  const result = [];
  const visit = async (candidate) => {
    const status = await lstat(candidate);
    if (status.isSymbolicLink()) throw new Error("native Chrome assets contain a symlink");
    if (status.isFile()) {
      result.push(candidate);
      return;
    }
    if (!status.isDirectory()) {
      throw new Error("native Chrome assets contain a special file");
    }
    const entries = await readdir(candidate);
    entries.sort();
    for (const entry of entries) await visit(path.join(candidate, entry));
  };
  await visit(root);
  return result;
}

function extensionIDFromKey(key) {
  if (
    typeof key !== "string" ||
    key.length < 16 ||
    key.length > 32 * 1024 ||
    key.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(key)
  ) {
    throw new Error("native Chrome extension manifest key is invalid");
  }
  const decoded = Buffer.from(key, "base64");
  if (decoded.length < 16 || decoded.length > 16 * 1024) {
    throw new Error("native Chrome extension manifest key is invalid");
  }
  const digest = createHash("sha256").update(decoded).digest().subarray(0, 16);
  let id = "";
  for (const byte of digest) {
    id += String.fromCharCode(97 + (byte >> 4), 97 + (byte & 0x0f));
  }
  return id;
}

async function validateExtensionManifest(options, extensionLock) {
  const manifestPath = path.join(options.extensionRoot, "manifest.json");
  const raw = await readFile(manifestPath, "utf8");
  if (Buffer.byteLength(raw) > 1024 * 1024) {
    throw new Error("native Chrome extension manifest is too large");
  }
  const manifest = JSON.parse(raw);
  if (
    manifest === null ||
    typeof manifest !== "object" ||
    Array.isArray(manifest) ||
    manifest.manifest_version !== 3 ||
    manifest.version !== extensionLock.version ||
    extensionIDFromKey(manifest.key) !== extensionLock.extension_id
  ) {
    throw new Error("native Chrome extension manifest identity is invalid");
  }
  const activationPath = extensionLock.activation_path.split("?", 1)[0];
  if (
    !/^\/[A-Za-z0-9._~!$&'()*+,;=:@%/-]+$/.test(activationPath) ||
    activationPath.includes("..")
  ) {
    throw new Error("native Chrome extension activation path is invalid");
  }
  const activationFile = path.join(options.extensionRoot, activationPath.slice(1));
  const activationStatus = await lstat(activationFile);
  if (!activationStatus.isFile() || activationStatus.isSymbolicLink()) {
    throw new Error("native Chrome extension activation file is invalid");
  }
  return manifest;
}

async function validateExtensionCRX(extensionRoot, extensionLock, manifest) {
  const crxPath = path.join(extensionRoot, "extension.crx");
  const status = await lstat(crxPath);
  if (
    !status.isFile() ||
    status.isSymbolicLink() ||
    status.size < 16 ||
    status.size > 512 * 1024 * 1024
  ) {
    throw new Error("native Chrome extension CRX is invalid");
  }
  const verified = verifyCRX3(await readFile(crxPath));
  if (
    verified.extensionID !== extensionLock.extension_id ||
    verified.publicKeyDER.toString("base64") !== manifest.key
  ) {
    throw new Error("native Chrome extension CRX identity is invalid");
  }
  const zipEntries = readZipEntries(verified.zip, "");
  const zipFiles = new Map(
    zipEntries
      .filter((entry) => !entry.isDirectory)
      .map((entry) => [entry.path, entry.data]),
  );
  const unpacked = (await regularFiles(extensionRoot))
    .filter((file) => file !== crxPath);
  if (zipFiles.size !== unpacked.length) {
    throw new Error("native Chrome extension CRX payload is incomplete");
  }
  for (const file of unpacked) {
    const relative = path.relative(extensionRoot, file).split(path.sep).join("/");
    const archived = zipFiles.get(relative);
    if (archived === undefined || !archived.equals(await readFile(file))) {
      throw new Error("native Chrome extension CRX payload does not match unpacked files");
    }
  }
  return crxPath;
}

async function buildAssetsLock(options) {
  const chromeLock = JSON.parse(await readFile(options.chromeLockPath, "utf8"));
  const extensionLock = JSON.parse(
    await readFile(options.extensionLockPath, "utf8"),
  );
  const manifest = await validateExtensionManifest(options, extensionLock);
  const extensionCRXPath = await validateExtensionCRX(
    options.extensionRoot,
    extensionLock,
    manifest,
  );
  const extensionInstallManifest = {
    external_crx: extensionCRXPath,
    external_version: extensionLock.version,
  };
  await writeFile(
    options.extensionInstallManifestPath,
    JSON.stringify(extensionInstallManifest),
    { mode: 0o444 },
  );
  await chmod(options.extensionInstallManifestPath, 0o444);
  const extensionCRXURL = pathToFileURL(extensionCRXPath).href;
  const extensionUpdateManifest =
    '<?xml version="1.0" encoding="UTF-8"?>' +
    '<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">' +
    `<app appid="${extensionLock.extension_id}">` +
    `<updatecheck codebase="${extensionCRXURL}" version="${extensionLock.version}"/>` +
    "</app></gupdate>";
  await writeFile(
    options.extensionUpdateManifestPath,
    extensionUpdateManifest,
    { mode: 0o444 },
  );
  await chmod(options.extensionUpdateManifestPath, 0o444);
  const extensionPolicy = {
    ExtensionSettings: {
      "*": { installation_mode: "blocked" },
      [extensionLock.extension_id]: {
        installation_mode: "allowed",
      },
    },
  };
  await writeFile(
    options.extensionPolicyPath,
    JSON.stringify(extensionPolicy),
    { mode: 0o444 },
  );
  await chmod(options.extensionPolicyPath, 0o444);
  const nativeManifest = {
    name: NATIVE_MESSAGING_HOST_NAME,
    description: "OpenLinker Browser Runtime Native Messaging Host",
    path: options.nativeHostPath,
    type: "stdio",
    allowed_origins: [`chrome-extension://${extensionLock.extension_id}/`],
  };
  await writeFile(
    options.nativeMessagingManifestPath,
    JSON.stringify(nativeManifest),
    { mode: 0o444 },
  );
  await chmod(options.nativeMessagingManifestPath, 0o444);

  const chromeFiles = await regularFiles(options.chromeRoot);
  const extensionFiles = await regularFiles(options.extensionRoot);
  if (!chromeFiles.includes(options.chromePath)) {
    throw new Error("locked Chrome executable is outside the Chrome asset tree");
  }
  const paths = [...new Set([
    ...chromeFiles,
    ...extensionFiles,
    options.extensionInstallManifestPath,
    options.extensionUpdateManifestPath,
    options.extensionPolicyPath,
    options.nativeHostPath,
    options.nativeHostSourcePath,
    options.nativeFramingSourcePath,
    options.enginePath,
    options.nativeMessagingManifestPath,
  ])];
  paths.sort();
  if (paths.length < 8 || paths.length > 4096) {
    throw new Error("native Chrome asset count is invalid");
  }
  const assets = [];
  let totalAssetBytes = 0;
  for (const assetPath of paths) {
    const status = await lstat(assetPath);
    totalAssetBytes += status.size;
    if (
      !status.isFile() ||
      status.isSymbolicLink() ||
      (status.mode & 0o022) !== 0 ||
      status.size > 1024 * 1024 * 1024 ||
      totalAssetBytes > 2 * 1024 * 1024 * 1024
    ) {
      throw new Error("native Chrome asset mode is invalid");
    }
    assets.push({
      path: assetPath,
      sha256: createHash("sha256")
        .update(await readFile(assetPath))
        .digest("hex"),
    });
  }
  const lock = {
    contract_id: "openlinker.native-chrome.assets.v1",
    platform: "linux",
    architecture: options.architecture,
    chrome_path: options.chromePath,
    chrome_distribution: chromeLock.distribution,
    chrome_version: chromeLock.version,
    extension_root: options.extensionRoot,
    extension_id: extensionLock.extension_id,
    extension_version: extensionLock.version,
    extension_activation_path: extensionLock.activation_path,
    extension_crx_path: extensionCRXPath,
    extension_install_manifest_path: options.extensionInstallManifestPath,
    extension_update_manifest_path: options.extensionUpdateManifestPath,
    extension_policy_path: options.extensionPolicyPath,
    native_host_path: options.nativeHostPath,
    native_host_protocol: options.nativeHostProtocol,
    native_messaging_manifest_path: options.nativeMessagingManifestPath,
    engine_path: options.enginePath,
    profile_generation: options.profileGeneration,
    capabilities: CAPABILITIES,
    assets,
  };
  await writeFile(options.outputPath, JSON.stringify(lock), { mode: 0o444 });
  await chmod(options.outputPath, 0o444);
  return lock;
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  const [
    chromeLockPath,
    extensionLockPath,
    chromeRoot,
    chromePath,
    extensionRoot,
    extensionInstallManifestPath,
    extensionUpdateManifestPath,
    extensionPolicyPath,
    nativeHostPath,
    nativeHostSourcePath,
    nativeFramingSourcePath,
    enginePath,
    nativeMessagingManifestPath,
    architecture,
    nativeHostProtocol,
    profileGenerationRaw,
    outputPath,
  ] = process.argv.slice(2);
  const profileGeneration = Number(profileGenerationRaw);
  if (
    [
      chromeLockPath,
      extensionLockPath,
      chromeRoot,
      chromePath,
      extensionRoot,
      extensionInstallManifestPath,
      extensionUpdateManifestPath,
      extensionPolicyPath,
      nativeHostPath,
      nativeHostSourcePath,
      nativeFramingSourcePath,
      enginePath,
      nativeMessagingManifestPath,
      outputPath,
    ].some((value) => value === undefined || !path.isAbsolute(value)) ||
    !["amd64", "arm64"].includes(architecture) ||
    !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(nativeHostProtocol ?? "") ||
    !Number.isSafeInteger(profileGeneration) ||
    profileGeneration < 1
  ) {
    throw new Error("native Chrome asset-lock builder arguments are invalid");
  }
  await buildAssetsLock({
    chromeLockPath,
    extensionLockPath,
    chromeRoot,
    chromePath,
    extensionRoot,
    extensionInstallManifestPath,
    extensionUpdateManifestPath,
    extensionPolicyPath,
    nativeHostPath,
    nativeHostSourcePath,
    nativeFramingSourcePath,
    enginePath,
    nativeMessagingManifestPath,
    architecture,
    nativeHostProtocol,
    profileGeneration,
    outputPath,
  });
}

export {
  buildAssetsLock,
  CAPABILITIES,
  NATIVE_MESSAGING_HOST_NAME,
  extensionIDFromKey,
};
