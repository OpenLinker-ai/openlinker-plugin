# Serve Codex or Claude Code as an Agent (Agent Mode)

[简体中文](./serving-as-agent.zh-CN.md) · [Configuration](./configuration.md) ·
[README](../README.md)

Agent Mode makes the current Codex or Claude Code host callable through an
existing OpenLinker Agent. The plugin's local MCP bridge runs the official SDK
Runtime Worker and invokes a dedicated provider CLI process for each remote Run.
It does not require OpenLinker Agent Node.

```text
OpenLinker Core → SDK Runtime Worker → Codex or Claude provider CLI
```

Agent Mode is disabled by default. Installing, updating, or invoking the Plugin
does not enable it automatically.

## Prerequisites

Prepare these outside model context:

1. An existing OpenLinker Agent UUID.
2. An Agent Token for that Agent.
3. An existing minimal workspace directory for remote tasks.
4. The `codex` or `claude` CLI installed and authenticated.
5. A compatible `openlinker` CLI resolved by the Plugin.

Use a workspace containing only the files remote tasks need. Plugin mode uses
the host sandbox and software policy; it is intended for personal or development
operation, not as a strong multi-tenant isolation boundary.

## Inject secrets before starting the host

The model must never receive Agent Tokens, provider API keys, secret-file
contents, or provider session IDs.

Direct environment form:

```bash
export OPENLINKER_AGENT_TOKEN='ol_agent_<redacted>'
export CODEX_API_KEY='<redacted>'
codex
```

For a Claude provider, use `ANTHROPIC_API_KEY` and launch `claude`. A provider
API key is optional when its CLI already has usable local login state.

Secret-file form:

```bash
export OPENLINKER_AGENT_TOKEN_FILE=/secure/path/openlinker-agent-token
export CODEX_API_KEY_FILE=/secure/path/codex-api-key
```

For Claude, use `ANTHROPIC_API_KEY_FILE`. Each secret file must be a regular,
non-symlink file owned by the current user and inaccessible to group or other
users. Direct and `_FILE` forms for the same secret are mutually exclusive.

For the Codex desktop app, add the required entries to `~/.codex/.env`, restart
the app, and start a new task. Do not store secrets in `agent.json` or a project
`.env` file.

## Step 1: verify the local bridge

Codex:

```text
$setup-openlinker-cli Verify Agent Mode capabilities without enabling Agent Mode.
```

Claude Code:

```text
/openlinker:openlinker-setup
```

The required CLI surface includes `agent.configure`, `agent.serve`,
`agent.status`, `agent.doctor`, and `plugin.serve`.

## Step 2: configure non-secret values

Codex:

```text
$serve-openlinker-agent Configure Codex as Agent <agent-uuid>. Use workspace /absolute/minimal/workspace and OpenLinker URL https://openlinker.ai. Keep transport auto, capacity 1, and session reuse enabled. Do not enable Agent Mode yet.
```

Claude Code:

```text
/openlinker:openlinker-agent Configure Claude Code as Agent <agent-uuid>. Use workspace /absolute/minimal/workspace and OpenLinker URL https://openlinker.ai. Keep transport auto, capacity 1, and session reuse enabled. Do not enable Agent Mode yet.
```

This calls `configure_agent_mode`. It accepts only non-secret configuration and
writes owner-only `agent.json`. Use the configuration reference for defaults,
paths, and provider policy options.

## Step 3: diagnose

Codex:

```text
$serve-openlinker-agent Diagnose the saved Codex Agent Mode configuration. Report only redacted presence and source categories. Do not enable it.
```

Claude Code:

```text
/openlinker:openlinker-agent Diagnose the saved Claude Agent Mode configuration. Report only redacted presence and source categories. Do not enable it.
```

This calls `diagnose_agent_mode`. Required checks cover provider, Agent UUID,
public URL, workspace, Runtime options, Agent Token, provider CLI, provider auth,
private state, and token-only Runtime security. Diagnostics never return secret
values.

## Step 4: enable explicitly

Enable only after diagnosis passes.

Codex:

```text
$serve-openlinker-agent Enable Agent Mode now and show the redacted status.
```

Claude Code:

```text
/openlinker:openlinker-agent Enable Agent Mode now and show the redacted status.
```

This calls `enable_agent_mode`. The bridge persists `enabled: true`, starts the
Runtime Worker, registers capacity with Core, and returns redacted status. If
startup fails, the bridge restores the disabled state rather than leaving a
false enabled configuration.

When the host is later restarted with the Plugin enabled and credentials still
available, the local bridge starts the persisted Agent Mode again. Closing the
host removes its live capacity.

## Step 5: inspect status

Codex:

```text
$serve-openlinker-agent Show the current redacted Agent Mode status.
```

Claude Code:

```text
/openlinker:openlinker-agent Show the current redacted Agent Mode status.
```

This calls `get_agent_mode_status`. Expected lifecycle states are `disabled`,
`starting`, `ready`, `draining`, `stopped`, and `error`.

## Step 6: verify multi-turn session reuse

From another OpenLinker caller, invoke the Agent twice with the same Core
conversation and a follow-up task on the second Run. Keep the Runtime online
between calls.

With `session_reuse: true`:

1. Core supplies a stable conversation key and persisted conversation history.
2. The Runtime hashes that key into a private provider-session mapping.
3. The first Run starts a dedicated Codex or Claude session.
4. The follow-up resumes that native session when it still exists.
5. If the provider session disappeared, the Runtime removes the stale mapping
   and retries once using Core-owned history.

Run output may report whether reuse, resume, or recovery occurred, but never
contains the raw provider session ID. The mapping is stored below the private
Agent state directory.

## Step 7: disable and drain

Codex:

```text
$serve-openlinker-agent Disable Agent Mode and drain active work gracefully.
```

Claude Code:

```text
/openlinker:openlinker-agent Disable Agent Mode and drain active work gracefully.
```

This calls `disable_agent_mode`, drains the Runtime Worker, and persists
`enabled: false`.

## Agent-control tools

| Tool | Purpose | Secret arguments allowed |
| --- | --- | --- |
| `configure_agent_mode` | Save non-secret configuration. | No |
| `diagnose_agent_mode` | Check configuration and credential presence. | No |
| `enable_agent_mode` | Persist enablement and start the Runtime Worker. | No |
| `get_agent_mode_status` | Return redacted status. | No |
| `disable_agent_mode` | Drain, stop, and persist disablement. | No |

## Headless or 24x7 operation

Native Plugin mode follows the Codex or Claude Code host lifecycle. For a
supervised process, install the CLI separately and use the same SDK-backed
Runtime implementation:

```bash
openlinker agent configure \
  --provider codex \
  --agent-id <agent-uuid> \
  --workspace /absolute/minimal/workspace \
  --url https://openlinker.ai
openlinker agent doctor --provider codex
openlinker agent serve --provider codex
```

`agent serve` runs in the foreground and does not require `enabled: true`.
Production images add a persistent Runtime volume, a non-root provider process,
and enforced outbound-network policy. They still use the CLI and SDK directly,
not Agent Node.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Agent remains offline | Confirm the host and Plugin MCP bridge are running and Agent Mode status is `ready`. |
| `agent_token` is missing | Inject exactly one of `OPENLINKER_AGENT_TOKEN` or `OPENLINKER_AGENT_TOKEN_FILE`, then restart the host. |
| Provider CLI is missing | Install and authenticate the selected `codex` or `claude` CLI; optionally configure its absolute binary path. |
| Secret file is rejected | Require a regular non-symlink file, current-user ownership, and mode without group/other access. |
| Node ID mismatch | Remove the explicit `OPENLINKER_NODE_ID` or make it match the persisted private Node ID. Do not casually delete state. |
| Worker already active | Only one Agent Mode process may use a state directory; stop the other process or use a separate state directory. |
| Session did not resume | Confirm both Runs used the same Core conversation and `session_reuse` is enabled. Recovery may start one replacement session. |
| Host must stay online continuously | Use supervised `openlinker agent serve` or a production provider image. |

See [Configuration](./configuration.md) for all fields, precedence rules, and
storage paths.
