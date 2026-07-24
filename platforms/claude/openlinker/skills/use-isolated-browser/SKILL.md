---
name: use-isolated-browser
description: "Use or explain the OpenLinker client-owned Browser tool for Codex or Claude Code. Use when the user asks to browse from an isolated Browser Runtime, configure a Browser Agent, inspect Browser readiness, or distinguish Browser tool calls from OpenLinker Agent calls."
---

# Use the OpenLinker Isolated Browser

The Browser is a client tool registered with the executing Codex or Claude
client. It is not a Provider `computer` API and it is not an OpenLinker Agent
capability probe.

## Choose the Correct Mode

- For an interactive local client, use `browser_session` only when the operator
  has connected the Plugin to an isolated Browser Runtime.
- For a callable Browser Agent, configure Agent Mode with
  `execution_profile: browser`. The Runtime injects the same Browser tool into
  the child Codex or Claude client automatically.
- Do not call an unrelated OpenLinker Agent merely to test whether the current
  model can use Browser tools.

## Browser Tool Contract

Use the `browser_session` tool with one of these operations:

- `observe`: capture the current screenshot and bounded page metadata;
- `act`: submit a small ordered list of typed browser actions;
- `checkpoint`: request a profile checkpoint;
- `close`: close the current attachment.

Do not invent or pass `run_id`, Agent ID, principal scope, Browser Session ID,
Session epoch, attachment ID, control epoch, channel credentials, lease paths,
or Provider credentials. The trusted Runtime supplies identity and
authorization outside model arguments.

Never enter passwords, API keys, tokens, payment details, or other secrets into
a page unless the user explicitly authorizes that exact action and the
deployment policy permits it. Stop before high-impact or irreversible actions.

## Failure Handling

If the tool reports that Browser Runtime configuration is unavailable, explain
which non-secret path or container component is missing. Do not fall back to
direct CDP, WebDriver, VNC, a host browser, or unrestricted HTTP navigation.

Browser frames and actions are transient. Only coarse Run lifecycle evidence
and the final result belong in OpenLinker durable Run history.
