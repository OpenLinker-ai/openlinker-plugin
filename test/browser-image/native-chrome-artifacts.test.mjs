import assert from "node:assert/strict";
import { chmod, cp, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import path from "node:path";
import test, { after } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { deterministicTar, deterministicZip, readZipEntries } from "./deterministic-archive.mjs";
import { verifyCRX3 } from "./crx3.mjs";
import { verifyChromeLock } from "./verify-chrome-lock.mjs";
import { verifyExtensionLock } from "./verify-native-chrome-extension-lock.mjs";

// Exercise the real root/submodule boundary in a disposable repository. The
// public Plugin checkout must not depend on a private parent repository in CI.
const rootRepository = await realpath(await mkdtemp(path.join(tmpdir(), "openlinker-native-artifacts-")));
after(() => rm(rootRepository, { recursive: true, force: true }));
execFileSync("git", ["init", "--quiet", rootRepository]);
await writeFile(path.join(rootRepository, ".gitignore"), ".openlinker-dev/\n");
await writeFile(path.join(rootRepository, ".dockerignore"), ".openlinker-dev\n");
const sourceRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const fixturePlugin = path.join(rootRepository, "openlinker-plugin");
const fixtureTools = path.join(fixturePlugin, "test", "browser-image");
await mkdir(fixtureTools, { recursive: true });
for (const name of ["prepare-native-chrome-artifacts.mjs", "deterministic-archive.mjs", "crx3.mjs"]) {
  await cp(path.join(sourceRoot, "test", "browser-image", name), path.join(fixtureTools, name));
}
await cp(
  path.join(sourceRoot, "packages", "browser-runtime", "native-chrome", "extension"),
  path.join(fixturePlugin, "packages", "browser-runtime", "native-chrome", "extension"),
  { recursive: true },
);
const { parseArguments, prepareChromeArtifacts, prepareExtensionArtifacts, validateStateRoot } =
  await import(pathToFileURL(path.join(fixtureTools, "prepare-native-chrome-artifacts.mjs")).href);
const stateBase = path.join(
  rootRepository,
  ".openlinker-dev",
  "native-chrome",
);

async function testStateRoot(prefix) {
  await mkdir(stateBase, { recursive: true });
  return mkdtemp(path.join(stateBase, prefix));
}

test("deterministic ZIP/tar encoders are stable and reject unsafe paths", () => {
  const zipFiles = [
    { path: "root/", type: "directory" },
    { path: "root/a.txt", data: Buffer.from("a") },
    { path: "root/tool", data: Buffer.from("tool"), executable: true },
  ];
  const firstZip = deterministicZip(zipFiles);
  const secondZip = deterministicZip([...zipFiles].reverse());
  assert.deepEqual(firstZip, secondZip);
  assert.deepEqual(
    readZipEntries(firstZip, "root/").map((entry) => [
      entry.path,
      entry.executable,
    ]),
    [
      ["root/", false],
      ["root/a.txt", false],
      ["root/tool", true],
    ],
  );
  assert.equal(
    readZipEntries(
      deterministicZip([{ path: "root/no-explicit-root", data: Buffer.from("x") }]),
      "root/",
    )[0].path,
    "root/no-explicit-root",
  );
  assert.throws(
    () => deterministicZip([{ path: "../escape", data: Buffer.alloc(0) }]),
    /entry/,
  );

  const storedWithDeflateHint = Buffer.from(firstZip);
  storedWithDeflateHint.writeUInt16LE(0x0004, 6);
  const centralOffset = storedWithDeflateHint.readUInt32LE(
    storedWithDeflateHint.length - 22 + 16,
  );
  storedWithDeflateHint.writeUInt16LE(0x0004, centralOffset + 8);
  assert.doesNotThrow(() => readZipEntries(storedWithDeflateHint, "root/"));
  const encrypted = Buffer.from(firstZip);
  encrypted.writeUInt16LE(0x0001, 6);
  encrypted.writeUInt16LE(0x0001, centralOffset + 8);
  assert.throws(() => readZipEntries(encrypted, "root/"), /features/);

  const tarEntries = [
    { path: "root/", type: "directory", mode: 0o555 },
    { path: "root/a.txt", type: "file", mode: 0o444, data: Buffer.from("a") },
  ];
  assert.deepEqual(
    deterministicTar(tarEntries),
    deterministicTar([...tarEntries].reverse()),
  );
  assert.throws(
    () => deterministicTar([{ path: "/escape", type: "directory", mode: 0o555 }]),
    /entry/,
  );
});

test("extension preparation keeps its RSA key outside the submodule and is reproducible", async () => {
  const stateRoot = await testStateRoot("artifact-extension-");
  try {
    const options = {
      rootRepository,
      stateRoot,
      version: "1.2.3.4",
      sourceReference: "git:1111111111111111111111111111111111111111",
    };
    const first = await prepareExtensionArtifacts(options);
    const second = await prepareExtensionArtifacts(options);
    assert.equal(first.extensionID, second.extensionID);
    assert.equal(first.lock.sha256, second.lock.sha256);
    assert.deepEqual(await readFile(first.crxPath), await readFile(second.crxPath));
    assert.equal(first.lock.activation_path, "/openlinker-runtime/index.html");
    assert.equal(
      (await verifyCRX3(await readFile(first.crxPath))).extensionID,
      first.extensionID,
    );
    assert.deepEqual(
      await verifyExtensionLock(
        first.lockPath,
        first.artifactPath,
        first.extensionID,
        options.version,
      ),
      first.lock,
    );
    const keyPath = path.join(
      stateRoot,
      "signing",
      "openlinker-browser-extension.pem",
    );
    assert.equal((await readFile(keyPath, "utf8")).includes("PRIVATE KEY"), true);
    assert.equal(
      (await readFile(first.artifactPath)).includes(Buffer.from("PRIVATE KEY")),
      false,
    );
    await chmod(keyPath, 0o644);
    await assert.rejects(prepareExtensionArtifacts(options), /permissions/);
  } finally {
    await rm(stateRoot, { recursive: true, force: true });
  }
});

test("Chrome preparation locks upstream ZIP and deterministic normalized tar", async () => {
  const stateRoot = await testStateRoot("artifact-chrome-");
  const version = "151.0.7922.77";
  const sourceReference =
    `https://storage.googleapis.com/chrome-for-testing-public/${version}/linux64/chrome-linux64.zip`;
  const upstreamBytes = deterministicZip([
    { path: "chrome-linux64/", type: "directory" },
    {
      path: "chrome-linux64/ABOUT",
      data: Buffer.from("Chrome for Testing"),
    },
    {
      path: "chrome-linux64/chrome",
      data: Buffer.from("chrome"),
      executable: true,
    },
    {
      path: "chrome-linux64/chrome_sandbox",
      data: Buffer.from("sandbox"),
      executable: true,
    },
  ]);
  try {
    const options = {
      rootRepository,
      stateRoot,
      version,
      sourceReference,
      upstreamBytes,
    };
    const first = await prepareChromeArtifacts(options);
    const second = await prepareChromeArtifacts(options);
    assert.equal(first.lock.upstream_sha256, second.lock.upstream_sha256);
    assert.equal(first.lock.sha256, second.lock.sha256);
    assert.deepEqual(
      await verifyChromeLock(
        first.lockPath,
        first.artifactPath,
        first.upstreamPath,
        "chrome_for_testing",
        version,
      ),
      first.lock,
    );
    await writeFile(first.upstreamPath, "tampered");
    await assert.rejects(
      verifyChromeLock(
        first.lockPath,
        first.artifactPath,
        first.upstreamPath,
        "chrome_for_testing",
        version,
      ),
      /upstream/,
    );
  } finally {
    await rm(stateRoot, { recursive: true, force: true });
  }
});

test("artifact CLI has no relative state-root fallback", async () => {
  assert.throws(
    () =>
      parseArguments([
        "extension",
        "--root-repository",
        rootRepository,
        "--version",
        "1.2.3.4",
        "--source-reference",
        "git:abc",
      ]),
    /state-root/,
  );
  await assert.rejects(
    validateStateRoot(rootRepository, ".openlinker-dev/native-chrome/twv1"),
    /absolute/,
  );
});
