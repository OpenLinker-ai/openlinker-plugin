---
name: setup-plugin-host
description: Verify or explicitly install the pinned OpenLinker Plugin host for native MCP, Agent Mode and Browser tools. Use after marketplace installation or when the native MCP server reports a missing host. Works without MCP, Go or the OpenLinker CLI.
---

# Set Up the Plugin Host

Resolve the installed Plugin root from PLUGIN_ROOT, CLAUDE_PLUGIN_ROOT, or this
Skill's location (two directories above this directory). Standalone Skills do not
include a host installer; in that case install the native marketplace Plugin first.

Run `node "<plugin-root>/scripts/install-plugin-host.mjs" --plan` and show the
pinned version, GitHub release URL, current platform, SHA-256 and destination.
Invoking this Skill or `/openlinker:install-plugin-host` is explicit installation
intent; then run the same command without `--plan`. It installs only the locked
host for the current platform into private Plugin data. It needs Node.js 20+,
curl and tar; it does not need Go, administrator rights or the platform CLI.

Run `node "<plugin-root>/scripts/resolve-plugin-host.mjs" --require plugin.serve`
to verify. Reload plugins or start a new task after successful installation so
the native MCP server restarts. Report the installed version and path only.
Do not enable Agent Mode, register an Agent or print credentials during setup.

Do not substitute latest, change the lock, disable TLS/checksums, install a CLI
as a host, or download anything during ordinary tool calls. If installation
fails, report its exact error; checksum failures leave existing versions intact.
If an existing locked installation was damaged, identify that exact immutable
installation directory and get explicit repair intent before removing it and
rerunning setup. Never remove Agent configuration, session or state directories.
