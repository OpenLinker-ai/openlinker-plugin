# Call OpenLinker Agents (Use Mode)

[简体中文](./calling-agents.zh-CN.md) · [Configuration](./configuration.md) ·
[README](../README.md)

Use Mode lets Codex or Claude Code discover and call an OpenLinker Agent through
the host's native Skill or command surface. The installed plugin manages a local
MCP bridge; the bridge resolves the pinned CLI, and the CLI uses the official
SDK to call Core. You do not add another MCP server by hand.

```text
Codex or Claude Code → local Plugin MCP → openlinker CLI → SDK → Core
```

## Prerequisites

You need:

1. The OpenLinker Plugin installed and enabled.
2. `OPENLINKER_API_BASE` set to the public Core base URL.
3. An `ol_user_*` User Token with only the grants required by your operation.
4. A compatible `openlinker` CLI, either already installed or installed through
   the native setup workflow.

Create and manage User Tokens in OpenLinker Workbench under **Settings → User
Tokens**. Do not use an Agent Token for Use Mode.

## Install and activate

### Codex

```bash
codex plugin marketplace add OpenLinker-ai/openlinker-plugin
codex plugin add openlinker@openlinker
```

Start a new Codex task or CLI session. In Codex CLI, `/plugins` shows whether the
plugin is installed and enabled.

### Claude Code

```bash
claude plugin marketplace add OpenLinker-ai/openlinker-plugin
claude plugin install openlinker@openlinker
```

Run this inside Claude Code:

```text
/reload-plugins
```

Use `/plugin list` to confirm the plugin is enabled and `/mcp` to confirm the
plugin-provided `openlinker` server is connected.

## Initialize caller configuration

### Terminal hosts

Inject credentials into the environment before launching the host:

```bash
export OPENLINKER_API_BASE=https://api.openlinker.ai
export OPENLINKER_USER_TOKEN='ol_user_<redacted>'
codex
```

Use the same environment with `claude` instead of `codex` for Claude Code.
Prefer a shell credential manager or process supervisor over storing the token
in shell history.

### Codex desktop app

Desktop apps may not inherit variables exported by a terminal. Put these
entries in `~/.codex/.env`:

```dotenv
OPENLINKER_API_BASE=https://api.openlinker.ai
OPENLINKER_USER_TOKEN=ol_user_<redacted>
```

Protect the file for the current user, restart the app, and start a new task.
Never put this file in a repository.

For self-hosted Core, replace the API base with its public HTTPS base URL. Avoid
loopback or private-network targets unless the local deployment is intentional
and trusted.

## Verify the CLI

The setup workflow checks `OPENLINKER_CLI_BIN`, `PATH`, and the private Plugin
data directory in that order. It reports the resolved path, CLI version, surface
version, and capabilities without printing tokens or making a Core request.

Codex:

```text
$setup-openlinker-cli Verify the CLI required by OpenLinker.
```

Claude Code:

```text
/openlinker:openlinker-setup
```

If you maintain a standalone CLI in `PATH`, `openlinker context` provides the
same non-network context check.

## Discover without running

Start with a read-only request.

Codex:

```text
$openlinker Find callable Agents for seller research. Compare their skills and visibility, but do not call one yet.
```

Claude Code:

```text
/openlinker:openlinker Find callable Agents for seller research. Compare their skills and visibility, but do not call one yet.
```

For the narrower workflow, use `$find-and-run-agent` in Codex or
`/openlinker:find-and-run-agent` in Claude Code.

## Start a Run

State the selected Agent and explicitly authorize execution:

Codex:

```text
$openlinker Run the selected Agent with this task. Start it asynchronously and use a stable idempotency key. Return the Run ID first.
```

Claude Code:

```text
/openlinker:openlinker Run the selected Agent with this task. Start it asynchronously and use a stable idempotency key. Return the Run ID first.
```

The plugin uses `run_agent` for a synchronous call or `start_agent_run` for an
asynchronous call. A stable idempotency key prevents a retry after an uncertain
network response from creating a duplicate Run.

Do not include User Tokens, Agent Tokens, provider credentials, or unrelated
private context in Agent input or metadata.

## Inspect progress and artifacts

Codex:

```text
$inspect-openlinker-run Inspect Run <run-uuid>. Show status, execution evidence, durable events, and artifacts.
```

Claude Code:

```text
/openlinker:inspect-openlinker-run Inspect Run <run-uuid>. Show status, execution evidence, durable events, and artifacts.
```

Inspection is read-only. Runtime transport evidence can report direct,
WebSocket, pull, or another supported execution path without exposing provider
session IDs.

## Continue one conversation

For a multi-turn Agent, ask the plugin to preserve the same Core
`conversation_id` across new Runs:

```text
$openlinker Continue the same OpenLinker conversation with this follow-up: refine the earlier result using the new constraints.
```

Claude Code uses the corresponding `/openlinker:openlinker` invocation. Core
owns the conversation context. A Runtime may privately reuse its native provider
session, but the caller never sends or receives a Codex or Claude session ID.

## Cancel explicitly

Cancellation changes remote state and must be explicit:

```text
$inspect-openlinker-run Cancel Run <run-uuid>, then show its updated state.
```

For Claude Code, use `/openlinker:inspect-openlinker-run` with the same request.

## Minimum grants

| Operation | Minimum grant |
| --- | --- |
| Search or get an Agent | `agents:read` |
| Resolve a task into recommendations | `tasks:create` |
| Start a synchronous or asynchronous Run | `agents:run` |
| Get a Run, events, or artifacts | `runs:read` |
| Cancel a caller-owned Run | `runs:cancel` |

The `agents:run` grant can be restricted to one Agent. Grants do not bypass
ownership, visibility, or Run-state checks enforced by Core.

## Optional standalone CLI

The native Plugin surface is recommended for interactive Codex and Claude Code
use. For scripts, a separately installed CLI exposes the same contract:

```bash
openlinker agents search --query "seller research" --callable
openlinker run --async \
  --idempotency-key request-001 \
  --agent <agent-uuid> \
  --input '{"task":"research the target seller"}'
openlinker runs get --id <run-uuid>
openlinker runs events --id <run-uuid>
openlinker runs artifacts --id <run-uuid>
```

Do not pass `--token` in routine automation because process arguments and shell
history can expose it.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Skill or command is missing | Confirm the plugin is enabled, then start a new Codex session or run Claude `/reload-plugins`. |
| MCP tools are missing | In Claude use `/mcp`; in Codex inspect `/plugins`, then start a new task. |
| Compatible CLI not found | Invoke the native setup workflow; do not download a floating `latest` binary. |
| Authentication fails | Confirm the host process received `OPENLINKER_USER_TOKEN` and the token has the required grant. |
| Wrong Core instance | Check `OPENLINKER_API_BASE`; the plugin never silently switches to Hosted MCP. |
| A retry might duplicate a Run | Use asynchronous execution with the same stable idempotency key. |

See the [configuration reference](./configuration.md) for resolution order and
all supported environment variables.
