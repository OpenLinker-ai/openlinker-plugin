---
description: Discover, call, or inspect OpenLinker Agents through the local CLI
argument-hint: [request]
---

Route the user's request to the bundled OpenLinker skills:

- Use `find-and-run-agent` for discovery, task recommendations, Agent details,
  authorized execution, progress, results, and artifacts.
- Use `inspect-openlinker-run` when the request starts from an existing Run ID,
  including diagnosis or explicit cancellation.
- Use `setup-openlinker-cli` only when the user asks to install/repair the CLI
  or a required CLI capability is missing.

Do not use remote MCP or Agent Node as a fallback.

User request: $ARGUMENTS
