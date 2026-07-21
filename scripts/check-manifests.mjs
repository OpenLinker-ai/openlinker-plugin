import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const json = async (path) => JSON.parse(await readFile(join(root, path), "utf8"));

const codex = await json("plugins/openlinker/.codex-plugin/plugin.json");
const claude = await json("plugins/openlinker/.claude-plugin/plugin.json");
const codexMarket = await json(".agents/plugins/marketplace.json");
const claudeMarket = await json(".claude-plugin/marketplace.json");
const surface = await json("contracts/plugin-surface.json");
const pkg = await json("package.json");

assert.equal(codex.name, "openlinker");
assert.equal(claude.name, codex.name);
assert.equal(codex.version, claude.version);
assert.equal(codex.version, surface.plugin_version);
assert.equal(codex.version, pkg.version);
assert.equal(codex.skills, "./skills/");
assert.equal(claude.skills, "./skills/");
assert.equal(claude.commands, "./commands/");
assert.equal("mcpServers" in codex, false, "local Codex plugin must not default to remote MCP");
assert.equal("apps" in codex, false, "ChatGPT App is a separate release target");
assert.equal(codexMarket.plugins[0].name, codex.name);
assert.equal(codexMarket.plugins[0].source.path, "./plugins/openlinker");
assert.equal(claudeMarket.plugins[0].name, claude.name);
assert.equal(claudeMarket.plugins[0].source, "./plugins/openlinker");
assert.equal(claudeMarket.plugins[0].version, claude.version);
assert.equal(surface.cli.surface_version, "openlinker.cli.v1");
assert.equal(surface.hosted_mcp.local_plugin_default, false);

const expectedOperations = [
  "search_agents",
  "get_agent",
  "create_task",
  "run_agent",
  "start_agent_run",
  "get_run",
  "list_run_events",
  "list_run_artifacts",
  "cancel_run",
];
assert.deepEqual(Object.keys(surface.operations).sort(), expectedOperations.sort());
for (const [name, operation] of Object.entries(surface.operations)) {
  assert.ok(operation.cli_capability);
  assert.ok(Array.isArray(operation.cli_command) && operation.cli_command.length > 0);
  assert.equal(operation.mcp_tool, name);
  assert.ok(operation.grant);
  assert.equal(typeof operation.read_only, "boolean");
}

console.log("manifest contracts passed");
