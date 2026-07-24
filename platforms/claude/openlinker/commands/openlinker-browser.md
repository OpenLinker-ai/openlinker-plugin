---
description: Use or explain the client-owned isolated Browser tool
argument-hint: [request]
---

Use the bundled `use-isolated-browser` skill and the `browser_session` tool.
The Browser is a client tool, not a Provider `computer` API. Never request or
accept channel credentials, lease contents, server-authoritative attachment
identity, Provider API keys, User Tokens, or Agent Tokens in this command.

User request: $ARGUMENTS
