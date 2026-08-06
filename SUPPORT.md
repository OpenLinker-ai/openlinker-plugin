# Support

Use GitHub issues for reproducible bugs, documentation problems, and feature
requests that fit OpenLinker Plugin's public scope.

## Good Issue Topics

- Codex or Claude plugin installation and manifest validation
- shared Skill, command, or local MCP entrypoint behavior
- pinned CLI resolution, checksum verification, or supported-platform packaging
- Use Mode, Agent Mode, isolated Browser, or documented configuration problems
- release checks and public plugin archive contents

## Before Opening an Issue

- Search existing issues and recent commits.
- Confirm the problem on the latest `main` branch or a named release.
- Include operating system, host version, Node.js version, plugin commit or tag,
  and OpenLinker CLI version.
- Include reproduction steps, expected behavior, actual behavior, and sanitized logs.
- Redact tokens, provider credentials, private URLs, customer data, local state,
  and provider-session material.

## Not Supported Here

- vulnerabilities; follow [SECURITY.md](./SECURITY.md)
- Core server protocol, database, scheduling, or authentication implementation
- Cloud billing, wallet, order, or hosted marketplace behavior
- SDK Runtime Worker or Agent Node implementation bugs that do not originate in
  the plugin package
- private deployment debugging without reproducible public details

For cross-repository issues, include the relevant Plugin, CLI, SDK, and Core
commit or release identifiers so ownership can be established without moving
the implementation across repository boundaries.
