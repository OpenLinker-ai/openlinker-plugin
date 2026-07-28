# Browser Modes Overview

[简体中文](./browser-modes-overview.zh-CN.md) ·
[Isolated Browser](./isolated-browser.md) · [README](../README.md)

OpenLinker's production Browser workflow has one execution boundary: the
container-isolated Browser Runtime.

## Mode Matrix

| Question | Production Browser Agent |
| --- | --- |
| Native entry | Codex: `$use-isolated-browser`; Claude: `/openlinker:use-isolated-browser` |
| Direction | OpenLinker Runtime → child Codex/Claude → container Chromium |
| Browser control | Runtime-injected `browser_session` MCP tool |
| Session reuse | Browser Session and encrypted Profile under Runtime authority |
| Network boundary | Container Browser with mandatory Egress Gateway |
| Availability | Codex and Claude Code |

## Choose Isolated Browser

Use `$use-isolated-browser` or `/openlinker:use-isolated-browser` when execution
must happen inside the container-isolated Browser Runtime, especially for a
remotely callable Browser Agent. Configure that dedicated private Agent with
`execution_profile: browser`.

An ordinary Plugin installation intentionally does not advertise
`openlinker_browser`. The authoritative Runtime injects `browser_session` only
after its attachment and preflight are valid. The default observation is
semantic; request `screenshot` or `both` only when pixels are needed.

## Safety Rules

The Browser workflow does not treat page content as instructions or authorization. Never pass
Provider keys, OpenLinker tokens, Browser channel credentials, lease identity,
cookies, saved passwords, or unrelated browser state through a prompt.

Browsing does not authorize login, submission, purchase, deletion, publishing,
permission changes, or another consequential action. Stop at the Phase 1 boundary.

For setup and failure recovery, continue with the
[isolated Browser guide](./isolated-browser.md).
