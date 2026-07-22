# Changelog

All notable changes to the OpenLinker Skills and native plugins are documented
here. This package is pre-1.0 and remains Developer Preview until its pinned
CLI release and cross-platform installation matrix pass.

## 0.1.0 - Unreleased

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
