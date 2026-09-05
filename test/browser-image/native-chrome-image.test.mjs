import assert from "node:assert/strict";
import {
  createHash,
  generateKeyPairSync,
} from "node:crypto";
import { chmod, mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  buildAssetsLock,
  CAPABILITIES,
  extensionIDFromKey,
} from "./build-native-chrome-assets-lock.mjs";
import { deterministicZip } from "./deterministic-archive.mjs";
import { createCRX3 } from "./crx3.mjs";
import { verifyExtensionLock } from "./verify-native-chrome-extension-lock.mjs";

test("extension build input is pinned by exact ID, version and digest", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "native-extension-lock-"));
  const artifact = path.join(root, "extension.tar");
  const bytes = Buffer.from("locked extension");
  await writeFile(artifact, bytes);
  const lock = {
    activation_path: "/openlinker-runtime/index.html",
    extension_id: "abcdefghijklmnopabcdefghijklmnop",
    filename: "extension.tar",
    sha256: createHash("sha256").update(bytes).digest("hex"),
    source_reference: "authorized-build-input",
    version: "1.2.3.4",
  };
  const lockPath = path.join(root, "extension.lock.json");
  await writeFile(lockPath, JSON.stringify(lock));
  assert.deepEqual(
    await verifyExtensionLock(
      lockPath,
      artifact,
      lock.extension_id,
      lock.version,
    ),
    lock,
  );
  await writeFile(artifact, "tampered");
  await assert.rejects(
    verifyExtensionLock(lockPath, artifact, lock.extension_id, lock.version),
    /digest/,
  );
});

test("native Chrome Dockerfile has no runtime download or exposed control port", async () => {
  const dockerfile = await readFile(
    new URL("../../Dockerfile.browser.native-chrome", import.meta.url),
    "utf8",
  );
  for (const required of [
    "OPENLINKER_CHROME_ARTIFACT",
    "OPENLINKER_CHROME_UPSTREAM_ARTIFACT",
    "OPENLINKER_EXTENSION_ARTIFACT",
    "verify-native-chrome-extension-lock.mjs",
    "build-native-chrome-assets-lock.mjs",
    "/usr/share/chromium/extensions/${OPENLINKER_EXTENSION_ID}.json",
    "/etc/opt/chrome_for_testing/policies/managed/openlinker-native-chrome.json",
    "/etc/opt/chrome_for_testing/native-messaging-hosts/ai.openlinker.browser.json",
    "find /opt/openlinker/native-chrome/extension -type f -exec chmod 0444 {} +",
    "find /opt/openlinker/native-chrome/src -type d -exec chmod 0555 {} +",
    "find /opt/openlinker/native-chrome/src -type f -exec chmod 0444 {} +",
    "chmod 4755 /opt/google/chrome/chrome-sandbox",
	"test \"${TARGETARCH}\" = amd64",
    "USER 10001:10001",
  ]) {
    assert.match(dockerfile, new RegExp(required.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  for (const forbidden of [
    "clients2.google.com",
    "--load-extension",
    "--disable-extensions-except",
    "--no-sandbox",
    "--remote-debugging-port",
    "seccomp=unconfined",
    "EXPOSE ",
    "hehggadaopoacecdllhhajmbjkdcmajg",
  ]) {
    assert.equal(dockerfile.includes(forbidden), false, forbidden);
  }
});

test("native Chrome engine preserves JSON stdout across xvfb-run", async () => {
  const wrapper = await readFile(
    new URL("../../packages/browser-runtime/native-chrome/bin/openlinker-native-chrome-engine", import.meta.url),
    "utf8",
  );
  assert.match(wrapper, /exec 3>&2/);
  assert.match(
    wrapper,
    /exec \/usr\/bin\/node \/opt\/openlinker\/browser-engine\/dist\/main\.js 2>&3/,
  );
});

test("final runtime lock covers every installed native component", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "native-assets-lock-"));
  const chromeRoot = path.join(root, "chrome");
  const extensionRoot = path.join(root, "extension");
  await mkdir(chromeRoot);
  await mkdir(path.join(extensionRoot, "openlinker-runtime"), { recursive: true });
  const extensionInstallRoot = path.join(root, "chrome-extensions");
  const extensionPolicyRoot = path.join(root, "chrome-policies");
  await mkdir(extensionInstallRoot);
  await mkdir(extensionPolicyRoot);
  const generatedKey = generateKeyPairSync("rsa", {
    modulusLength: 2048,
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
    publicKeyEncoding: { type: "spki", format: "der" },
  });
  const extensionKey = generatedKey.publicKey.toString("base64");
  const extensionID = extensionIDFromKey(extensionKey);
  const paths = {
    chrome: path.join(chromeRoot, "chrome"),
    chromeSandbox: path.join(chromeRoot, "chrome-sandbox"),
    manifest: path.join(extensionRoot, "manifest.json"),
    crx: path.join(extensionRoot, "extension.crx"),
    extensionFile: path.join(extensionRoot, "worker.js"),
    activationFile: path.join(extensionRoot, "openlinker-runtime", "index.html"),
    host: path.join(root, "native-host"),
    hostSource: path.join(root, "native-host.mjs"),
    framing: path.join(root, "native-framing.mjs"),
    engine: path.join(root, "native-engine"),
    extensionInstallManifest: path.join(extensionInstallRoot, `${extensionID}.json`),
    extensionUpdateManifest: path.join(root, "extension-update.xml"),
    extensionPolicy: path.join(extensionPolicyRoot, "openlinker-native-chrome.json"),
    nativeManifest: path.join(root, "native-manifest.json"),
    output: path.join(root, "assets.lock.json"),
  };
  for (const file of [
    paths.chrome,
    paths.chromeSandbox,
    paths.extensionFile,
    paths.activationFile,
    paths.host,
    paths.hostSource,
    paths.framing,
    paths.engine,
  ]) {
    await writeFile(file, path.basename(file), { mode: 0o555 });
    await chmod(file, 0o555);
  }
  const manifestBytes = Buffer.from(JSON.stringify({
    manifest_version: 3,
    version: "1.2.3.4",
    key: extensionKey,
  }));
  await writeFile(paths.manifest, manifestBytes, { mode: 0o444 });
  await chmod(paths.manifest, 0o444);
  const crx = createCRX3(
    deterministicZip([
      { path: "manifest.json", data: manifestBytes },
      { path: "worker.js", data: Buffer.from("worker.js") },
      {
        path: "openlinker-runtime/index.html",
        data: Buffer.from("index.html"),
      },
    ]),
    generatedKey.privateKey,
  );
  await writeFile(paths.crx, crx, { mode: 0o444 });
  await chmod(paths.crx, 0o444);
  const chromeLockPath = path.join(root, "chrome.lock.json");
  const extensionLockPath = path.join(root, "extension.lock.json");
  await writeFile(
    chromeLockPath,
    JSON.stringify({
      distribution: "chrome_for_testing",
      version: "150.0.1.2",
    }),
  );
  await writeFile(
    extensionLockPath,
    JSON.stringify({
      extension_id: extensionID,
      version: "1.2.3.4",
      activation_path: "/openlinker-runtime/index.html",
    }),
  );
  const lock = await buildAssetsLock({
    chromeLockPath,
    extensionLockPath,
    chromeRoot,
    chromePath: paths.chrome,
    extensionRoot,
    extensionInstallManifestPath: paths.extensionInstallManifest,
    extensionUpdateManifestPath: paths.extensionUpdateManifest,
    extensionPolicyPath: paths.extensionPolicy,
    nativeHostPath: paths.host,
    nativeHostSourcePath: paths.hostSource,
    nativeFramingSourcePath: paths.framing,
    enginePath: paths.engine,
    nativeMessagingManifestPath: paths.nativeManifest,
    architecture: "amd64",
    nativeHostProtocol: "openlinker.native-chrome.v2",
    profileGeneration: 1,
    outputPath: paths.output,
  });
  assert.deepEqual(lock.capabilities, CAPABILITIES);
  assert.equal(lock.assets.some((asset) => asset.path === paths.chrome), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.manifest), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.crx), true);
  assert.equal(
    lock.assets.some((asset) => asset.path === paths.extensionInstallManifest),
    true,
  );
  assert.equal(
    lock.assets.some((asset) => asset.path === paths.extensionUpdateManifest),
    true,
  );
  assert.equal(lock.assets.some((asset) => asset.path === paths.extensionPolicy), true);
  assert.equal(lock.assets.some((asset) => asset.path === paths.nativeManifest), true);
  const policy = JSON.parse(await readFile(paths.extensionPolicy, "utf8"));
  assert.deepEqual(policy.ExtensionSettings, {
    "*": { installation_mode: "blocked" },
    [extensionID]: { installation_mode: "allowed" },
  });
  await chmod(paths.manifest, 0o644);
  await writeFile(
    paths.manifest,
    JSON.stringify({
      manifest_version: 3,
      version: "9.9.9.9",
      key: extensionKey,
    }),
  );
  await chmod(paths.manifest, 0o444);
  await assert.rejects(
    buildAssetsLock({
      chromeLockPath,
      extensionLockPath,
      chromeRoot,
      chromePath: paths.chrome,
      extensionRoot,
      extensionInstallManifestPath: paths.extensionInstallManifest,
      extensionUpdateManifestPath: paths.extensionUpdateManifest,
      extensionPolicyPath: paths.extensionPolicy,
      nativeHostPath: paths.host,
      nativeHostSourcePath: paths.hostSource,
      nativeFramingSourcePath: paths.framing,
      enginePath: paths.engine,
      nativeMessagingManifestPath: paths.nativeManifest,
      architecture: "amd64",
      nativeHostProtocol: "openlinker.native-chrome.v2",
      profileGeneration: 1,
      outputPath: paths.output,
    }),
    /manifest identity/,
  );
});
