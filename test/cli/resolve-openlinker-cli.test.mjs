import assert from "node:assert/strict";
import { chmod, mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";

const repoRoot = resolve(import.meta.dirname, "../..");
const resolver = join(repoRoot, "platforms/codex/openlinker/scripts/resolve-openlinker-cli");

async function fakeCLI(root, context) {
  const bin = join(root, "openlinker");
  await writeFile(
    bin,
    `#!/bin/sh
if [ "\${1}" = context ]; then
  cat <<'JSON'
${JSON.stringify(context, null, 2)}
JSON
  exit 0
fi
printf 'COMMAND:%s\n' "$*"
`,
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

const browserRoot = join(root, "browser");
await mkdir(browserRoot, { recursive: true });
const browserCompatible = await fakeCLI(browserRoot, {
  api_base: "https://api.openlinker.ai",
  cli_version: "0.2.0-rc.1",
  surface_version: "openlinker.cli.v1",
  capabilities: ["plugin.browser.serve"],
});
result = spawnSync(resolver, ["--require", "plugin.browser.serve"], {
  encoding: "utf8",
  env: { ...process.env, OPENLINKER_CLI_BIN: browserCompatible },
});
assert.equal(result.status, 0, result.stderr);
assert.equal(result.stdout.trim(), browserCompatible);

const launcher = join(
  repoRoot,
  "platforms/codex/openlinker/bin/openlinker-plugin",
);
result = spawnSync(
  launcher,
  ["plugin", "browser-proxy", "--host", "codex"],
  {
    encoding: "utf8",
    env: {
      ...process.env,
      OPENLINKER_CLI_BIN: browserCompatible,
      PLUGIN_ROOT: join(repoRoot, "platforms/codex/openlinker"),
    },
  },
);
assert.notEqual(result.status, 0, "native Plugin must not fall back to the old full CLI");
assert.match(result.stderr, /verified Plugin host unavailable/);

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
