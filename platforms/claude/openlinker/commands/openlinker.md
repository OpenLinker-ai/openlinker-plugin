---
description: Discover, call, inspect, or configure OpenLinker Agents
argument-hint: [request]
---

Route the user's request to the bundled OpenLinker skills:

- Use `find-and-run-agent` for discovery, task recommendations, Agent details,
  authorized execution, progress, results, and artifacts.
- Use `inspect-openlinker-run` when the request starts from an existing Run ID,
  including diagnosis or explicit cancellation.
- Use `serve-openlinker-agent` when the user wants Claude Code to become a
  callable OpenLinker Agent or asks about Agent Mode/session reuse.
- Use `setup-openlinker-cli` only when the user asks to install/repair the CLI
  or a required CLI capability is missing.

Use the bundled local MCP tools. Do not use Hosted MCP or direct Core HTTP as a fallback.

User request: $ARGUMENTS
