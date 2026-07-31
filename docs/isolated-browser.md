# Use the Isolated Browser

[简体中文](./isolated-browser.zh-CN.md) ·
[Browser modes](./browser-modes-overview.md) ·
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

The same Browser tool can appear in two authoritative contexts, but the
ordinary Plugin manifest does not register it:

| Context | Who registers `browser_session` | Who supplies attachment authority |
| --- | --- | --- |
| Runtime-attached interactive Codex or Claude host | Explicit Runtime-generated MCP configuration | A separately managed local Browser Runtime deployment |
| Callable Browser Agent, `native` | Image-owned Codex/Claude Browser-only Plugin | Core and the Runtime Worker |
| Callable Browser Agent, `mcp` | Runtime Worker injects an isolated Browser-only MCP configuration | Core and the Runtime Worker |

The packaged images also support `auto`, which validates native Plugin loading
before the model starts and otherwise selects direct MCP for a closed set of
loading failures. It never exposes both surfaces, switches after Browser
actions begin, or uses a policy/site failure to authorize fallback. “Native”
describes Provider Plugin UX; its tool transport remains MCP and both modes
share the same Browser Runtime.

The supported production path is the callable Browser Agent. It requires no
User Token. An ordinary public Plugin install intentionally exposes only the
OpenLinker bridge, so it never shows a Browser tool that is guaranteed to fail.
An interactive host requires an explicit Runtime-generated configuration after
the private socket, channel credential file, active lease, and preflight are
valid. Do not hand-author a lease or put its contents in a prompt.

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
The new Browser overlays select `OPENLINKER_BROWSER_CLIENT_MODE=auto`. Operators
may set strict `native` or `mcp` for diagnosis and rollback. A mode change drains
the old Provider Session generation; it never changes Profile identity. The
operator still supplies the ordinary Agent Token and Provider authentication
outside model context. `OPENLINKER_USER_TOKEN` is unsupported in this packaged
Agent path.

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
/openlinker:use-isolated-browser Open the requested public page and summarize it.
```

The client calls `browser_session` with:

- `observe` for bounded semantic page state by default;
- `act` for typed navigation, pointer, keyboard, scrolling, or wait actions;
- `checkpoint` for an explicit Profile checkpoint;
- `close` to close the current attachment.

For `observe` or `act`, choose `observation: semantic`, `screenshot`, or
`both`. Omitting it selects `semantic`; screenshots are never implicit.
Multi-action batches perform no full intermediate observation and return only
the final one. A failed batch reports `completed_actions`.

Phase 1 permits public navigation, ordinary public links, non-sensitive text
and search fields, and public GET search forms. It rejects credentials,
buttons, custom activation controls, select controls, Space activation,
state-changing page requests, and high-impact actions.

The tool schema intentionally has no Run, Agent, principal, Session,
attachment, epoch, credential, lease, proxy, or Provider-key argument. The
trusted broker adds the immutable identity tuple
`(runtime_session_id, session_epoch, attachment_id)` and the Browser control
epoch.

## Reliability outcomes and evidence

The first structured Browser result in each MCP session/control epoch includes
`attachment_evidence` with the validated Browser engine, distribution, major
version, locale, timezone, and font contract. It is emitted again after a
Provider/MCP recovery and is not repeated on every action.

Handle stable site outcomes without guessing:

- `BROWSER_ACCESS_DENIED`: the current attachment remains usable. After three
  consecutive top-level 403 responses, only that origin is blocked for the
  rest of the attachment.
- `BROWSER_RATE_LIMITED` or `BROWSER_ORIGIN_RATE_LIMITED`: respect the bounded
  `retry_after_ms`; do not retry automatically or switch identity, Profile,
  proxy, or Browser engine.
- `BROWSER_CHALLENGE_SUSPECTED`: do not click, type, submit, or otherwise
  interact with the suspected challenge. Read-only inspection or navigation
  away may remain available. `challenge_release_unavailable: true` means the
  restriction cannot clear in this attachment.
- `BROWSER_CHALLENGE_REQUIRED`: the attachment is fenced and closed. This
  release has no remote Viewer/controller, so the user cannot solve the
  challenge inside that Browser process.

`classifier_rules_version` identifies the released challenge rules. These
outcomes never authorize CAPTCHA solving, a host-browser fallback, proxy
rotation, or automation-evasion behavior.

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
completion invalidates the old attachment. `ready` is emitted only after the
Runtime has validated the lease, Profile, Chromium, Gateway, and a bounded
blank observation. `close` durably revokes the exact attachment, including
across a new MCP connection or Runtime restart. Late actions are rejected
against the full immutable identity and cannot affect a replacement
connection.

Different conversations or principals cannot reuse the same active Browser
attachment. A dedicated private Browser Agent is the deployment boundary for a
persistent logged-in Profile. Plaintext Profile state exists only in Browser
tmpfs while active; checkpoints are encrypted with a Browser-only root key and
an identity-derived wrapping key. The root key and encrypted Profile payload
use separate Browser-only volumes; neither is mounted into the Provider
Runtime. Inactive Profiles expire after 30 days.

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
