import { execFileSync } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = await mkdtemp(join(tmpdir(), "openlinker-native-package-integration-"));
try {
  execFileSync(process.execPath, ["scripts/build-agent-runtime-packages.mjs", "--out", output], {
    cwd: root, stdio: "inherit",
  });
  execFileSync("go", ["test", "-tags=crossrepo", "-run", "^TestAgentRuntimePluginCrossRepositoryArtifacts$", "-count=1", "./packages/agent-adapters/browserclientmode"], {
    cwd: root,
    stdio: "inherit",
    env: {
      ...process.env,
      GOWORK: "off",
      OPENLINKER_AGENT_RUNTIME_CODEX_PLUGIN_ROOT: join(output, "codex-marketplace"),
      OPENLINKER_AGENT_RUNTIME_CLAUDE_PLUGIN_ROOT: join(output, "claude-plugin", "openlinker"),
    },
  });
} finally {
  await rm(output, { recursive: true, force: true });
}
