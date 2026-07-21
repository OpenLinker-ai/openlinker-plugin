# OpenLinker Plugins

Official OpenLinker Skills and native plugins for Codex and Claude Code.

This repository is in Developer Preview. Local Codex and Claude Code plugins
use the JSON-first `openlinker` CLI; they do not use Agent Node or silently
switch to Hosted MCP. Agent Node belongs to the opposite direction, where
OpenLinker Runtime invokes Codex or Claude Code as an Agent.

## Packages

- `skills/find-and-run-agent`: standalone Agent discovery and invocation Skill.
- `skills/inspect-openlinker-run`: standalone Run inspection and cancellation Skill.
- `plugins/openlinker`: Codex and Claude Code native Plugin package.
- `chatgpt`: authenticated ChatGPT App readiness contract and Browser workflow.
- `contracts/plugin-surface.json`: shared CLI/MCP semantic operation mapping.

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
python3 /path/to/plugin-creator/scripts/validate_plugin.py plugins/openlinker
claude plugin validate ./plugins/openlinker --strict
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
codex plugin marketplace add /absolute/path/to/openlinker-plugins
codex plugin add openlinker@openlinker
```

For Claude Code:

```bash
claude plugin marketplace add /absolute/path/to/openlinker-plugins
claude plugin install openlinker@openlinker
```

Do not commit User Tokens or pass them as command-line arguments. See
`https://openlinker.ai/privacy` and `https://openlinker.ai/terms` for the Hosted
service policies.
