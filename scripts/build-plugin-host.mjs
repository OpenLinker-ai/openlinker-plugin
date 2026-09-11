import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(import.meta.dirname, "..");
const modulePath = "github.com/OpenLinker-ai/openlinker-plugin";
export const platforms = ["linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64"];
const env = { ...process.env, GOWORK: "off", CGO_ENABLED: "0" };
const run = (args, environment = env) => execFileSync("go", args, { cwd: root, env: environment, encoding: "utf8" });

export function verifyHostBuildInfo(info, platform, sdkVersion, nodeVersion) {
  assert.ok(platforms.includes(platform), "unsupported Plugin host platform");
  const lines = info.split(/\r?\n/).map(line => line.trim().split(/\s+/));
  assert.ok(lines.some(line => line[0] === "path" && line[1] === `${modulePath}/cmd/openlinker-plugin-host`), "wrong host entry point");
  assert.ok(!lines.some(line => line[0] === "=>"), "replaced host dependency");
  assert.ok(!lines.some(line => line[1]?.startsWith("github.com/OpenLinker-ai/openlinker-cli")), "host must not depend on CLI");
  for (const [name, version] of [["openlinker-go", sdkVersion], ["openlinker-agent-node", nodeVersion]]) {
    const dependency = lines.find(line => line[0] === "dep" && line[1] === `github.com/OpenLinker-ai/${name}`);
    assert.ok(dependency, `missing ${name} dependency`);
    assert.equal(dependency[2], version, `wrong ${name} version`);
    assert.match(dependency[3] ?? "", /^h1:[A-Za-z0-9+/]{43}=$/, `missing ${name} checksum`);
  }
  for (const [key, value] of [["GOOS", platform.split("-")[0]], ["GOARCH", platform.split("-")[1]]]) {
    assert.ok(lines.some(line => line[0] === "build" && line[1] === `${key}=${value}`), `wrong ${key}`);
  }
}

export function buildPluginHost({ output, platform, sourceCommit, version }) {
  assert.ok(platforms.includes(platform), "unsupported Plugin host platform");
  if (existsSync(join(root, ".git"))) {
    const revision = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
    if (sourceCommit) assert.equal(sourceCommit, revision, "source commit differs from checkout");
    sourceCommit = revision;
    assert.equal(execFileSync("git", ["status", "--porcelain", "--untracked-files=all"], { cwd: root, encoding: "utf8" }).trim(), "", "commit host sources before packaging");
  }
  // Image builds use git-archive contexts. Their caller binds this commit to
  // the archived checkout; it is not represented as compiler vcs evidence.
  assert.match(sourceCommit, /^[a-f0-9]{40}$/);
  version ??= `sha-${sourceCommit.slice(0, 12)}`;
  assert.match(version, /^(?:v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?|sha-[a-f0-9]{12})$/);
  const manifest = JSON.parse(run(["mod", "edit", "-json"]));
  assert.equal(manifest.Module.Path, modulePath);
  assert.ok(!manifest.Replace?.length, "release module has replacements");
  const pin = name => manifest.Require.find(entry => entry.Path === `github.com/OpenLinker-ai/${name}`)?.Version;
  const sdkVersion = pin("openlinker-go"), nodeVersion = pin("openlinker-agent-node");
  for (const value of [sdkVersion, nodeVersion]) assert.match(value ?? "", /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/);
  run(["mod", "verify"]);
  mkdirSync(output, { recursive: true });
  const binary = join(output, `openlinker-plugin-host${platform.startsWith("windows-") ? ".exe" : ""}`);
  run(["build", "-mod=readonly", "-buildvcs=false", "-trimpath", `-ldflags=-s -w -X ${modulePath}/internal/pluginhost/buildinfo.Version=${version} -X ${modulePath}/internal/pluginhost/buildinfo.Revision=${sourceCommit}`, "-o", binary, "./cmd/openlinker-plugin-host"], { ...env, GOOS: platform.split("-")[0], GOARCH: platform.split("-")[1] });
  const info = run(["version", "-m", binary]);
  verifyHostBuildInfo(info, platform, sdkVersion, nodeVersion);
  const metadata = { source_contract_id: "openlinker.plugin-host-sources.v1", plugin_commit: sourceCommit, plugin_host_version: version, plugin_host_sha256: createHash("sha256").update(readFileSync(binary)).digest("hex"), openlinker_go_version: sdkVersion, agent_node_version: nodeVersion, host_platform: platform };
  writeFileSync(join(output, "host-build-info.json"), `${JSON.stringify(metadata, null, 2)}\n`, { mode: 0o444 });
  writeFileSync(join(output, "build-info.txt"), info, { mode: 0o444 });
  return metadata;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const value = flag => args.includes(flag) ? args[args.indexOf(flag) + 1] : undefined;
  assert.ok(value("--out") && value("--platform"), "use --out PATH --platform OS-ARCH [--source-commit HASH] [--version VERSION]");
  buildPluginHost({ output: resolve(value("--out")), platform: value("--platform"), sourceCommit: value("--source-commit"), version: value("--version") });
}
