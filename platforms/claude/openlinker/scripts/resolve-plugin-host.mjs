#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lstatSync, readFileSync } from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export function resolvePluginHost({ pluginRoot, platform = process.platform, arch = process.arch, explicit = process.env.OPENLINKER_PLUGIN_HOST_BIN, required = [] }) {
  const goos = { linux: "linux", darwin: "darwin", win32: "windows" }[platform];
  const goarch = { x64: "amd64", arm64: "arm64" }[arch];
  assert.ok(goos && goarch, "unsupported Plugin host platform");
  const target = `${goos}-${goarch}`;
  const binary = explicit || join(pluginRoot, "host", target, `openlinker-plugin-host${goos === "windows" ? ".exe" : ""}`);
  assert.ok(isAbsolute(binary), "OPENLINKER_PLUGIN_HOST_BIN must be absolute");
  const metadataPath = join(dirname(binary), "host-build-info.json");
  assert.ok(lstatSync(binary).isFile() && lstatSync(metadataPath).isFile(), "Plugin host and metadata must be regular files");
  const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
  assert.equal(metadata.source_contract_id, "openlinker.plugin-host-sources.v1");
  assert.equal(metadata.host_platform, target);
  assert.match(metadata.plugin_commit, /^[a-f0-9]{40}$/);
  assert.match(metadata.plugin_host_sha256, /^[a-f0-9]{64}$/);
  assert.equal(createHash("sha256").update(readFileSync(binary)).digest("hex"), metadata.plugin_host_sha256, "Plugin host checksum mismatch");
  const context = JSON.parse(execFileSync(binary, ["context"], { encoding: "utf8", timeout: 10000, maxBuffer: 65536, stdio: ["ignore", "pipe", "pipe"] }));
  assert.equal(context.surface_version, "openlinker.plugin-host.v1", "wrong executable surface");
  assert.equal(context.host_version, metadata.plugin_host_version);
  assert.equal(context.plugin_commit, metadata.plugin_commit);
  for (const capability of required) assert.ok(context.capabilities?.includes(capability), `Plugin host capability missing: ${capability}`);
  return binary;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2), required = [];
    while (args.length) {
      assert.equal(args.shift(), "--require");
      assert.ok(args[0], "--require needs a capability");
      required.push(args.shift());
    }
    console.log(resolvePluginHost({ pluginRoot: resolve(import.meta.dirname, ".."), required }));
  } catch (error) {
    console.error(`openlinker plugin: verified Plugin host unavailable (${error.message}); install the matching Plugin release package. No CLI fallback or runtime download is performed.`);
    process.exitCode = 3;
  }
}
