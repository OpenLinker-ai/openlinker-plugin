#!/usr/bin/env node

import {
  copyFile,
  mkdir,
  readFile,
  writeFile,
} from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { readAgentHostContract } from "./resolve-agent-node-module.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const canonicalSkillRoot = join(
  repositoryRoot,
  "shared",
  "skills",
  "use-isolated-browser",
);

function prettyJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

async function writeJSON(path, value) {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, prettyJSON(value), "utf8");
}

async function copyCanonicalFile(source, target) {
  await mkdir(dirname(target), { recursive: true });
  await copyFile(source, target);
}

async function packageVersion() {
  const raw = await readFile(join(repositoryRoot, "package.json"), "utf8");
  return JSON.parse(raw).version;
}

function browserMCP(host, hostContract) {
  const server = {
    command: "/usr/local/bin/openlinker",
    args: [...hostContract.browser_proxy, host],
  };
  if (host === "codex") {
    server.cwd = "/workspace";
    server.env_vars = ["OPENLINKER_BROWSER_TOOL_SOCKET"];
  } else {
    server.env = {
      OPENLINKER_BROWSER_TOOL_SOCKET: "${OPENLINKER_BROWSER_TOOL_SOCKET}",
    };
  }
  return {
    mcpServers: {
      openlinker_browser: server,
    },
  };
}

export async function buildAgentRuntimePackages(outputRoot) {
  const hostContract = readAgentHostContract();
  const root = resolve(outputRoot);
  const version = await packageVersion();
  const codexMarketplace = join(root, "codex-marketplace");
  const codexPlugin = join(
    codexMarketplace,
    "plugins",
    "openlinker",
  );
  const claudePlugin = join(root, "claude-plugin", "openlinker");

  await writeJSON(
    join(codexMarketplace, ".agents", "plugins", "marketplace.json"),
    {
      name: "openlinker-agent-runtime",
      interface: {
        displayName: "OpenLinker Agent Runtime",
      },
      plugins: [
        {
          name: "openlinker",
          source: {
            source: "local",
            path: "./plugins/openlinker",
          },
          policy: {
            installation: "AVAILABLE",
            authentication: "ON_INSTALL",
          },
          category: "Productivity",
        },
      ],
    },
  );
  await writeJSON(
    join(codexPlugin, ".codex-plugin", "plugin.json"),
    {
      name: "openlinker",
      version: `${version}+agent-runtime.codex`,
      description:
        "Use the Runtime-authorized OpenLinker isolated Browser from a packaged Codex Agent.",
      author: {
        name: "OpenLinker",
        url: "https://openlinker.ai/",
      },
      homepage: "https://openlinker.ai/",
      repository: "https://github.com/OpenLinker-ai/openlinker-plugin",
      license: "Apache-2.0",
      keywords: ["openlinker", "agent-runtime", "isolated-browser"],
      skills: "./skills/",
      mcpServers: "./.mcp.json",
      interface: {
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
      },
    },
  );
  await writeJSON(join(codexPlugin, ".mcp.json"), browserMCP("codex", hostContract));

  await writeJSON(
    join(claudePlugin, ".claude-plugin", "plugin.json"),
    {
      $schema:
        "https://json.schemastore.org/claude-code-plugin-manifest.json",
      name: "openlinker",
      displayName: "OpenLinker Agent Runtime Browser",
      version,
      description:
        "Use the Runtime-authorized OpenLinker isolated Browser from a packaged Claude Code Agent.",
      author: {
        name: "OpenLinker",
      },
      homepage: "https://openlinker.ai/",
      repository: "https://github.com/OpenLinker-ai/openlinker-plugin",
      license: "Apache-2.0",
      keywords: ["openlinker", "agent-runtime", "isolated-browser"],
      skills: "./skills/",
      commands: "./commands/",
    },
  );
  await writeJSON(join(claudePlugin, ".mcp.json"), browserMCP("claude", hostContract));

  await Promise.all([
    copyCanonicalFile(
      join(canonicalSkillRoot, "SKILL.md"),
      join(codexPlugin, "skills", "use-isolated-browser", "SKILL.md"),
    ),
    copyCanonicalFile(
      join(canonicalSkillRoot, "agents", "openai.yaml"),
      join(
        codexPlugin,
        "skills",
        "use-isolated-browser",
        "agents",
        "openai.yaml",
      ),
    ),
    copyCanonicalFile(
      join(canonicalSkillRoot, "SKILL.md"),
      join(claudePlugin, "skills", "use-isolated-browser", "SKILL.md"),
    ),
    copyCanonicalFile(
      join(repositoryRoot, "platforms", "claude", "openlinker", "commands", "use-isolated-browser.md"),
      join(claudePlugin, "commands", "use-isolated-browser.md"),
    ),
    copyCanonicalFile(
      join(repositoryRoot, "LICENSE"),
      join(codexPlugin, "LICENSE"),
    ),
    copyCanonicalFile(
      join(repositoryRoot, "LICENSE"),
      join(claudePlugin, "LICENSE"),
    ),
  ]);

  return {
    codexMarketplace,
    claudePlugin,
    version,
  };
}

function argumentValue(argv, name) {
  const index = argv.indexOf(name);
  if (index < 0 || index + 1 >= argv.length) return "";
  return argv[index + 1];
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const output = argumentValue(process.argv.slice(2), "--out");
  if (output === "") {
    process.stderr.write(
      "usage: build-agent-runtime-packages.mjs --out <directory>\n",
    );
    process.exitCode = 2;
  } else {
    const result = await buildAgentRuntimePackages(output);
    process.stdout.write(
      `${JSON.stringify({
        codex_marketplace: result.codexMarketplace,
        claude_plugin: result.claudePlugin,
        version: result.version,
      })}\n`,
    );
  }
}
