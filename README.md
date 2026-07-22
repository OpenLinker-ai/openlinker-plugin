# OpenLinker Plugin

Official bidirectional OpenLinker plugins for Codex and Claude Code.

This repository is in Developer Preview. Each host gets its own native package,
while both packages use the same reviewed Skills, contracts, assets, and pinned
`openlinker` CLI. The CLI-backed local MCP bridge supports both directions:
calling OpenLinker Agents and, only after explicit enablement, serving the
current Codex or Claude Code host as a reusable OpenLinker Runtime Agent.

## Packages

- `platforms/codex/openlinker`: native Codex plugin package.
- `platforms/claude/openlinker`: native Claude Code plugin package and slash commands.
- `shared/skills`: canonical Skills mirrored byte-for-byte into both packages.
- `shared/contracts`: caller, Agent Mode, and CLI release contracts.
- `shared/assets`: shared brand assets.
- `chatgpt`: authenticated ChatGPT App readiness contract and Browser workflow.

The ChatGPT package is intentionally not installable yet. ChatGPT expects an
authenticated MCP app that exposes user data or write tools to use OAuth 2.1;
Core currently accepts scoped `ol_user_*` tokens at MCP, which is suitable for
local MCP clients but is not an OAuth authorization flow. The repository keeps
the real connector ID unset and fails its release check until OAuth discovery,
PKCE, refresh, revocation, and production endpoint tests are complete. It does
not publish a placeholder `.app.json` or a shared account workaround.

The future ChatGPT workflow can compose OpenLinker tools with the separately
installed, host-provided Browser plugin. Browser is never bundled into the
local CLI plugins or Provider Runtime images.

## Local requirements

- `openlinker` CLI compatible with `openlinker.cli.v1`.
- `OPENLINKER_API_BASE` for the Hosted or Self-hosted Core instance.
- A least-privilege `OPENLINKER_USER_TOKEN` supplied outside prompts and project files.

Run `openlinker context` to inspect the effective API base, CLI version,
surface version, and capabilities without sending a network request or printing
the token.

## Local validation

```bash
npm test
python3 /path/to/plugin-creator/scripts/validate_plugin.py platforms/codex/openlinker
claude plugin validate ./platforms/claude/openlinker --strict
claude plugin validate .
```

The Python validator requires PyYAML. The public release workflow installs the
validator dependency in an isolated environment.

## Release ordering

The Plugin release is intentionally gated on a published compatible CLI. After
the CLI release exists, generate the immutable six-platform lock and run the
release gate:

```bash
npm run lock:cli -- v0.2.0-rc.1 --write
npm run release:check
```

The installer follows only approved public GitHub release hosts, honors
standard HTTP/HTTPS proxy variables through curl, verifies both the adjacent
checksum and the digest pinned in `cli-lock.json`, extracts only the expected
executable, validates its JSON capability surface, rejects symlinked
destinations, and replaces the previous binary atomically.

## Local marketplace testing

For Codex, add this repository's marketplace only when testing the local source:

```bash
codex plugin marketplace add /absolute/path/to/openlinker-plugin
codex plugin add openlinker@openlinker
```

For Claude Code:

```bash
claude plugin marketplace add /absolute/path/to/openlinker-plugin
claude plugin install openlinker@openlinker
```

Do not commit User Tokens or pass them as command-line arguments. See
`https://openlinker.ai/privacy` and `https://openlinker.ai/terms` for the Hosted
service policies.
