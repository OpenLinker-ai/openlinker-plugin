#!/usr/bin/env node
// Plugin consumes generated RPC bindings from its immutable Node dependency.
// Schema refresh belongs to Node; this read-only consumer gate cannot rewrite
// a module cache or silently create another copy of the protocol generator.
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { resolveAgentNodeModule } from "./resolve-agent-node-module.mjs";

if (process.argv.length !== 3 || process.argv[2] !== "--check") {
  throw new Error("Plugin only supports --check; refresh Codex schemas in the Agent Node repository");
}
const module = resolveAgentNodeModule();
execFileSync(process.execPath, [join(module.directory, "scripts/generate-codex-rpc.mjs"), "--check"], {
  cwd: module.directory,
  stdio: "inherit",
  timeout: 60_000,
  env: { ...process.env, GOWORK: "off", GOFLAGS: "-mod=readonly" },
});
