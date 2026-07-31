import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import {
  mkdtemp,
  readFile,
  readdir,
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import test from "node:test";

import {
  buildAgentRuntimePackages,
} from "../scripts/build-agent-runtime-packages.mjs";
import {
  createDeterministicTarGz,
} from "../scripts/create-deterministic-tar-gz.mjs";

const repositoryRoot = resolve(import.meta.dirname, "..");
const execFileAsync = promisify(execFile);

async function filesUnder(base, current = base) {
  const entries = await readdir(current, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(current, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await filesUnder(base, path)));
    } else if (entry.isFile()) {
      files.push(relative(base, path));
    }
  }
  return files.sort();
}

async function json(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

test("Agent Runtime packages expose only the Browser-native surface", async () => {
  const output = await mkdtemp(join(tmpdir(), "openlinker-agent-runtime-"));
  const built = await buildAgentRuntimePackages(output);
  const codexPlugin = join(
    built.codexMarketplace,
    "plugins",
    "openlinker",
  );

  assert.deepEqual(await filesUnder(built.codexMarketplace), [
    ".agents/plugins/marketplace.json",
    "plugins/openlinker/.codex-plugin/plugin.json",
    "plugins/openlinker/.mcp.json",
    "plugins/openlinker/LICENSE",
    "plugins/openlinker/skills/use-isolated-browser/SKILL.md",
    "plugins/openlinker/skills/use-isolated-browser/agents/openai.yaml",
  ]);
  assert.deepEqual(await filesUnder(built.claudePlugin), [
    ".claude-plugin/plugin.json",
    ".mcp.json",
    "LICENSE",
    "commands/use-isolated-browser.md",
    "skills/use-isolated-browser/SKILL.md",
  ]);

  const canonicalSkill = await readFile(
    join(
      repositoryRoot,
      "shared",
      "skills",
      "use-isolated-browser",
      "SKILL.md",
    ),
  );
  assert.deepEqual(
    await readFile(
      join(codexPlugin, "skills", "use-isolated-browser", "SKILL.md"),
    ),
    canonicalSkill,
  );
  assert.deepEqual(
    await readFile(
      join(
        built.claudePlugin,
        "skills",
        "use-isolated-browser",
        "SKILL.md",
      ),
    ),
    canonicalSkill,
  );

  const codexManifest = await json(
    join(codexPlugin, ".codex-plugin", "plugin.json"),
  );
  assert.deepEqual(codexManifest.interface, {
    displayName: "OpenLinker Isolated Browser",
    shortDescription:
      "Use the Runtime-authorized isolated Browser in a packaged Codex Agent.",
    longDescription:
      "Navigate and interact with public webpages through the isolated Browser owned by the current OpenLinker Runtime.",
    developerName: "OpenLinker",
    category: "Productivity",
    capabilities: ["Browser automation"],
    websiteURL: "https://openlinker.ai/",
    defaultPrompt: [
      "Use the isolated Browser to complete this web task.",
    ],
  });

  const codexMCP = await json(join(codexPlugin, ".mcp.json"));
  const claudeMCP = await json(join(built.claudePlugin, ".mcp.json"));
  assert.deepEqual(
    Object.keys(codexMCP.mcpServers),
    ["openlinker_browser"],
  );
  assert.deepEqual(
    Object.keys(claudeMCP.mcpServers),
    ["openlinker_browser"],
  );
  assert.deepEqual(
    codexMCP.mcpServers.openlinker_browser.env_vars,
    ["OPENLINKER_BROWSER_TOOL_SOCKET"],
  );
  assert.deepEqual(
    claudeMCP.mcpServers.openlinker_browser.env,
    {
      OPENLINKER_BROWSER_TOOL_SOCKET: "${OPENLINKER_BROWSER_TOOL_SOCKET}",
    },
  );
  for (const server of [
    codexMCP.mcpServers.openlinker_browser,
    claudeMCP.mcpServers.openlinker_browser,
  ]) {
    assert.equal(server.command, "/usr/local/bin/openlinker");
    assert.deepEqual(server.args.slice(0, 2), ["plugin", "browser-proxy"]);
    assert.equal(server.args.includes("serve"), false);
  }

  const text = (
    await Promise.all(
      [
        ...(await filesUnder(built.codexMarketplace)).map((file) =>
          readFile(join(built.codexMarketplace, file), "utf8")
        ),
        ...(await filesUnder(built.claudePlugin)).map((file) =>
          readFile(join(built.claudePlugin, file), "utf8")
        ),
      ],
    )
  ).join("\n");
  for (const forbidden of [
    "OPENLINKER_USER_TOKEN",
    "OPENLINKER_AGENT_TOKEN",
    "find-and-run-agent",
    "inspect-openlinker-run",
    "serve-openlinker-agent",
    "configure_agent_mode",
    "plugin serve",
  ]) {
    assert.equal(
      text.includes(forbidden),
      false,
      `Agent Runtime package contains forbidden surface ${forbidden}`,
    );
  }
});

test("Agent Runtime archives are byte-stable and contain only generated files", async () => {
  const temporary = await mkdtemp(join(tmpdir(), "openlinker-runtime-archive-"));
  try {
    const packages = await buildAgentRuntimePackages(join(temporary, "packages"));
    const first = join(temporary, "first.tar.gz");
    const second = join(temporary, "second.tar.gz");
    await createDeterministicTarGz(packages.codexMarketplace, first);
    await createDeterministicTarGz(packages.codexMarketplace, second);
    assert.deepEqual(await readFile(first), await readFile(second));
    const { stdout } = await execFileAsync("tar", ["-tzf", first]);
    assert.deepEqual(
      stdout.trim().split("\n").filter((entry) => !entry.endsWith("/")).sort(),
      [
        ".agents/plugins/marketplace.json",
        "plugins/openlinker/.codex-plugin/plugin.json",
        "plugins/openlinker/.mcp.json",
        "plugins/openlinker/LICENSE",
        "plugins/openlinker/skills/use-isolated-browser/SKILL.md",
        "plugins/openlinker/skills/use-isolated-browser/agents/openai.yaml",
      ].sort(),
    );
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});
