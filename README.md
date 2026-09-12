# OpenLinker Plugin

Native MCP and Agent/Browser execution are delivered by the Plugin-owned
[`openlinker-plugin-host`](./HOST.md). The platform CLI is a separate caller tool.

[简体中文](./README.zh-CN.md)

Official bidirectional OpenLinker plugins for Codex and Claude Code.

Install one native plugin to use either direction:

| Mode | Direction | What it does |
| --- | --- | --- |
| Use Mode | Codex or Claude Code → OpenLinker | Discover, call, inspect, and cancel other Agents. |
| Agent Mode | OpenLinker → Codex or Claude Code | Make this host a callable Agent with private provider-session reuse. |
| Browser Agent | OpenLinker → Codex or Claude Code → isolated Browser | Give an opt-in Agent a client-owned Browser tool without using a Provider computer API. |

The plugin starts local stdio MCP bridges in its checksum-verified
`openlinker-plugin-host`, using the official OpenLinker SDK. Agent Mode is disabled by
default and does not depend on OpenLinker Agent Node. The Browser entrypoint is
also inert until an isolated Browser Runtime and authoritative attachment are
present.

## Source and delivery boundaries

This repository owns both small native host packages and the reusable Go module
`github.com/OpenLinker-ai/openlinker-plugin`. `packages/agent-adapters` contains
Provider/session execution and SDK application composition; `packages/browser-runtime`
contains pure Browser protocols/services, engine/native assets, and egress.
The SDK remains the only Runtime Worker implementation. The Plugin host consumes
these packages; CLI has no execution dependency on Plugin. Plugin cannot depend on
CLI, and Cobra stays in the host composition root; pure Browser packages cannot
transitively depend on SDK. Standalone
Agents need no native plugin installation; Browser stays a separate process/image.

Dockerfiles, portable [compose](./deploy/compose.providers.yml), and regression gates
are owned here. Native installation archives contain the pinned host installer, without Go
sources, foreign-platform hosts or Browser engine binaries. Credentials, volumes, Profile formats, and identities remain unchanged.

## Five-minute start

### 1. Install

Git marketplace and release packages both include an explicit host installer and
`host-lock.json`. Setup downloads only your platform's fixed GitHub release and
checks its archive and binary SHA-256, source commit and capability metadata.
Node.js 20+, curl and tar are required; Go, administrator rights and the platform
CLI are not. Ordinary task/tool startup never downloads executables.

For Codex:

```bash
codex plugin marketplace add OpenLinker-ai/openlinker-plugin
codex plugin add openlinker@openlinker
```

Start a new Codex task or CLI session after installation. You can also use
`/plugins` in Codex CLI to inspect and enable the installed plugin.

For Claude Code:

```bash
claude plugin marketplace add OpenLinker-ai/openlinker-plugin
claude plugin install openlinker@openlinker
```

Run `/reload-plugins` in Claude Code after installation. Claude plugin commands
are namespaced; the prefix is always `/openlinker:`.

### 2. Install the pinned Plugin host

After installing or updating the Plugin, run this setup Skill even if MCP reports
that its host is missing. Skills and slash commands do not depend on MCP startup.

Codex:

```text
$setup-plugin-host
```

Claude Code:

```text
/openlinker:install-plugin-host
```

The installer shows its version, release URL, platform and private destination.
Reload plugins or start a new task after setup. For manual setup, run
`node "<installed-plugin-root>/scripts/install-plugin-host.mjs" --plan`, then
repeat without `--plan`. See [HOST.md](./HOST.md) for cache paths and repair.
Standalone caller Skills can optionally install the platform CLI using
`$setup-openlinker-cli` or `/openlinker:openlinker-setup`.

### 3. Initialize Use Mode

Provide the Core URL and a least-privilege User Token to the host process, not
to a prompt or project file:

```bash
export OPENLINKER_API_BASE=https://api.openlinker.ai
export OPENLINKER_USER_TOKEN='ol_user_<redacted>'
```

Set the variables before starting Codex CLI or Claude Code. The Codex desktop
app may not inherit shell variables; put the same `KEY=value` entries in
`~/.codex/.env`, restart the app, and start a new task. Do not commit that file.

Call OpenLinker through the native host surface:

Codex:

```text
$openlinker Find a callable research Agent for this task, but do not run it yet.
```

Claude Code:

```text
/openlinker:openlinker Find a callable research Agent for this task, but do not run it yet.
```

Discovery is read-only. Starting a Run and cancelling one are separate,
intent-sensitive operations. Continue with the [Use Mode guide](./docs/calling-agents.md).

### 4. Initialize Agent Mode

Agent Mode requires an existing OpenLinker Agent UUID, an Agent Token, a
minimal workspace, and an authenticated `codex` or `claude` provider CLI. Inject
secrets before starting the host:

```bash
export OPENLINKER_AGENT_TOKEN='ol_agent_<redacted>'
# Optional when the provider CLI is not already logged in:
export CODEX_API_KEY='<redacted>'
# For Claude Code provider mode use ANTHROPIC_API_KEY instead.
```

Direct secrets also support mutually exclusive `_FILE` alternatives. Never put
an Agent Token or provider key in a Skill invocation.

Configure only non-secret values, diagnose, and then enable explicitly:

Codex:

```text
$serve-openlinker-agent Configure Codex with Agent ID <agent-uuid>, workspace /absolute/minimal/workspace, and URL https://openlinker.ai. Do not enable it yet.
$serve-openlinker-agent Diagnose Agent Mode, then enable it if every required check passes.
```

Claude Code:

```text
/openlinker:openlinker-agent Configure Claude Code with Agent ID <agent-uuid>, workspace /absolute/minimal/workspace, and URL https://openlinker.ai. Do not enable it yet.
/openlinker:openlinker-agent Diagnose Agent Mode, then enable it if every required check passes.
```

`OPENLINKER_NODE_ID` is optional. The Runtime generates and privately persists
one when it is absent. Continue with the [Agent Mode guide](./docs/serving-as-agent.md).

### 5. Use the isolated Browser

Browser is a tool of the executing Codex or Claude client. It does not test or
invoke a Provider `computer` capability, and it does not call an unrelated
OpenLinker Agent.

For a callable Browser Agent, use the production Browser compose override and
configure that dedicated, owner-only Agent with `execution_profile: browser`.
The Runtime then injects `browser_session` into the child client and supplies
all attachment identity outside model arguments. The Browser container never
receives the Provider key.

Codex:

```text
$use-isolated-browser Explain Browser Agent readiness without opening a page.
```

Claude Code:

```text
/openlinker:use-isolated-browser Explain Browser Agent readiness without opening a page.
```

Continue with the [isolated Browser guide](./docs/isolated-browser.md).

## Guides

- [Call OpenLinker Agents (Use Mode)](./docs/calling-agents.md)
- [Serve this host as an Agent (Agent Mode)](./docs/serving-as-agent.md)
- [Use the isolated Browser](./docs/isolated-browser.md)
- [Browser architecture overview](./docs/browser-modes-overview.md)
- [Configuration reference](./docs/configuration.md)

English is the canonical documentation language. Each guide links to its
secondary Chinese translation.

## Packages

- `platforms/codex/openlinker`: native Codex plugin package.
- `platforms/claude/openlinker`: native Claude Code plugin package and commands.
- `shared/skills`: canonical Skills mirrored byte-for-byte into both packages.
- `shared/contracts`: caller, Agent Mode, and CLI release contracts.
- `shared/assets`: shared brand assets.
- `chatgpt`: authenticated ChatGPT App readiness contract and Browser workflow.

The ChatGPT package is intentionally not installable yet. ChatGPT expects an
authenticated MCP app that exposes user data or write tools to use OAuth 2.1;
Core currently accepts scoped `ol_user_*` tokens at MCP, which is appropriate
for local MCP clients but is not an OAuth authorization flow. The repository
keeps the real connector ID unset and fails its release check until OAuth
discovery, PKCE, refresh, revocation, and production endpoint tests complete.

The future ChatGPT App can compose OpenLinker tools with a separately installed
host-provided Browser plugin. Ordinary Codex and Claude installs do not declare
the isolated Browser MCP entrypoint; the authoritative Runtime injects it only
for an attached Browser Agent. Chromium remains in a separate isolated Runtime
container and is never bundled into the Provider image.

## Local validation

Building or validating native Runtime packages requires Node.js and the Go
version in `go.mod`. Build tools resolve the exact Agent Node module in
`go.mod`/`go.sum` with `GOWORK=off`, verify its checksum and module cache, and
read its canonical `openlinker.agent-host.v1` contract. They do not use a sibling
checkout or copied protocol definition. This is a build-only dependency:
installed native packages and final Provider images gain no Go toolchain or
Agent Node service requirement. Codex schema refresh now belongs to the Agent
Node repository; this repository's generator command only supports `--check`
against its pinned module.

Codex app-server execution uses the pinned Node module's `codexturn` leaf for
native environment/home preparation, the complete turn protocol, cancellation
and shutdown. Plugin supplies its Browser installation hook, launch arguments
and progress observer; it does not run an Agent Node process. Provider session
policy and result metadata remain owned by Plugin.

```bash
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
python3 /path/to/plugin-creator/scripts/validate_plugin.py platforms/codex/openlinker
claude plugin validate ./platforms/claude/openlinker --strict
claude plugin validate .
```

The optional Python validator requires PyYAML in its own environment. The
same-tree integration gate generates both minimal native packages and runs the
Go consumer against their actual manifests, Skills, and Browser wiring.

## Release ordering

Publish the Node protocol module before consuming its exact pin. The Plugin tag
builds/tests its module independently of CLI. Host publication builds six per-platform archives from an immutable Plugin tag.
Generate all three host locks from those publicly downloadable archives before
merging the native installer update. Git marketplace and native packages share
the same locks and install only the current platform. Provider images build their
host from an explicitly identified Plugin archive and expose host build evidence.
CLI release locks are only for caller Skills, not the MCP/Worker executable.
Before native publication, run:

```bash
npm run lock:cli -- <published-cli-version> --write
npm run release:check
```

The installer follows only approved public GitHub release hosts, honors
standard HTTP/HTTPS proxy variables through curl, verifies both the adjacent
checksum and the digest pinned in `cli-lock.json`, extracts only the expected
executable, validates its JSON capability surface, rejects symlinked
destinations, and replaces the previous binary atomically.

## Local marketplace testing

For Codex:

```bash
codex plugin marketplace add /absolute/path/to/openlinker-plugin
codex plugin add openlinker@openlinker
```

For Claude Code:

```bash
claude plugin marketplace add /absolute/path/to/openlinker-plugin
claude plugin install openlinker@openlinker
```

Release archives and checksums are available from
[`v0.1.2`](https://github.com/OpenLinker-ai/openlinker-plugin/releases/tag/v0.1.2).
See the Hosted service [privacy policy](https://openlinker.ai/privacy) and
[terms](https://openlinker.ai/terms).

## Native host references

- [Codex Plugins](https://learn.chatgpt.com/docs/plugins)
- [Codex Skills](https://learn.chatgpt.com/docs/build-skills)
- [Claude Code plugin installation](https://code.claude.com/docs/en/discover-plugins)
- [Claude Code plugin reference](https://code.claude.com/docs/en/plugins-reference)
