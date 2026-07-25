# OpenLinker Plugin Configuration

[简体中文](./configuration.zh-CN.md) · [Use Mode](./calling-agents.md) ·
[Agent Mode](./serving-as-agent.md) · [Browser](./isolated-browser.md) ·
[README](../README.md)

This is the canonical configuration reference for the local Codex and Claude
Code Plugins. The two modes use separate credentials and must not exchange them.

## Trust domains

| Domain | Credential | Used for | Never used for |
| --- | --- | --- | --- |
| Caller | `OPENLINKER_USER_TOKEN` | Search, Run creation, inspection, artifacts, cancellation | Runtime registration or provider authentication |
| Runtime Agent | `OPENLINKER_AGENT_TOKEN` or `_FILE` | Token-only Runtime registration and delivery | Caller operations or provider authentication |
| Provider | Local login, `CODEX_API_KEY`, or `ANTHROPIC_API_KEY` | Dedicated provider CLI execution | Core caller or Runtime authentication |
| Browser channel | Owner-only generated file | Provider Runtime to Browser Runtime UDS authentication | Provider API, Core, model arguments, or page input |

Do not put credentials in prompts, MCP arguments, `agent.json`, Run input,
metadata, project files, or logs.

## Use Mode

| Variable | Required | Meaning |
| --- | --- | --- |
| `OPENLINKER_API_BASE` | Yes | Hosted or self-hosted Core public base URL. |
| `OPENLINKER_USER_TOKEN` | Yes for authenticated operations | Least-privilege `ol_user_*` token. |
| `OPENLINKER_CLI_BIN` | No | Absolute path to a compatible CLI; checked before `PATH` and Plugin data. |
| `OPENLINKER_PLUGIN_DATA` | No | Override private Plugin data containing an installed CLI. |

The local bridge does not silently fall back to Hosted MCP or direct HTTP. The
effective caller order is explicit CLI options for standalone commands, then
environment, then the CLI default. Native Plugin calls use the host environment.

## Minimum User Token grants

| Operation | Grant |
| --- | --- |
| `search_agents`, `get_agent` | `agents:read` |
| `create_task` | `tasks:create` |
| `run_agent`, `start_agent_run` | `agents:run` |
| `get_run`, `list_run_events`, `list_run_artifacts` | `runs:read` |
| `cancel_run` | `runs:cancel` |

## Agent Mode initialization fields

Use `$serve-openlinker-agent` in Codex or
`/openlinker:openlinker-agent` in Claude Code. The native workflow calls
`configure_agent_mode`; standalone deployments can use `openlinker agent
configure`.

| Stored field | Required | Default | Meaning |
| --- | --- | --- | --- |
| `provider` | Yes | — | `codex` or `claude`. |
| `agent_id` | Yes | — | Existing lowercase, non-zero Agent UUID. |
| `workspace` | Yes | — | Existing minimal workspace, saved as an absolute path. |
| `openlinker_url` | Yes | — | Public OpenLinker platform URL used for Runtime discovery. |
| `state_dir` | No | Private default | Persistent Node ID, status, lock, and session maps. |
| `provider_bin` | No | Provider name | Provider CLI name or absolute path. |
| `model` | No | Provider default | Provider model override. |
| `transport` | No | `auto` | `auto`, `websocket`, or `pull`. |
| `capacity` | No | `1` | Concurrent Runs, from 1 through 1024. |
| `timeout_seconds` | No | `1800` | Positive provider execution timeout. |
| `session_reuse` | No | `true` | Reuse a private provider session per Core conversation. |
| `web_search` | No | `false` | Allow provider web search. |
| `execution_profile` | No | `standard` | `standard` or opt-in `browser`; Browser requires capacity 1 and session reuse. |
| `browser_plugin_bin` | Browser only | Current CLI | Absolute compatible OpenLinker CLI used for the Browser MCP subprocess. |
| `browser_socket` | Browser only | — | Private Browser Runtime Unix socket. |
| `browser_credential_file` | Browser only | — | Owner-only Browser channel credential path, never the credential value. |
| `browser_lease_root` | Browser only | — | Private authoritative per-Run lease directory. |
| `browser_broker_root` | Browser only | — | Private local Browser MCP broker directory. |
| `codex_base_url` | Codex only | Provider default | Validated OpenAI-compatible HTTP(S) Base URL; credentials, query, and fragment are forbidden. |
| `codex_sandbox` | Codex only | `read-only` | `read-only` or `workspace-write`. |
| `codex_approval` | Codex only | `never` | `never`, `untrusted`, or `on-request`. |
| `claude_permission` | Claude only | `dontAsk` | `acceptEdits`, `auto`, `dontAsk`, `manual`, or `plan`. |
| `allowed_tools` | Claude only | Empty | Explicit Claude tool allowlist. |
| `enabled` | Managed by control tools | `false` | Persisted Agent Mode preference. |

`agent.json` contains no credentials. An illustrative file is:

```json
{
  "version": 1,
  "enabled": false,
  "provider": "codex",
  "openlinker_url": "https://openlinker.ai",
  "agent_id": "22222222-2222-4222-8222-222222222222",
  "workspace": "/absolute/minimal/workspace",
  "transport": "auto",
  "capacity": 1,
  "timeout_seconds": 1800,
  "session_reuse": true,
  "web_search": false,
  "execution_profile": "standard",
  "codex_base_url": "https://router.example/v1",
  "codex_sandbox": "read-only",
  "codex_approval": "never",
  "claude_permission": "dontAsk"
}
```

Use the native configure workflow instead of hand-editing this file; its decoder
rejects unknown fields and unsupported versions.

## Configuration and state paths

Set `OPENLINKER_AGENT_CONFIG` to override the configuration file. Otherwise the
CLI uses the operating system's user configuration directory:

| Platform | Default `agent.json` |
| --- | --- |
| macOS | `~/Library/Application Support/openlinker/agent.json` |
| Linux | `$XDG_CONFIG_HOME/openlinker/agent.json`, or `~/.config/openlinker/agent.json` |
| Windows | `%AppData%\openlinker\agent.json` |

State resolution order is:

1. `OPENLINKER_AGENT_STATE_DIR`.
2. Stored `state_dir`.
3. `$XDG_STATE_HOME/openlinker/agent`.
4. `~/.local/state/openlinker/agent`.

The state directory is private and contains:

- `node-id`: generated stable Runtime Node UUID;
- `session-map/<provider>.json`: hashed Core-conversation to private provider
  session mappings;
- `status.json`: redacted last status;
- a process lock preventing two workers from sharing the same state.

`OPENLINKER_NODE_ID` is optional. When omitted, the Runtime creates and persists
one. If explicitly supplied later, it must match the persisted value.

## Agent and Provider secrets

| Direct variable | File alternative | Required |
| --- | --- | --- |
| `OPENLINKER_AGENT_TOKEN` | `OPENLINKER_AGENT_TOKEN_FILE` | Exactly one source is required for Agent Mode. |
| `CODEX_API_KEY` | `CODEX_API_KEY_FILE` | Optional when Codex local login is usable. |
| `ANTHROPIC_API_KEY` | `ANTHROPIC_API_KEY_FILE` | Optional when Claude local login is usable. |

For each row, direct and file sources are mutually exclusive. Secret files must
be regular files, not symlinks, owned by the current user, inaccessible to group
or other users, non-empty, and no larger than 64 KiB. Whitespace surrounding
the secret is removed.

The Runtime removes caller and Agent credentials before starting the provider
process. It passes only the selected provider credential plus a small allowlist
of provider state, proxy, and CA-certificate variables.

## Runtime environment overrides

Environment values override stored non-secret Agent configuration at Runtime.

| Variable | Overrides |
| --- | --- |
| `OPENLINKER_PROVIDER` | `provider` |
| `OPENLINKER_URL` | `openlinker_url` |
| `OPENLINKER_API_BASE` | `openlinker_url` when `OPENLINKER_URL` is absent |
| `OPENLINKER_AGENT_ID` | `agent_id` |
| `OPENLINKER_WORKSPACE` | `workspace` |
| `OPENLINKER_AGENT_STATE_DIR` | `state_dir` |
| `OPENLINKER_AGENT_TRANSPORT` | `transport` |
| `OPENLINKER_AGENT_CAPACITY` | `capacity` |
| `OPENLINKER_AGENT_TIMEOUT_SECONDS` | `timeout_seconds` |
| `OPENLINKER_AGENT_SESSION_REUSE` | `session_reuse` |
| `OPENLINKER_AGENT_WEB_SEARCH` | `web_search` fallback |
| `OPENLINKER_AGENT_EXECUTION_PROFILE` | `execution_profile` |
| `OPENLINKER_BROWSER_PLUGIN_BIN` | `browser_plugin_bin` |
| `OPENLINKER_BROWSER_SOCKET` | `browser_socket` |
| `OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE` | `browser_credential_file` |
| `OPENLINKER_BROWSER_LEASE_ROOT` | `browser_lease_root` |
| `OPENLINKER_BROWSER_BROKER_ROOT` | `browser_broker_root` |
| `OPENLINKER_CODEX_MODEL`, `OPENLINKER_CLAUDE_MODEL` | `model` for the selected provider |
| `OPENLINKER_CODEX_BASE_URL` | `codex_base_url`; used by new and resumed Codex sessions |
| `OPENLINKER_CODEX_WEB_SEARCH`, `OPENLINKER_CLAUDE_WEB_SEARCH` | Provider-specific `web_search` |
| `OPENLINKER_CODEX_SANDBOX` | `codex_sandbox` |
| `OPENLINKER_CODEX_APPROVAL` | `codex_approval` |
| `OPENLINKER_CLAUDE_PERMISSION` | `claude_permission` |
| `OPENLINKER_CLAUDE_ALLOWED_TOOLS` | Comma-separated `allowed_tools` |
| `OPENLINKER_CODEX_BIN`, `OPENLINKER_CLAUDE_BIN` | `provider_bin` |

Use environment overrides for deployment injection, not as a second unmanaged
configuration system for interactive use.

## Native Agent-control tools

| Tool | Behavior |
| --- | --- |
| `configure_agent_mode` | Validate and persist non-secret fields. |
| `diagnose_agent_mode` | Return redacted presence, source, and validity checks. |
| `enable_agent_mode` | Persist enablement and start the local Runtime Worker. |
| `get_agent_mode_status` | Return redacted current or persisted status. |
| `disable_agent_mode` | Drain, stop, and persist disablement. |

None accepts a credential or provider session ID.

## Host initialization

### Codex CLI

Set environment variables before launching `codex`, install the Plugin, and
start a new session. Explicit native Skills use `$openlinker`,
`$setup-openlinker-cli`, `$serve-openlinker-agent`, and
`$use-isolated-browser`.

The Codex package declares only the environment variable names required by the
local bridge and passes no inline values. Its MCP process starts from the
installed Plugin root, so the bundled launcher resolves independently of the
user's current workspace.

### Codex desktop app

Put required `KEY=value` entries in `~/.codex/.env`, protect the file, restart
the app, and start a new task. The repository must never contain that file.

### Claude Code

Set environment variables before launching `claude`. After installing or
enabling the Plugin, run `/reload-plugins`. Explicit native commands are
`/openlinker:openlinker`, `/openlinker:openlinker-setup`,
`/openlinker:openlinker-agent`, and `/openlinker:openlinker-browser`.

## Proxies and network scope

The CLI and provider subprocesses honor standard `HTTP_PROXY`, `HTTPS_PROXY`,
`NO_PROXY`, and `ALL_PROXY` variables. The local native Plugin does not provide
a hard network-isolation boundary. The production Browser profile keeps
Chromium in a separate container and forces it through the egress gateway; it
does not expose CDP, WebDriver, VNC, or a TCP control port. Use that deployment
when private-address, link-local, metadata, DNS-rebinding, direct-DNS, QUIC,
WebRTC, DoH, or proxy-bypass protection must be enforced.

## Safe diagnostics

- Native CLI setup reports CLI source, version, surface, and capabilities.
- `diagnose_agent_mode` reports credential source categories such as
  `environment`, `file`, `absent`, or `missing`, never values.
- `get_agent_mode_status` reports lifecycle and non-secret paths.
- `openlinker context` is useful only when a compatible standalone CLI is in
  `PATH` or selected explicitly; it performs no network request.

If a credential changes, restart or reload the host so its local MCP process
receives the new environment.
