import { cpSync, mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { buildPluginHost, platforms } from "./build-plugin-host.mjs";

const output = resolve(process.argv[2] ?? "dist/native");
const version = process.argv[3];
const hosts = join(output, "host");
for (const platform of platforms) buildPluginHost({ output: join(hosts, platform), platform, version });
for (const platform of ["codex", "claude"]) {
  const target = join(output, platform, "openlinker");
  mkdirSync(target, { recursive: true });
  cpSync(resolve(import.meta.dirname, `../platforms/${platform}/openlinker`), target, { recursive: true });
  cpSync(hosts, join(target, "host"), { recursive: true });
}
