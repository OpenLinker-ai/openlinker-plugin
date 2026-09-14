# Changelog

All notable changes to the OpenLinker Skills and native plugins are documented
here. This package is pre-1.0 and remains Developer Preview until its pinned
CLI release and cross-platform installation matrix pass.

## 0.1.2 - Unreleased

- Pin the verified Go SDK module at `63fc87d73406`, adopting the gRPC
  1.83.2 and protobuf dependency updates while retaining the SDK Go 1.25 baseline.
  Pin Node `v0.1.58-rc.3` for the shared protocol leaves. Command, credential,
  session and persistent-state contracts are unchanged. The caller CLI locks
  adopt the six-platform public `v0.2.0-rc.16` release.

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
