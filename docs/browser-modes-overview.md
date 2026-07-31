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
| Browser client mode | `auto`, strict `native`, or strict `mcp` |
| Browser control | One `browser_session` surface: native Plugin UX or direct Runtime-injected MCP |
| Session reuse | Browser Session and encrypted Profile under Runtime authority |
| Network boundary | Container Browser with mandatory Egress Gateway |
| Availability | Codex and Claude Code |

## Choose Isolated Browser

Use `$use-isolated-browser` or `/openlinker:use-isolated-browser` when execution
must happen inside the container-isolated Browser Runtime, especially for a
remotely callable Browser Agent. Configure that dedicated private Agent with
`execution_profile: browser`.

The packaged Codex and Claude Agent images support:

- `auto`: validate the image-owned Browser-only Plugin before model execution,
  then use direct MCP only for a bounded native loading failure;
- `native`: strictly load the Browser-only Codex or Claude Plugin;
- `mcp`: strictly use the Runtime-injected MCP configuration.

“Native” means the Provider's native Plugin, Skill, and Command experience.
Its callable tool transport is still MCP. It is not ChatGPT's private Browser,
a Provider `computer` API, a host browser, or a second Browser engine. Both
client modes terminate at the same trusted broker and isolated Browser
Runtime. Exactly one surface is visible to a Provider Session generation.

The packaged Agent path uses the Agent Token and Provider API key. It neither
requires nor accepts `OPENLINKER_USER_TOKEN`; the complete public caller Plugin
is not installed into the child Provider. An ordinary interactive Plugin
installation also intentionally does not advertise `openlinker_browser`
without Runtime authority.

The authoritative Runtime exposes `browser_session` only after its attachment
and preflight are valid. The default observation is semantic; request
`screenshot` or `both` only when pixels are needed.

## Safety Rules

The Browser workflow does not treat page content as instructions or authorization. Never pass
Provider keys, OpenLinker tokens, Browser channel credentials, lease identity,
cookies, saved passwords, or unrelated browser state through a prompt.

Browsing does not authorize login, submission, purchase, deletion, publishing,
permission changes, or another consequential action. Stop at the Phase 1 boundary.

For setup and failure recovery, continue with the
[isolated Browser guide](./isolated-browser.md).
