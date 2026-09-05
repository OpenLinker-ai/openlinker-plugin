import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { access, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { downloadPinnedCLI, validateAsset, verifyBuildInfo } from "../scripts/download-pinned-cli.mjs";

const pluginModule = "github.com/OpenLinker-ai/openlinker-plugin";
const sdkModule = "github.com/OpenLinker-ai/openlinker-go";
const checksum = `h1:${Buffer.alloc(32, 7).toString("base64")}`;
const revision = "0123456789abcdef0123456789abcdef01234567";
const pluginVersion = "v0.1.58-0.20260905000000-0123456789ab";
const sdkVersion = "v0.2.0-rc7";
const buildInfo = [
  "/tmp/openlinker: go1.26.4",
  "\tpath\tgithub.com/OpenLinker-ai/openlinker-cli/cmd/openlinker",
  `\tdep\t${pluginModule}\t${pluginVersion}\t${checksum}`,
  `\tdep\t${sdkModule}\t${sdkVersion}\t${checksum}`,
  "\tbuild\tGOOS=linux",
  "\tbuild\tGOARCH=amd64",
  "\tbuild\tvcs=git",
  `\tbuild\tvcs.revision=${revision}`,
  "\tbuild\tvcs.modified=false",
].join("\n");

function lockFor(bytes) {
  const version = "v0.2.0-rc.6";
  const archiveRoot = `openlinker-cli-${version}-linux-amd64`;
  const archive = `${archiveRoot}.tar.gz`;
  return {
    schema_version: 1,
    repository: "OpenLinker-ai/openlinker-cli",
    surface_version: "openlinker.cli.v1",
    version,
    assets: {
      "linux-amd64": {
        archive,
        archive_url: `https://github.com/OpenLinker-ai/openlinker-cli/releases/download/${version}/${archive}`,
        executable_path: `${archiveRoot}/openlinker`,
        sha256: createHash("sha256").update(bytes).digest("hex"),
      },
    },
  };
}

test("build-info verification returns exact immutable Plugin, SDK, and CLI source evidence", () => {
  assert.deepEqual(verifyBuildInfo(buildInfo, "linux-amd64", sdkVersion), {
    cli_commit: revision,
    plugin_module_version: pluginVersion,
    openlinker_go_version: sdkVersion,
  });
  assert.deepEqual(verifyBuildInfo(buildInfo.replace("GOARCH=amd64", "GOARCH=arm64"), "linux-arm64", sdkVersion), {
    cli_commit: revision,
    plugin_module_version: pluginVersion,
    openlinker_go_version: sdkVersion,
  });
});

test("incompatible, replaced, unchecksummed, wrong-target, and dirty CLI builds fail closed", async (t) => {
  const mutations = [
    ["legacy CLI without Plugin", (info) => info.split("\n").filter((line) => !line.includes(pluginModule)).join("\n"), /missing.*openlinker-plugin/],
    ["missing SDK", (info) => info.split("\n").filter((line) => !line.includes(sdkModule)).join("\n"), /missing.*openlinker-go/],
    ["different SDK", (info) => info.replace(sdkVersion, "v0.2.0-rc5"), /SDK versions differ/],
    ["replacement", (info) => `${info}\n\t=>\t../openlinker-plugin\t(devel)`, /replaced module/],
    ["development version", (info) => info.replace(pluginVersion, "(devel)"), /not immutable/],
    ["placeholder Plugin", (info) => info.replace(pluginVersion, "v0.0.0"), /placeholder/],
    ["placeholder SDK", (info) => info.replace(sdkVersion, "v0.0.0"), /placeholder/],
    ["missing checksum", (info) => info.replace(checksum, ""), /checksum missing/],
    ["malformed checksum", (info) => info.replace(checksum, "h1:garbage"), /checksum missing/],
    ["wrong OS", (info) => info.replace("GOOS=linux", "GOOS=darwin"), /GOOS differs/],
    ["wrong architecture", (info) => info.replace("GOARCH=amd64", "GOARCH=arm64"), /GOARCH differs/],
    ["missing revision", (info) => info.replace(`vcs.revision=${revision}`, "vcs.time=2026-09-05T00:00:00Z"), /vcs.revision/],
    ["abbreviated revision", (info) => info.replace(revision, revision.slice(0, 12)), /vcs.revision/],
    ["non-Git VCS", (info) => info.replace("vcs=git", "vcs=hg"), /Git source metadata/],
    ["dirty checkout", (info) => info.replace("vcs.modified=false", "vcs.modified=true"), /clean immutable checkout/],
    ["missing cleanliness metadata", (info) => info.replace("vcs.modified=false", ""), /clean immutable checkout/],
  ];
  for (const [name, mutate, message] of mutations) {
    await t.test(name, () => assert.throws(() => verifyBuildInfo(mutate(buildInfo), "linux-amd64", sdkVersion), message));
  }
});

test("asset validation pins repository, release, archive, executable, platform, and digest", async (t) => {
  const lock = lockFor(Buffer.from("fixture"));
  const asset = validateAsset(lock, "linux-amd64");
  assert.equal(asset.archiveRoot, "openlinker-cli-v0.2.0-rc.6-linux-amd64");
  const mutations = [
    ["fixture lock", (value) => { value.test_fixture = true; }],
    ["different repository", (value) => { value.repository = "someone/cli"; }],
    ["moving version", (value) => { value.version = "main"; }],
    ["different URL", (value) => { value.assets["linux-amd64"].archive_url = "https://example.invalid/cli.tar.gz"; }],
    ["different archive", (value) => { value.assets["linux-amd64"].archive = "cli.tar.gz"; }],
    ["escaped executable", (value) => { value.assets["linux-amd64"].executable_path = "../openlinker"; }],
    ["invalid hash", (value) => { value.assets["linux-amd64"].sha256 = "0"; }],
  ];
  for (const [name, mutate] of mutations) {
    await t.test(name, () => {
      const changed = structuredClone(lock);
      mutate(changed);
      assert.throws(() => validateAsset(changed, "linux-amd64"));
    });
  }
  assert.throws(() => validateAsset(lock, "darwin-arm64"), /supported Linux target/);
  assert.throws(() => validateAsset(lock, "linux-arm64"), /no linux-arm64 artifact/);
});

test("offline archive checksum rejection produces no executable or source metadata", async () => {
  const root = await mkdtemp(join(tmpdir(), "openlinker-cli-download-test-"));
  try {
    const bytes = Buffer.from("not an archive: checksum validation must happen first");
    const lockPath = join(root, "lock.json");
    const archivePath = join(root, "archive.tar.gz");
    const output = join(root, "out", "openlinker");
    await writeFile(lockPath, JSON.stringify(lockFor(Buffer.from("different bytes"))));
    await writeFile(archivePath, bytes);
    await assert.rejects(downloadPinnedCLI({ platform: "linux-amd64", output, lockPath, archivePath, expectedSDK: sdkVersion }), /SHA-256 differs/);
    await assert.rejects(access(output), { code: "ENOENT" });
    await assert.rejects(access(join(root, "out", "cli-build-info.json")), { code: "ENOENT" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("checksum-valid archives containing links are rejected before Go inspection or extraction", async () => {
  const root = await mkdtemp(join(tmpdir(), "openlinker-cli-download-test-"));
  try {
    const archiveRoot = validateAsset(lockFor(Buffer.from("unused")), "linux-amd64").archiveRoot;
    await mkdir(join(root, archiveRoot));
    await symlink("../outside", join(root, archiveRoot, "openlinker"));
    const archivePath = join(root, "archive.tar.gz");
    execFileSync("tar", ["-czf", archivePath, "-C", root, archiveRoot]);
    const lockPath = join(root, "lock.json");
    await writeFile(lockPath, JSON.stringify(lockFor(await readFile(archivePath))));
    const output = join(root, "out", "openlinker");
    await assert.rejects(downloadPinnedCLI({ platform: "linux-amd64", output, lockPath, archivePath, expectedSDK: sdkVersion }), /links or special files/);
    await assert.rejects(access(output), { code: "ENOENT" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
