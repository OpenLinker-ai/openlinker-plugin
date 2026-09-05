import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
} from "node:crypto";
import {
  chmod,
  lstat,
  mkdir,
  readFile,
  readdir,
  realpath,
  writeFile,
} from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

import {
  deterministicTar,
  deterministicZip,
  readZipEntries,
} from "./deterministic-archive.mjs";
import {
  createCRX3,
  extensionIDFromPublicKey,
  verifyCRX3,
} from "./crx3.mjs";

const PLUGIN_ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);
const EXTENSION_SOURCE = path.join(PLUGIN_ROOT, "packages", "browser-runtime", "native-chrome", "extension");
const CHROME_METADATA_URL =
  "https://googlechromelabs.github.io/chrome-for-testing/known-good-versions-with-downloads.json";
const EXTENSION_ACTIVATION_PATH = "/openlinker-runtime/index.html";
const BUILDER_VERSION = "openlinker-native-artifacts.v1";
const MAX_EXTENSION_SOURCE_BYTES = 64 * 1024 * 1024;
const MAX_DOWNLOAD_BYTES = 1024 * 1024 * 1024;

function isWithin(candidate, root) {
  const relative = path.relative(root, candidate);
  return relative !== "" && relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function commandStatus(command, args) {
  return spawnSync(command, args, { stdio: "ignore" }).status;
}

async function validateStateRoot(rootRepository, stateRoot) {
  if (!path.isAbsolute(rootRepository ?? "") || !path.isAbsolute(stateRoot ?? "")) {
    throw new Error("root repository and state root must be absolute paths");
  }
  const root = await realpath(rootRepository);
  const plugin = await realpath(PLUGIN_ROOT);
  if (!isWithin(plugin, root)) {
    throw new Error("openlinker-plugin must be inside the supplied root repository");
  }
  const stateBaseCandidate = path.join(root, ".openlinker-dev", "native-chrome");
  await mkdir(stateBaseCandidate, { recursive: true, mode: 0o700 });
  const stateBaseStatus = await lstat(stateBaseCandidate);
  if (stateBaseStatus.isSymbolicLink() || !stateBaseStatus.isDirectory()) {
    throw new Error("native Chrome state base must be a real directory");
  }
  const stateBase = await realpath(stateBaseCandidate);
  const normalizedStateRoot = path.resolve(stateRoot);
  if (!isWithin(normalizedStateRoot, stateBase) || isWithin(normalizedStateRoot, plugin)) {
    throw new Error("state root must stay under the root repository native Chrome state base and outside the submodule");
  }
  await mkdir(normalizedStateRoot, { recursive: true, mode: 0o700 });
  const stateStatus = await lstat(normalizedStateRoot);
  const state = await realpath(normalizedStateRoot);
  if (
    stateStatus.isSymbolicLink() ||
    !stateStatus.isDirectory() ||
    state !== normalizedStateRoot ||
    !isWithin(state, stateBase)
  ) {
    throw new Error("state root must not traverse a symlink");
  }
  await chmod(state, 0o700);
  const relative = path.relative(root, state);
  if (
    commandStatus("git", ["-C", root, "check-ignore", "-q", "--", relative]) !== 0
  ) {
    throw new Error("state root must be ignored by the root repository");
  }
  const dockerIgnore = await readFile(path.join(root, ".dockerignore"), "utf8");
  if (!dockerIgnore.split(/\r?\n/).includes(".openlinker-dev")) {
    throw new Error("root Docker context must ignore .openlinker-dev");
  }
  return { root, state, plugin };
}

function canonicalJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

async function collectExtensionFiles(root) {
  const files = [];
  let totalBytes = 0;
  const visit = async (directory, relativeRoot = "") => {
    const entries = await readdir(directory);
    entries.sort((left, right) => Buffer.compare(Buffer.from(left), Buffer.from(right)));
    for (const name of entries) {
      const candidate = path.join(directory, name);
      const relative = relativeRoot === "" ? name : `${relativeRoot}/${name}`;
      const status = await lstat(candidate);
      if (status.isSymbolicLink()) {
        throw new Error("extension source contains a symlink");
      }
      if (status.isDirectory()) {
        await visit(candidate, relative);
        continue;
      }
      if (!status.isFile() || (status.mode & 0o022) !== 0) {
        throw new Error("extension source contains an unsafe file");
      }
      const data = await readFile(candidate);
      totalBytes += data.length;
      if (totalBytes > MAX_EXTENSION_SOURCE_BYTES) {
        throw new Error("extension source is too large");
      }
      files.push({ path: relative, data });
    }
  };
  await visit(root);
  return files;
}

async function loadOrCreateSigningKey(state, root) {
  const signingRoot = path.join(state, "signing");
  await mkdir(signingRoot, { recursive: true, mode: 0o700 });
  const signingStatus = await lstat(signingRoot);
  if (signingStatus.isSymbolicLink() || !signingStatus.isDirectory()) {
    throw new Error("extension signing directory is invalid");
  }
  await chmod(signingRoot, 0o700);
  const keyPath = path.join(signingRoot, "openlinker-browser-extension.pem");
  let keyPEM;
  try {
    const status = await lstat(keyPath);
    if (
      status.isSymbolicLink() ||
      !status.isFile() ||
      (status.mode & 0o777) !== 0o600 ||
      status.uid !== process.getuid()
    ) {
      throw new Error("extension signing key permissions are invalid");
    }
    if ((await realpath(keyPath)) !== keyPath) {
      throw new Error("extension signing key must not traverse a symlink");
    }
    keyPEM = await readFile(keyPath, "utf8");
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
    const generated = generateKeyPairSync("rsa", {
      modulusLength: 3072,
      publicExponent: 0x10001,
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
      publicKeyEncoding: { type: "spki", format: "der" },
    });
    keyPEM = generated.privateKey;
    await writeFile(keyPath, keyPEM, { mode: 0o600, flag: "wx" });
    await chmod(keyPath, 0o600);
  }
  const keyRelative = path.relative(root, keyPath);
  if (
    commandStatus("git", ["-C", root, "ls-files", "--error-unmatch", "--", keyRelative]) === 0 ||
    isWithin(keyPath, PLUGIN_ROOT)
  ) {
    throw new Error("extension signing key must not be tracked or inside the submodule");
  }
  const privateKey = createPrivateKey(keyPEM);
  if (
    privateKey.asymmetricKeyType !== "rsa" ||
    (privateKey.asymmetricKeyDetails?.modulusLength ?? 0) < 3072
  ) {
    throw new Error("extension signing key must be at least RSA-3072");
  }
  const publicKeyDER = createPublicKey(privateKey).export({ type: "spki", format: "der" });
  return { keyPath, keyPEM, publicKeyDER };
}

function extensionTarEntries(files, crx) {
  const directories = new Set(["extension/"]);
  for (const file of files) {
    const parts = file.path.split("/");
    for (let index = 1; index < parts.length; index += 1) {
      directories.add(`extension/${parts.slice(0, index).join("/")}/`);
    }
  }
  return [
    ...[...directories].map((entryPath) => ({
      path: entryPath,
      type: "directory",
      mode: 0o555,
    })),
    ...files.map((file) => ({
      path: `extension/${file.path}`,
      type: "file",
      mode: 0o444,
      data: file.data,
    })),
    {
      path: "extension/extension.crx",
      type: "file",
      mode: 0o444,
      data: crx,
    },
  ];
}

async function prepareExtensionArtifacts(options) {
  const { root, state } = await validateStateRoot(
    options.rootRepository,
    options.stateRoot,
  );
  if (
    !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(options.version ?? "") ||
    typeof options.sourceReference !== "string" ||
    options.sourceReference.length < 1 ||
    options.sourceReference.length > 512
  ) {
    throw new Error("extension artifact identity is invalid");
  }
  const { keyPEM, publicKeyDER } = await loadOrCreateSigningKey(state, root);
  const extensionID = extensionIDFromPublicKey(publicKeyDER);
  const sourceFiles = await collectExtensionFiles(EXTENSION_SOURCE);
  const manifestIndex = sourceFiles.findIndex((file) => file.path === "manifest.json");
  if (manifestIndex < 0) throw new Error("extension source manifest is missing");
  const sourceManifest = JSON.parse(sourceFiles[manifestIndex].data.toString("utf8"));
  if (
    sourceManifest.manifest_version !== 3 ||
    sourceManifest.name !== "OpenLinker Browser Runtime" ||
    sourceManifest.action?.default_popup !== EXTENSION_ACTIVATION_PATH.slice(1) ||
    sourceManifest.key !== undefined
  ) {
    throw new Error("extension source manifest identity is invalid");
  }
  const manifest = {
    manifest_version: 3,
    name: sourceManifest.name,
    version: options.version,
    description: sourceManifest.description,
    key: publicKeyDER.toString("base64"),
    permissions: sourceManifest.permissions,
    background: sourceManifest.background,
    action: sourceManifest.action,
  };
  const files = sourceFiles.map((file, index) =>
    index === manifestIndex
      ? { path: file.path, data: Buffer.from(canonicalJSON(manifest)) }
      : file,
  );
  const zip = deterministicZip(files);
  const crx = createCRX3(zip, keyPEM);
  const verified = verifyCRX3(crx);
  if (
    verified.extensionID !== extensionID ||
    !verified.publicKeyDER.equals(publicKeyDER) ||
    !verified.zip.equals(zip)
  ) {
    throw new Error("generated CRX3 identity is invalid");
  }
  const tar = deterministicTar(extensionTarEntries(files, crx));
  const outputRoot = path.join(state, "artifacts", `extension-${options.version}`);
  await mkdir(outputRoot, { recursive: true, mode: 0o700 });
  await chmod(outputRoot, 0o700);
  const artifactPath = path.join(outputRoot, "openlinker-native-extension.tar");
  const crxPath = path.join(outputRoot, "openlinker-native-extension.crx");
  const lockPath = path.join(outputRoot, "openlinker-native-extension.lock.json");
  const publicKeyPath = path.join(outputRoot, "openlinker-browser-extension.spki.der");
  const lock = {
    activation_path: EXTENSION_ACTIVATION_PATH,
    extension_id: extensionID,
    filename: path.basename(artifactPath),
    sha256: createHash("sha256").update(tar).digest("hex"),
    source_reference: `${options.sourceReference};builder=${BUILDER_VERSION}`,
    version: options.version,
  };
  await writeFile(artifactPath, tar, { mode: 0o600 });
  await writeFile(crxPath, crx, { mode: 0o600 });
  await writeFile(publicKeyPath, publicKeyDER, { mode: 0o644 });
  await writeFile(lockPath, canonicalJSON(lock), { mode: 0o600 });
  return {
    artifactPath,
    crxPath,
    extensionID,
    lock,
    lockPath,
    publicKeyPath,
  };
}

async function boundedDownload(url, fetchImplementation = fetch) {
  const response = await fetchImplementation(url, { redirect: "error" });
  if (!response.ok) throw new Error(`artifact download failed with status ${response.status}`);
  const contentLength = Number(response.headers.get("content-length"));
  if (Number.isFinite(contentLength) && contentLength > MAX_DOWNLOAD_BYTES) {
    throw new Error("artifact download is too large");
  }
  const bytes = Buffer.from(await response.arrayBuffer());
  if (bytes.length < 1 || bytes.length > MAX_DOWNLOAD_BYTES) {
    throw new Error("artifact download size is invalid");
  }
  return bytes;
}

async function chromeDownloadForVersion(version, fetchImplementation = fetch) {
  const metadataBytes = await boundedDownload(CHROME_METADATA_URL, fetchImplementation);
  if (metadataBytes.length > 32 * 1024 * 1024) {
    throw new Error("Chrome for Testing metadata is too large");
  }
  const metadata = JSON.parse(metadataBytes.toString("utf8"));
  const record = metadata.versions?.find((candidate) => candidate.version === version);
  const download = record?.downloads?.chrome?.find(
    (candidate) => candidate.platform === "linux64",
  );
  const expectedURL = `https://storage.googleapis.com/chrome-for-testing-public/${version}/linux64/chrome-linux64.zip`;
  if (download?.url !== expectedURL) {
    throw new Error("official Chrome for Testing linux64 download is unavailable");
  }
  return expectedURL;
}

function normalizedChromeEntries(zipEntries) {
  const output = [];
  for (const entry of zipEntries) {
    const relative = entry.path.slice("chrome-linux64/".length);
    let normalized = relative === "" ? "chrome/" : `chrome/${relative}`;
    if (normalized === "chrome/chrome_sandbox") {
      normalized = "chrome/chrome-sandbox";
    }
    output.push(
      entry.isDirectory
        ? { path: normalized, type: "directory", mode: 0o555 }
        : {
            path: normalized,
            type: "file",
            mode: entry.executable ? 0o555 : 0o444,
            data: entry.data,
          },
    );
  }
  if (
    !output.some((entry) => entry.path === "chrome/chrome" && entry.mode === 0o555) ||
    !output.some((entry) => entry.path === "chrome/chrome-sandbox" && entry.mode === 0o555)
  ) {
    throw new Error("Chrome for Testing archive lacks required executables");
  }
  return output;
}

async function prepareChromeArtifacts(options) {
  const { state } = await validateStateRoot(
    options.rootRepository,
    options.stateRoot,
  );
  if (!/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){3}$/.test(options.version ?? "")) {
    throw new Error("Chrome for Testing version must contain four numeric parts");
  }
  const sourceReference = options.sourceReference ??
    await chromeDownloadForVersion(options.version, options.fetchImplementation);
  const expectedURL = `https://storage.googleapis.com/chrome-for-testing-public/${options.version}/linux64/chrome-linux64.zip`;
  if (sourceReference !== expectedURL) {
    throw new Error("Chrome for Testing source reference is not the official versioned URL");
  }
  const upstream = options.upstreamBytes ??
    await boundedDownload(sourceReference, options.fetchImplementation);
  const zipEntries = readZipEntries(upstream, "chrome-linux64/");
  const tar = deterministicTar(normalizedChromeEntries(zipEntries));
  const outputRoot = path.join(state, "artifacts", `chrome-${options.version}`);
  await mkdir(outputRoot, { recursive: true, mode: 0o700 });
  await chmod(outputRoot, 0o700);
  const artifactPath = path.join(outputRoot, "chrome-linux-amd64.tar");
  const upstreamPath = path.join(outputRoot, "chrome-linux64.zip");
  const lockPath = path.join(outputRoot, "chrome.lock.json");
  const lock = {
    distribution: "chrome_for_testing",
    version: options.version,
    filename: path.basename(artifactPath),
    source_reference: sourceReference,
    sha256: createHash("sha256").update(tar).digest("hex"),
    upstream_filename: path.basename(upstreamPath),
    upstream_sha256: createHash("sha256").update(upstream).digest("hex"),
  };
  await writeFile(artifactPath, tar, { mode: 0o600 });
  await writeFile(upstreamPath, upstream, { mode: 0o600 });
  await writeFile(lockPath, canonicalJSON(lock), { mode: 0o600 });
  return { artifactPath, lock, lockPath, upstreamPath };
}

function parseArguments(argv) {
  const [command, ...rest] = argv;
  if (!new Set(["extension", "chrome"]).has(command)) {
    throw new Error("artifact preparation command must be extension or chrome");
  }
  const values = {};
  for (let index = 0; index < rest.length; index += 2) {
    const name = rest[index];
    const value = rest[index + 1];
    if (!name?.startsWith("--") || value === undefined || values[name] !== undefined) {
      throw new Error("artifact preparation arguments are invalid");
    }
    values[name] = value;
  }
  const allowed = new Set([
    "--root-repository",
    "--state-root",
    "--version",
    "--source-reference",
  ]);
  if (Object.keys(values).some((name) => !allowed.has(name))) {
    throw new Error("artifact preparation argument is unknown");
  }
  for (const required of ["--root-repository", "--state-root", "--version"]) {
    if (values[required] === undefined) throw new Error(`${required} is required`);
  }
  if (command === "extension" && values["--source-reference"] === undefined) {
    throw new Error("--source-reference is required for extension preparation");
  }
  if (command === "chrome" && values["--source-reference"] !== undefined) {
    throw new Error("Chrome source reference is resolved from official metadata");
  }
  return { command, values };
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  const { command, values } = parseArguments(process.argv.slice(2));
  const common = {
    rootRepository: values["--root-repository"],
    stateRoot: values["--state-root"],
    version: values["--version"],
  };
  const result = command === "extension"
    ? await prepareExtensionArtifacts({
        ...common,
        sourceReference: values["--source-reference"],
      })
    : await prepareChromeArtifacts(common);
  process.stdout.write(`${JSON.stringify({
    artifact_path: result.artifactPath,
    extension_id: result.extensionID,
    lock_path: result.lockPath,
    upstream_path: result.upstreamPath,
  })}\n`);
}

export {
  BUILDER_VERSION,
  CHROME_METADATA_URL,
  EXTENSION_ACTIVATION_PATH,
  chromeDownloadForVersion,
  parseArguments,
  prepareChromeArtifacts,
  prepareExtensionArtifacts,
  validateStateRoot,
};
