---
description: Use or explain the client-owned isolated Browser tool
argument-hint: [request]
---

Use the bundled `use-isolated-browser` skill and the `browser_session` tool.
The Browser is a client tool, not a Provider `computer` API. Never request or
accept channel credentials, lease contents, server-authoritative attachment
identity, Provider API keys, User Tokens, or Agent Tokens in this command.
If `browser_session` is not registered in this client, report that and stop; do
not use a browser extension or any other browser instead.

User request: $ARGUMENTS
