import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { verifyHostBuildInfo } from "../scripts/build-plugin-host.mjs";
import { resolvePluginHost } from "../platforms/codex/openlinker/scripts/resolve-plugin-host.mjs";

const revision = "1".repeat(40), sdk = "v0.2.0-rc8.0.20260908135527-31afbf9c1a18", node = "v0.1.57-rc.2";
const sum = `h1:${Buffer.alloc(32, 1).toString("base64")}`;
const info = `path github.com/OpenLinker-ai/openlinker-plugin/cmd/openlinker-plugin-host\ndep github.com/OpenLinker-ai/openlinker-go ${sdk} ${sum}\ndep github.com/OpenLinker-ai/openlinker-agent-node ${node} ${sum}\nbuild GOOS=linux\nbuild GOARCH=amd64\n`;

test("host provenance validates entry, module checksums, target and absence of CLI/replacements", () => {
  verifyHostBuildInfo(info, "linux-amd64", sdk, node);
  for (const changed of [info.replace("/cmd/openlinker-plugin-host", "/cmd/other"), info.replace(sdk, "v0.0.0"), info.replace(node, "v0.0.0"), info.replaceAll(sum, ""), info.replace("GOOS=linux", "GOOS=darwin"), info.replace("GOARCH=amd64", "GOARCH=arm64"), info + "=> ../sdk\n", info + "dep github.com/OpenLinker-ai/openlinker-cli v0.2.0 h1:bad\n"]) {
    assert.throws(() => verifyHostBuildInfo(changed, "linux-amd64", sdk, node));
  }
});

test("native launcher verifies packaged host and preserves args, stdin, stdout, stderr and exit code", { skip: process.platform === "win32" }, t => {
  const root = mkdtempSync(join(tmpdir(), "plugin host "));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const binary = join(root, "openlinker-plugin-host");
  const metadata = { source_contract_id: "openlinker.plugin-host-sources.v1", plugin_commit: revision, plugin_host_version: "v0.1.60", host_platform: `${process.platform}-${process.arch === "arm64" ? "arm64" : "amd64"}` };
  const context = { surface_version: "openlinker.plugin-host.v1", plugin_commit: revision, host_version: metadata.plugin_host_version, capabilities: ["plugin.serve", "plugin.browser.serve"] };
  writeFileSync(binary, `#!/bin/sh\nif [ "$1" = context ]; then\n  printf '%s\\n' '${JSON.stringify(context)}'\n  exit 0\nfi\nprintf '%s\\n' "$@"\ncat\nprintf 'host diagnostic\\n' >&2\nexit 23\n`);
  chmodSync(binary, 0o755);
  metadata.plugin_host_sha256 = createHash("sha256").update(readFileSync(binary)).digest("hex");
  writeFileSync(join(root, "host-build-info.json"), JSON.stringify(metadata));
  assert.equal(resolvePluginHost({ pluginRoot: root, explicit: binary, required: ["plugin.browser.serve"] }), binary);
  assert.throws(() => resolvePluginHost({ pluginRoot: root, explicit: binary, required: ["missing"] }));
  const launcher = resolve(import.meta.dirname, "../platforms/codex/openlinker/bin/openlinker-plugin");
  const result = spawnSync(launcher, ["plugin", "browser-proxy", "--host", "codex"], { encoding: "utf8", input: "input frame\n", env: { ...process.env, PLUGIN_ROOT: dirname(dirname(launcher)), OPENLINKER_PLUGIN_HOST_BIN: binary, OPENLINKER_CLI_BIN: "/nonexistent-cli" } });
  assert.equal(result.status, 23, result.stderr);
  assert.equal(result.stdout, "plugin\nbrowser-proxy\n--host\ncodex\ninput frame\n");
  assert.equal(result.stderr, "host diagnostic\n");
  writeFileSync(binary, "tampered");
  assert.throws(() => resolvePluginHost({ pluginRoot: root, explicit: binary }), /checksum mismatch/);
});

test("missing packaged host never falls back to a CLI or downloads a binary", () => {
  assert.throws(() => resolvePluginHost({ pluginRoot: "/nonexistent-plugin-root", explicit: "" }), /ENOENT/);
});
