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
  is already inside an authoritative isolated Browser Runtime. The ordinary
  Plugin manifest intentionally does not advertise a Browser MCP server.
- For a callable Browser Agent, configure Agent Mode with
  `execution_profile: browser`. The Runtime injects the same Browser tool into
  the child Codex or Claude client automatically.
- Do not call an unrelated OpenLinker Agent merely to test whether the current
  model can use Browser tools.

## Browser Tool Contract

Use the `browser_session` tool with one of these operations:

- `observe`: return bounded semantic page state by default;
- `act`: submit a small ordered list of typed browser actions;
- `checkpoint`: request a profile checkpoint;
- `close`: close the current attachment.

For `observe` or `act`, set `observation` to `semantic`, `screenshot`, or
`both`. Omit it for the cheaper `semantic` default. Screenshot data is never
included implicitly. A batch returns one final observation; on failure,
`completed_actions` reports how many earlier actions completed.

Phase 1 permits public navigation, ordinary public links, non-sensitive
text/search fields, and public GET search forms. It rejects credentials,
buttons, custom activation controls, select controls, Space activation,
state-changing page requests, and high-impact actions.

Semantic observations do not expose click coordinates. Before a coordinate
click, request `screenshot` or `both` and use the returned `viewport`,
`screenshot.width`, and `screenshot.height`. Coordinates are expressed in the
reported Browser viewport, not in whatever resized image the model client
displays. Do not reuse coordinates after `page_state_id` or
`navigation_generation` changes.

A permitted coordinate click has exactly two effects:

- a public link is activated and reports `click_effect: activated` with
  `target_category: link`;
- a non-sensitive text/search input is focused without pointer, mouse, click,
  or submit activation and reports `click_effect: focused` with
  `target_category: text_input`.

No other element is clickable in Phase 1. In particular, do not repeatedly
try buttons or custom controls. A blocked click returns only a Runtime-checked
coarse target category, page state, and remaining navigation/Run retry
budgets. After one blocked click, request a fresh screenshot and use the
category feedback. Attempt at most twice against one unchanged
`page_state_id`; then stop and report the limitation even if Runtime budget
remains.

`type_non_secret` writes only to the currently focused, policy-approved text
input. Keep text non-sensitive and bounded; existing or resulting values over
16 KiB fail closed rather than being truncated. Press Enter only after the
same safe input has been focused and typed, and only for a public GET search
flow.

Do not invent or pass `run_id`, Agent ID, principal scope, Browser Session ID,
Session epoch, attachment ID, control epoch, channel credentials, lease paths,
or Provider credentials. The trusted Runtime supplies identity and
authorization outside model arguments.

Never enter passwords, API keys, tokens, payment details, or other secrets into
a page. Agent control rejects credential fields even when a user requests the
action. If Runtime pauses for human control, stop issuing Browser actions and
wait in the same model session. Only the authenticated Run owner may use the
Viewer's bounded pointer, keyboard, text, and scroll controls. Stop before
high-impact or irreversible actions.

## Failure Handling

Treat stable site outcomes explicitly:

- keep the attachment for `BROWSER_ACCESS_DENIED`,
  `BROWSER_RATE_LIMITED`, `BROWSER_ORIGIN_RATE_LIMITED`, and
  `BROWSER_CHALLENGE_SUSPECTED`;
- respect `retry_after_ms` without automatic retry, identity/Profile/proxy
  switching, or Browser fallback;
- do not click, type, submit, upload, or solve a suspected challenge;
- if `challenge_release_unavailable` is true, navigate away or stop because
  this attachment cannot clear the suspected-state restriction; and
- when `human_control_available` is true, explain that the same isolated
  Browser Session is paused for its owner; do not retry, switch identity,
  create a second model session, or attempt to solve the challenge; and
- when human control is unavailable, explain that
  `BROWSER_CHALLENGE_REQUIRED` fenced and closed the attachment.

If the tool reports that Browser Runtime configuration is unavailable, explain
which non-secret path or container component is missing. Do not fall back to
direct CDP, WebDriver, VNC, a host browser, or unrestricted HTTP navigation.

Browser frames and actions are transient. Only coarse Run lifecycle evidence
and the final result belong in OpenLinker durable Run history.

Human control does not change Browser/Profile identity or egress. Page POST
and page WebSocket traffic are enabled only for the current human control
epoch; proxy-only egress, private-network blocking, sandboxing, download
blocking, secret redaction, lease fencing, and Run deadlines remain active.
The Viewer never exposes CDP, WebDriver, VNC, arbitrary scripts, file upload,
clipboard access, or host-browser fallback. On release, the Runtime restores
the restrictive Agent page policy before control can return to the model.
