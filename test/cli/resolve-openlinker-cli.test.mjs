import assert from "node:assert/strict";
import { chmod, mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";

const repoRoot = resolve(import.meta.dirname, "../..");
const resolver = join(repoRoot, "plugins/openlinker/scripts/resolve-openlinker-cli");

async function fakeCLI(root, context) {
  const bin = join(root, "openlinker");
  await writeFile(
    bin,
    `#!/bin/sh\nif [ "\${1}" != context ]; then exit 9; fi\ncat <<'JSON'\n${JSON.stringify(context, null, 2)}\nJSON\n`,
  );
  await chmod(bin, 0o755);
  return bin;
}

const root = await mkdtemp(join(tmpdir(), "openlinker-resolver-test-"));
await mkdir(join(root, "data", "bin"), { recursive: true });

const compatible = await fakeCLI(root, {
  api_base: "https://api.openlinker.ai",
  cli_version: "0.2.0-rc.1",
  surface_version: "openlinker.cli.v1",
  capabilities: ["agents.search", "runs.async"],
});

let result = spawnSync(resolver, ["--require", "runs.async"], {
  encoding: "utf8",
  env: { ...process.env, OPENLINKER_CLI_BIN: compatible },
});
assert.equal(result.status, 0, result.stderr);
assert.equal(result.stdout.trim(), compatible);

result = spawnSync(resolver, ["--require", "runs.cancel"], {
  encoding: "utf8",
  env: { ...process.env, OPENLINKER_CLI_BIN: compatible },
});
assert.equal(result.status, 5);
assert.match(result.stderr, /capability missing: runs\.cancel/);

const incompatible = await fakeCLI(join(root, "data", "bin"), {
  cli_version: "0.1.42",
  surface_version: "openlinker.cli.v0",
  capabilities: [],
});
result = spawnSync(resolver, [], {
  encoding: "utf8",
  env: { ...process.env, OPENLINKER_CLI_BIN: incompatible },
});
assert.equal(result.status, 4);
assert.match(result.stderr, /does not expose surface/);

result = spawnSync(resolver, [], {
  encoding: "utf8",
  env: {
    PATH: "/usr/bin:/bin",
    OPENLINKER_CLI_BIN: "",
    OPENLINKER_PLUGIN_DATA: join(root, "data"),
  },
});
assert.equal(result.status, 4);

console.log("resolver tests passed");
