# Use the Isolated Browser

[简体中文](./isolated-browser.zh-CN.md) ·
[Configuration](./configuration.md) ·
[Agent Mode](./serving-as-agent.md) · [README](../README.md)

OpenLinker exposes Browser as a tool of the executing Codex or Claude client.
The model uses `browser_session`; it does not use an OpenAI or Anthropic
Provider `computer` API.

```text
OpenLinker Core
  → Runtime Worker
  → normal Codex or Claude client
  → client-registered Browser MCP tool
  → trusted local broker
  → isolated Browser Runtime over Unix socket
  → Chromium through the egress gateway
```

Browser is opt-in. A standard Agent follows the existing provider path and
loads no Browser tool or Browser configuration.

## Two client contexts

The Plugin has the same Browser tool entrypoint in both contexts, but authority
is provisioned differently:

| Context | Who registers `browser_session` | Who supplies attachment authority |
| --- | --- | --- |
| Interactive Codex or Claude host | Installed OpenLinker Plugin | A separately managed local Browser Runtime deployment |
| Callable Browser Agent | Runtime Worker injects an isolated Browser-only MCP configuration into its child client | Core and the Runtime Worker |

The supported production path is the callable Browser Agent. The manifest
entrypoint for an interactive host remains unavailable until an operator has
provisioned the private Runtime socket, channel credential file, and
authoritative active lease. Do not hand-author a lease or put its contents in a
prompt.

## Configure a Browser Agent

Use a dedicated, private, owner-only Agent record. Do not convert an existing
public Agent in place.

The production container group consists of:

- the normal Codex or Claude Provider Runtime;
- a separate Browser Runtime containing Chromium;
- the existing egress gateway;
- private shared mounts for Browser control and the trusted MCP broker;
- persistent Browser Profile storage.

Apply the provider compose file together with its Browser override:

```bash
docker compose \
  -f deploy/compose.codex.yml \
  -f deploy/compose.codex.browser.yml \
  up -d
```

For Claude, use `compose.claude.yml` and
`compose.claude.browser.yml`. The entrypoint fixes the private paths, generates
the Browser channel credential, and configures `execution_profile: browser`.
The operator still supplies the ordinary Agent Token and Provider
authentication outside model context.

For native Plugin configuration, call `configure_agent_mode` with
`execution_profile: browser`, `capacity: 1`, and `session_reuse: true`. In a
container deployment, leave Browser paths to the image entrypoint. Diagnose
before enabling.

## Use the Browser tool

Codex Skill:

```text
$use-isolated-browser Open the requested public page and summarize it.
```

Claude command:

```text
/openlinker:openlinker-browser Open the requested public page and summarize it.
```

The client calls `browser_session` with:

- `observe` for the current screenshot and bounded page metadata;
- `act` for typed navigation, pointer, keyboard, scrolling, or wait actions;
- `checkpoint` for an explicit Profile checkpoint;
- `close` to close the current attachment.

The tool schema intentionally has no Run, Agent, principal, Session,
attachment, epoch, credential, lease, proxy, or Provider-key argument. The
trusted broker adds the immutable identity tuple
`(runtime_session_id, session_epoch, attachment_id)` and the Browser control
epoch.

## Isolation and credentials

The Provider Runtime owns the Provider API key or local login. The Browser
container never receives it. The child model process receives only the trusted
broker socket path; it cannot read the Browser channel credential or active
lease.

The Browser container exposes no TCP control port, CDP, WebDriver, VNC, or host
browser. Chromium can reach external destinations only through the egress
gateway. Private, loopback, link-local, metadata, rebinding, direct DNS, QUIC,
WebRTC UDP, and DoH bypasses are denied.

Do not enter passwords, tokens, payment data, or other secrets into a page
unless the user explicitly authorizes that exact action and deployment policy
allows it.

## Session reuse and fencing

Core conversation identity selects a private Browser Session. Follow-up Runs in
the same conversation may reuse its Provider Session and Browser Profile.
Every Run gets a new attachment and control epoch.

A Provider session recovery, Runtime reattachment, cancellation, expiry, or
completion invalidates the old attachment. Late actions are rejected against
the full immutable identity and cannot affect a replacement connection.

Different conversations or principals cannot reuse the same active Browser
attachment. A dedicated private Browser Agent is the deployment boundary for a
persistent logged-in Profile. Plaintext Profile state exists only in Browser
tmpfs while active; checkpoints are encrypted with a Browser-only root key and
an identity-derived wrapping key. Inactive Profiles expire after 30 days.

## Durable history

Screenshots, frames, page metadata, and individual Browser actions are
transient and must not pass through the durable Runtime event submission path.
OpenLinker stores only a fixed number of coarse Browser lifecycle events and
the final Run result. Durable event count must not grow with `action_count`.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `browser_session` is unavailable | Confirm the compatible CLI exposes `plugin.browser.serve`, then restart or reload the host. |
| Browser Runtime is unavailable | Confirm the Browser compose override is active and the private socket, channel credential, and lease mounts match. |
| Agent diagnosis rejects capacity | Browser profile requires `capacity: 1`. |
| Attachment is stale | Let the current Run retry with its new attachment; never reuse or edit an old lease. |
| Page cannot reach a private address | This is expected egress policy, not a Browser failure. |
| Ordinary Agent changed behavior | Confirm `execution_profile` remains `standard`; Browser is not loaded in that profile. |

Browser failures do not authorize a fallback to a host browser or an
unrestricted network client.
