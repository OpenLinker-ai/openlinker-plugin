import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { buildPluginHost, platforms } from "./build-plugin-host.mjs";

const version = process.argv[2];
assert.match(version ?? "", /^v\d+\.\d+\.\d+(?:-rc\.\d+)?$/);
const output = resolve(process.argv[3] ?? "dist/plugin-host-release");
mkdirSync(output, { recursive: true });
for (const platform of platforms) {
  const directory = join(output, "build", platform);
  buildPluginHost({ output: directory, platform, version });
  const name = `openlinker-plugin-host-${version}-${platform}.tar.gz`;
  execFileSync("tar", ["-czf", join(output, name), "-C", directory, `openlinker-plugin-host${platform.startsWith("windows-") ? ".exe" : ""}`, "host-build-info.json", "build-info.txt"]);
  const digest = createHash("sha256").update(readFileSync(join(output, name))).digest("hex");
  writeFileSync(join(output, `${name}.sha256`), `${digest}  ${name}\n`);
}
