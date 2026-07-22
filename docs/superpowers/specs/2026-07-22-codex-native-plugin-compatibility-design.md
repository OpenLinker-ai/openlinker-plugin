# Codex Native Plugin Compatibility Design

Status: accepted on 2026-07-22

## Problem

The Codex plugin bundle loads its Skills but cannot start its bundled MCP server
reliably. Its `.mcp.json` uses `./bin/openlinker-plugin` without a plugin-relative
working directory. Codex therefore resolves the command from the user's current
workspace and reports `No such file or directory`. An isolated installation also
showed that OpenLinker-specific environment variables are not forwarded unless
the MCP declaration names them.

Once the process path and environment are supplied manually, the bridge reaches
the CLI. The remaining `_meta` protocol issue belongs to `openlinker-cli`, not
this repository.

## Goals

- Make a normal Codex plugin installation start the local OpenLinker MCP server
  without an absolute-path override.
- Forward only the named configuration and credential variables required by
  caller mode and explicitly enabled Agent Mode.
- Keep all credentials outside plugin configuration and prompts.
- Pin the first released CLI containing the protocol and Provider fixes.
- Add package checks that fail if the Codex-local execution contract regresses.

## Non-goals and Repository Boundary

- Do not implement Core HTTP, SDK, Runtime Worker, or Provider execution logic.
- Do not vendor or build the CLI in this repository.
- Do not place secret values in `.mcp.json`, the manifest, Skill text, or docs.
- Do not change Claude's `${CLAUDE_PLUGIN_ROOT}` command model merely to make the
  two host manifests textually identical.
- Do not use Hosted MCP as a fallback for a broken local installation.

## Codex MCP Declaration

The Codex `.mcp.json` will retain the relative command and add `cwd: "."`, which
Codex resolves against the plugin root. It will declare `env_vars` as names only.
The allowlist covers these supported groups:

- resolver and caller: `OPENLINKER_PLUGIN_DATA`, `OPENLINKER_CLI_BIN`,
  `OPENLINKER_API_BASE`, `OPENLINKER_USER_TOKEN`;
- Agent Mode location and identity: `OPENLINKER_AGENT_CONFIG`,
  `OPENLINKER_AGENT_STATE_DIR`, `OPENLINKER_AGENT_TOKEN`,
  `OPENLINKER_AGENT_TOKEN_FILE`, `OPENLINKER_URL`, `OPENLINKER_RUNTIME_BASE`,
  `OPENLINKER_NODE_ID`, `OPENLINKER_WORKSPACE`, `OPENLINKER_PROVIDER`,
  `OPENLINKER_AGENT_ID`;
- Provider selection and authentication: `OPENLINKER_CODEX_BIN`,
  `OPENLINKER_CLAUDE_BIN`, `OPENLINKER_CODEX_MODEL`,
  `OPENLINKER_CLAUDE_MODEL`, `OPENLINKER_CODEX_BASE_URL`, `CODEX_HOME`,
  `CODEX_API_KEY`, `CODEX_API_KEY_FILE`, `ANTHROPIC_API_KEY`,
  `ANTHROPIC_API_KEY_FILE`;
- Runtime policy: `OPENLINKER_AGENT_TRANSPORT`,
  `OPENLINKER_AGENT_CAPACITY`, `OPENLINKER_AGENT_TIMEOUT_SECONDS`,
  `OPENLINKER_AGENT_SESSION_REUSE`, `OPENLINKER_AGENT_WEB_SEARCH`,
  `OPENLINKER_CODEX_WEB_SEARCH`, `OPENLINKER_CLAUDE_WEB_SEARCH`,
  `OPENLINKER_CODEX_SANDBOX`, `OPENLINKER_CODEX_APPROVAL`,
  `OPENLINKER_CLAUDE_PERMISSION`, `OPENLINKER_CLAUDE_ALLOWED_TOOLS`;
- network policy: `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`,
  `CODEX_CA_CERTIFICATE`, `SSL_CERT_FILE`.

Absent variables stay absent. The declaration never supplies defaults or secret
values. The CLI remains responsible for mutual exclusivity of direct and `_FILE`
secret forms and for sanitizing the Provider child environment.

## CLI Lock and Installation

The plugin continues to install a checksum-pinned GitHub release rather than a
vendored binary. After the compatible CLI release candidate is published,
`cli-lock.json` and both packaged copies will be regenerated from release asset
metadata. Resolver capability checks remain the runtime compatibility boundary.

## Testing

Manifest checks will assert that the Codex MCP entry has:

- command `./bin/openlinker-plugin`;
- `cwd` equal to `.`;
- exact arguments `plugin serve --host codex`;
- the required environment-variable names and no inline `env` values.

Existing dual-host Skills, scripts, binaries, documentation, secret scans, CLI
resolver tests, and installer checksum tests remain required. A real acceptance
test installs the marketplace into an isolated `CODEX_HOME`, runs the setup
Skill, invokes `$openlinker` without an extra MCP override, and completes one
zero-cost Run plus inspection.

## Release and Rollback

The existing plugin PR will carry this change and the updated CLI lock. It is
merged only after the compatible CLI release exists and the native caller and
callee acceptance tests pass. Rollback restores the prior plugin commit and CLI
lock; no user configuration migration is required.
