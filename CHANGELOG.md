# Changelog

All notable changes to the OpenLinker Skills and native plugins are documented
here. This package is pre-1.0 and remains Developer Preview until its pinned
CLI release and cross-platform installation matrix pass.

## 0.1.2 - Unreleased

- Adopt Node's macOS native Codex descendant cleanup (Node #46) and its bound
  fix (Node #47). Plugin's Codex provider now keeps observed tool children and
  grandchildren from surviving cancellation after they leave the original
  process group, and long successful turns with many short-lived commands no
  longer fail the tracking bound. A canceled or timed-out request preserves
  `ErrProcessCleanup` instead of hiding incomplete cleanup, and a resume
  attempt that reports both a missing session and incomplete cleanup returns
  that failure instead of retrying with a new session (Node #48). This is bounded
  observed-ancestry cleanup, not hostile double-fork containment; Linux and
  Windows keep their existing process-group behavior. Execution sources change,
  so adoption requires a newly published host and regenerated marketplace locks.

- Fix the Linux Profile migration test's premature goroutine-leak assertion:
  allow a bounded exit interval after receiving the final conversion result,
  with a real blocked-converter negative control and repeated race checks in CI.
  Production migration behavior is unchanged. Package tests participate in the
  host source digest, so adoption still requires a newly published host and
  regenerated marketplace locks. Keep the pinned official Codex local API
  regression on upgrades; the live upstream `max_messages` limit remains open.

- Adopt Node's shared Codex message-limit stop: interrupt the active turn at
  the first scoped `max_messages` diagnostic and return a fixed failure without
  repeated native retries or partial success. Preserve the native session for
  an explicit follow-up when reuse is enabled. Ordinary transient retries remain
  unchanged. The installed Codex local API regression separately checks both
  paths after an actual Browser MCP result, including session continuation and
  no extra automatic model request. No real model credentials are required.
  This limits retry waste; the upstream cause remains unconfirmed. Publish a
  new host and regenerate all installation locks before merging.

- Return `BROWSER_PROTOCOL_INVALID` with fixed, actionable messages for invalid
  Browser MCP arguments, including actions on observe and oversized or unsafe
  batches. Rejected calls do not execute Browser actions or poison the MCP
  connection. Unexpected internal errors remain redacted; action limits and
  authority/policy checks are unchanged. New host publication is required.

- Preserve Browser action deadline errors through the local Runtime transport.
  After the action deadline, the client allows up to five seconds to receive
  Runtime cleanup and its structured response. Caller cancellation/deadlines
  still interrupt immediately; unresponsive transports remain bounded and are
  not reported as confirmed action failures. No action is automatically retried.

- Report configured/effective WebSearch policy and override source through CLI
  and MCP configuration, doctor and Worker status. Preserve requested file values,
  reject invalid search overrides before writing, and retain the last Worker
  snapshot when the status caller uses a different environment. Older status
  files leave the policy unknown. Search remains opt-in; diagnostics do not prove
  model/gateway availability. Execution-source adoption requires a new published
  host and regenerated installation locks.

- Add an offline Linux-only Browser Profile v1-to-v2 migration command with exact
  identity/source preconditions, authenticated archive preservation, a fresh
  encrypted destination, pending-activation guard and operation-bound reports.
  Read-only authentication failures preserve the target in place. Initial
  `--check`/`--finalize` verification refuses after normal checkpoint or mtime
  advancement; retain the receipt and encrypted pre-use baseline. Approval hashes
  are format-checked audit associations, not approval verification or Core
  authorization: the trusted operator owns approval binding. Legacy, unknown and
  malformed expired Profiles require explicit operator cleanup and capacity
  management; normal valid inactive v2 expiry is retained. Non-Linux platforms,
  including Windows, report a fixed refusal rather than enabling migration.
  Browser Runtime image acceptance and a newly published Host/marketplace lock
  remain separate release gates; this unreleased entry does not advance either.

- Pin the verified Go SDK module at `63fc87d73406`, adopting the gRPC
  1.83.2 and protobuf dependency updates while retaining the SDK Go 1.25 baseline.
  Pin Node `v0.1.58-rc.3` for the shared protocol leaves. Command, credential,
  session and persistent-state contracts are unchanged. The caller CLI locks
  adopt the six-platform public `v0.2.0-rc.16` release. Pin Host `v0.1.58-rc.9`
  from its public archives and checksums; source marketplace installation is
  verified on Linux, macOS and Windows.

- Align the SDK module, Node leaf module, and Provider build pin with the shared
  SDK contract synchronization. Production Go sources retain their existing
  behavior. Pin CLI `v0.2.0-rc.15` and Plugin host `v0.1.58-rc.8` using locks
  generated from their verified public six-platform release artifacts.

- Use Node's shared `codexturn` leaf for Codex app-server preparation, turn
  events, cancellation and shutdown. Plugin retains Browser installation,
  launch flags and progress policy. Canceled requests no longer start a new
  app-server; failed Browser installation still prevents thread/turn creation.
- Add minimal Codex and Claude Agent Runtime Browser Plugin artifacts for
  packaged Provider images.
- Document restricted/full Browser interaction, exact mutation-origin scopes,
  side-effect uncertainty, and the rule that page content cannot grant broader
  authority to an Agent.
- Document mutually exclusive `auto`, `native`, and `mcp` Browser client modes.
- Keep caller and Agent-control surfaces, User Tokens, and Provider credentials
  out of the packaged Agent Runtime artifacts.

## 0.1.0 - 2026-07-22

- Added standalone Agent discovery/invocation and Run inspection Skills.
- Added native Codex and Claude Code plugin manifests, marketplaces, Claude
  slash commands, and separate native packages backed by a shared local-CLI
  workflow surface.
- Added explicit bidirectional Agent Mode with token-only Runtime registration,
  Core-owned conversation continuity, and private provider-session reuse.
- Added explicit, checksum-verified CLI setup for six OS/architecture targets.
- Added ChatGPT App OAuth readiness gates and a host Browser composition Skill
  without publishing a placeholder connector ID.
- Added complete English-first, Chinese-secondary native initialization and
  usage guides for caller and Agent modes, including configuration contracts.
- Fixed native Codex MCP startup to use the installed Plugin root, explicitly
  inherit only supported host variables, and support validated Codex API Base
  URLs, non-Git workspaces, and Codex client tool metadata through the pinned CLI.
