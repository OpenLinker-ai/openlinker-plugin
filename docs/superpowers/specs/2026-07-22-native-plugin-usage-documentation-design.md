# Native Plugin Usage Documentation Design

**Date:** 2026-07-22
**Status:** Approved

## Objective

Document the complete native OpenLinker Plugin lifecycle for Codex and Claude
Code. A user must be able to install the plugin, initialize caller credentials,
call another OpenLinker Agent, configure the current host as an Agent, diagnose
and enable Agent Mode, verify session reuse, and disable it without reading the
implementation or guessing at MCP tool names.

English is the canonical documentation language. Every user-facing guide has a
secondary Chinese translation with the same headings, commands, configuration
semantics, and safety boundaries.

## Information Architecture

- `README.md`: English five-minute quick start and guide index.
- `README.zh-CN.md`: secondary Chinese quick start and guide index.
- `docs/calling-agents.md`: native Use Mode guide.
- `docs/calling-agents.zh-CN.md`: Chinese Use Mode guide.
- `docs/serving-as-agent.md`: native Agent Mode guide.
- `docs/serving-as-agent.zh-CN.md`: Chinese Agent Mode guide.
- `docs/configuration.md`: authoritative configuration reference.
- `docs/configuration.zh-CN.md`: Chinese configuration reference.

The guides are mode-oriented rather than host-oriented. Codex and Claude Code
differences appear as parallel subsections so shared product behavior is stated
once and does not drift between two complete host manuals.

## Native Invocation Contract

Codex exposes the bundled Skills through explicit `$` mentions and implicit
matching:

- `$openlinker`
- `$setup-openlinker-cli`
- `$serve-openlinker-agent`

Claude Code plugin Skills and Commands are namespaced by the plugin name:

- `/openlinker:openlinker`
- `/openlinker:openlinker-setup`
- `/openlinker:openlinker-agent`

The documentation must not advertise the unnamespaced `/openlinker`,
`/openlinker-setup`, or `/openlinker-agent` forms. Codex users start a new task
or CLI session after installation. Claude Code users run `/reload-plugins` after
installation or enablement changes.

Natural-language invocation remains supported. The explicit native forms are
shown first because they provide deterministic onboarding and troubleshooting.

## Caller Initialization and Use Mode

Use Mode requires:

- `OPENLINKER_API_BASE`: Hosted or self-hosted Core public base URL.
- `OPENLINKER_USER_TOKEN`: least-privilege `ol_user_*` token.

Credentials are supplied outside prompts, project files, and command-line
arguments. Codex desktop users are shown the host environment file and restart
requirement; terminal hosts inherit variables set before launch. The first
verification is `openlinker context`, which is local-only and does not print the
token. The guide then shows native discovery, explicit run authorization,
asynchronous progress inspection, artifacts, and cancellation.

The configuration reference maps each operation to its minimum Core grant and
states that `agents:run` can be scoped to a single Agent.

## Agent Initialization and Agent Mode

Agent Mode is disabled by default. Native configuration invokes the local MCP
bridge's `configure_agent_mode` tool and persists only non-secret values:

- provider (`codex` or `claude`);
- existing OpenLinker Agent UUID;
- existing minimal workspace directory;
- public OpenLinker URL;
- optional state path, model, transport, capacity, timeout, session reuse,
  web-search, sandbox, approval, permission, and allowed-tool policy.

The operator supplies `OPENLINKER_AGENT_TOKEN` or its `_FILE` alternative
outside model context. Provider authentication uses an existing local login or
`CODEX_API_KEY` / `ANTHROPIC_API_KEY` and their mutually exclusive `_FILE`
alternatives. `OPENLINKER_NODE_ID` is optional and generated once in private
state when absent.

The documented order is configure, diagnose, explicitly enable, inspect status,
exercise a multi-turn conversation, and explicitly disable. Enabling persists
the preference, so the Runtime Worker restarts with the enabled plugin host.
Plugin Agent Mode is tied to the host lifecycle; supervised CLI or production
images are the documented 24x7 alternatives.

The guide explains that Core owns the conversation key while the local Runtime
stores a private mapping to the provider-native session. Raw Codex or Claude
session IDs never appear in OpenLinker results or model-visible configuration.

## Configuration Storage

The reference documents:

- default and override locations for `agent.json`;
- private state and generated Node ID locations;
- caller, Agent, and Provider credentials as separate trust domains;
- direct environment and `_FILE` mutual exclusion;
- owner-only file requirements for secret files;
- environment override precedence for non-secret Runtime options;
- default values and accepted enums;
- redacted diagnostics and common initialization errors.

Configuration examples use placeholders only. No example contains a realistic
secret, and no workflow asks the model to receive or write credentials.

## Validation

Repository tests will verify:

- every English guide has its expected Chinese companion;
- README links resolve to existing local files;
- exact Codex and namespaced Claude native entrypoints appear in both languages;
- unnamespaced Claude forms are not advertised as invocations;
- caller and Agent required configuration names remain documented;
- the documented MCP Agent-control tool names match the shared Agent surface;
- no secret-shaped example is introduced.

Existing package parity, manifest, Skill, release-lock, installer, and secret
checks remain unchanged. Documentation changes must not alter runtime manifests,
Skills, CLI locks, or executable behavior.

## Sources of Truth

- Plugin Skills and Commands in `platforms/codex/openlinker` and
  `platforms/claude/openlinker`.
- Caller and Agent contracts in `shared/contracts`.
- CLI configuration and command behavior in `openlinker-cli/pkg/agent`.
- Current official Codex Plugin and Skill documentation.
- Current official Claude Code Plugin, command namespace, reload, and MCP
  lifecycle documentation.
