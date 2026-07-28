import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { constants } from "node:fs";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const json = async (path) => JSON.parse(await readFile(join(root, path), "utf8"));
const exists = async (path) => {
  try { await access(join(root, path), constants.F_OK); return true; } catch { return false; }
};

const codex = await json("platforms/codex/openlinker/.codex-plugin/plugin.json");
const claude = await json("platforms/claude/openlinker/.claude-plugin/plugin.json");
const codexMCP = await json("platforms/codex/openlinker/.mcp.json");
const claudeMCP = await json("platforms/claude/openlinker/.mcp.json");
const codexMarket = await json(".agents/plugins/marketplace.json");
const claudeMarket = await json(".claude-plugin/marketplace.json");
const caller = await json("shared/contracts/caller-surface.json");
const agent = await json("shared/contracts/agent-surface.json");
const pkg = await json("package.json");
const baseVersion = (value) => value.split("+")[0];

assert.equal(codex.name, "openlinker");
assert.equal(claude.name, codex.name);
assert.equal(baseVersion(codex.version), claude.version);
assert.equal(claude.version, caller.plugin_version);
assert.equal(claude.version, agent.plugin_version);
assert.equal(claude.version, pkg.version);
assert.equal(codex.skills, "./skills/");
assert.equal(codex.mcpServers, "./.mcp.json");
assert.equal(claude.skills, "./skills/");
assert.equal(claude.commands, "./commands/");
assert.equal("apps" in codex, false, "ChatGPT App is a separate release target");
assert.equal(await exists("platforms/codex/openlinker/.claude-plugin/plugin.json"), false);
assert.equal(await exists("platforms/claude/openlinker/.codex-plugin/plugin.json"), false);
assert.equal(await exists("platforms/codex/openlinker/commands"), false);
assert.equal(await exists("commands"), false, "legacy root commands directory must not exist");
assert.equal(await exists("plugins"), false, "legacy root plugins directory must not exist");

const codexServer = codexMCP.mcpServers.openlinker;
const codexEnvironment = [
  "ALL_PROXY",
  "ANTHROPIC_API_KEY",
  "ANTHROPIC_API_KEY_FILE",
  "CODEX_API_KEY",
  "CODEX_API_KEY_FILE",
  "CODEX_CA_CERTIFICATE",
  "CODEX_HOME",
  "HTTP_PROXY",
  "HTTPS_PROXY",
  "NO_PROXY",
  "OPENLINKER_AGENT_CAPACITY",
  "OPENLINKER_AGENT_CONFIG",
  "OPENLINKER_AGENT_EXECUTION_PROFILE",
  "OPENLINKER_AGENT_ID",
  "OPENLINKER_AGENT_SESSION_REUSE",
  "OPENLINKER_AGENT_STATE_DIR",
  "OPENLINKER_AGENT_TIMEOUT_SECONDS",
  "OPENLINKER_AGENT_TOKEN",
  "OPENLINKER_AGENT_TOKEN_FILE",
  "OPENLINKER_AGENT_TRANSPORT",
  "OPENLINKER_AGENT_WEB_SEARCH",
  "OPENLINKER_API_BASE",
  "OPENLINKER_CLAUDE_ALLOWED_TOOLS",
  "OPENLINKER_CLAUDE_BIN",
  "OPENLINKER_CLAUDE_MODEL",
  "OPENLINKER_CLAUDE_PERMISSION",
  "OPENLINKER_CLAUDE_WEB_SEARCH",
  "OPENLINKER_BROWSER_BROKER_ROOT",
  "OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE",
  "OPENLINKER_BROWSER_LEASE_ROOT",
  "OPENLINKER_BROWSER_PLUGIN_BIN",
  "OPENLINKER_BROWSER_SOCKET",
  "OPENLINKER_CLI_BIN",
  "OPENLINKER_CODEX_APPROVAL",
  "OPENLINKER_CODEX_BASE_URL",
  "OPENLINKER_CODEX_BIN",
  "OPENLINKER_CODEX_MODEL",
  "OPENLINKER_CODEX_SANDBOX",
  "OPENLINKER_CODEX_WEB_SEARCH",
  "OPENLINKER_NODE_ID",
  "OPENLINKER_PLUGIN_DATA",
  "OPENLINKER_PROVIDER",
  "OPENLINKER_RUNTIME_BASE",
  "OPENLINKER_URL",
  "OPENLINKER_USER_TOKEN",
  "OPENLINKER_WORKSPACE",
  "SSL_CERT_FILE",
];
assert.deepEqual(codexServer.args, ["plugin", "serve", "--host", "codex"]);
assert.equal(codexServer.cwd, ".");
assert.deepEqual(codexServer.env_vars, codexEnvironment);
assert.equal("env" in codexServer, false, "Codex MCP manifest must not contain inline environment values");
assert.deepEqual(claudeMCP.mcpServers.openlinker.args, ["plugin", "serve", "--host", "claude"]);
assert.deepEqual(
  Object.keys(codexMCP.mcpServers),
  ["openlinker"],
  "ordinary Codex installs must not advertise a phantom Browser MCP server",
);
assert.deepEqual(
  Object.keys(claudeMCP.mcpServers),
  ["openlinker"],
  "ordinary Claude installs must not advertise a phantom Browser MCP server",
);
for (const path of ["platforms/codex/openlinker/bin/openlinker-plugin", "platforms/claude/openlinker/bin/openlinker-plugin"]) {
  const launcher = await readFile(join(root, path), "utf8");
  assert.match(launcher, /required_capability=plugin\.serve/);
  assert.match(launcher, /required_capability=plugin\.browser\.serve/);
  assert.doesNotMatch(launcher, /client-browser/);
}
for (const path of ["platforms/codex/openlinker/bin/openlinker-plugin.cmd", "platforms/claude/openlinker/bin/openlinker-plugin.cmd"]) {
  const launcher = await readFile(join(root, path), "utf8");
  assert.match(launcher, /REQUIRED_CAPABILITY=plugin\.serve/);
  assert.match(launcher, /REQUIRED_CAPABILITY=plugin\.browser\.serve/);
  assert.doesNotMatch(launcher, /client-browser/);
}
assert.equal(codexMarket.plugins[0].name, codex.name);
assert.equal(codexMarket.plugins[0].source.path, "./platforms/codex/openlinker");
assert.equal(claudeMarket.plugins[0].name, claude.name);
assert.equal(claudeMarket.plugins[0].source, "./platforms/claude/openlinker");
assert.equal(claudeMarket.plugins[0].version, claude.version);

const expectedOperations = [
  "search_agents", "get_agent", "create_task", "run_agent", "start_agent_run",
  "get_run", "list_run_events", "list_run_artifacts", "cancel_run",
];
assert.deepEqual(Object.keys(caller.operations).sort(), expectedOperations.sort());
for (const [name, operation] of Object.entries(caller.operations)) {
  assert.ok(operation.cli_capability);
  assert.ok(Array.isArray(operation.cli_command) && operation.cli_command.length > 0);
  assert.equal(operation.mcp_tool, name);
  assert.ok(operation.grant);
  assert.equal(typeof operation.read_only, "boolean");
}
assert.deepEqual(agent.providers, ["codex", "claude"]);
assert.deepEqual(agent.tools, [
  "configure_agent_mode", "enable_agent_mode", "disable_agent_mode",
  "get_agent_mode_status", "diagnose_agent_mode",
]);
for (const capability of ["agent.configure", "agent.serve", "agent.status", "agent.doctor", "plugin.serve", "plugin.browser.serve"]) {
  assert.ok(agent.capabilities.includes(capability));
}
assert.equal(agent.security.runtime, "token_only");
assert.equal(agent.security.agent_mode_default, "disabled");
assert.deepEqual(agent.execution_profiles, ["standard", "browser"]);
assert.equal(agent.browser.model_tool, "browser_session");
assert.equal(agent.browser.provider_computer_api, false);
assert.equal(agent.browser.server_authoritative_identity, true);
assert.equal(agent.browser.provider_credentials_visible_to_browser, false);

console.log("dual-platform manifest contracts passed");
