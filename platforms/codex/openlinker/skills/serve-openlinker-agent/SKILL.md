---
name: serve-openlinker-agent
description: "Configure, diagnose, enable, disable, or explain OpenLinker Agent Mode for Codex or Claude Code. Use when the user wants this coding agent to be callable as an OpenLinker Runtime Agent, reuse provider sessions, or prepare a headless deployment."
---

# Serve This Host as an OpenLinker Agent

Agent Mode is disabled by default. Use the local Agent-control MCP tools inside
the Plugin. For a standalone installation use `openlinker agent` commands.

## Configure Without Secrets

Collect and save only:

- provider: `codex` or `claude`;
- an existing OpenLinker Agent UUID;
- a minimal workspace directory;
- the public OpenLinker platform URL;
- optional state directory, model, capacity, timeout, sandbox/permission, web
  search and session-reuse preferences.

Use `configure_agent_mode` or `openlinker agent configure`. Never accept an
Agent Token, Provider API key, secret file contents, or Provider session ID in
tool arguments, prompts, config JSON, shell history, or Run metadata.

The operator supplies `OPENLINKER_AGENT_TOKEN` or
`OPENLINKER_AGENT_TOKEN_FILE` outside model context. Provider authentication
uses the provider's supported local login state or `CODEX_API_KEY` /
`ANTHROPIC_API_KEY` and their optional `_FILE` alternatives. Direct and file
forms are mutually exclusive.

## Diagnose and Enable Explicitly

Run `diagnose_agent_mode` first. Report only present/missing/source categories.
Do not print secret values. Explain that native Plugin mode is tied to the host
lifecycle and is not a 24/7 production service.

Only call `enable_agent_mode` after the user explicitly asks to enable it.
Enabling starts a token-only reliable Runtime Worker. If discovery requires an
unsupported security policy, report `RUNTIME_SECURITY_POLICY_UNSUPPORTED`; do
not bypass discovery or silently downgrade.

Use `get_agent_mode_status` for redacted status. Call `disable_agent_mode` only
on explicit request; it gracefully drains before stopping.

## Session and Isolation Rules

Core owns conversation keys and prior persisted messages. The local Runtime
hashes that key into an owner-only Provider session mapping and never returns a
raw Codex or Claude session ID. A missing Provider session may fall back once
using Core-owned history.

Local Plugin mode provides software boundaries and the host sandbox. For 24/7
or strong network isolation, use the corresponding production image or supervise
`openlinker agent serve --provider <provider>` with persistent private state.

