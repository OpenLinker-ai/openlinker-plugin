# ChatGPT App release target

This directory prepares the remote ChatGPT App without changing the execution
path of the local Codex and Claude Code plugins.

`app-config.example.json` is an operator checklist, not an upload artifact and
not proof that a connector exists. A release pipeline may generate the plugin
root `.app.json` only after ChatGPT creates a real connector ID for the
production MCP endpoint. Never commit a guessed or placeholder connector ID.

## Authentication gate

The production MCP server exposes private Runs, Events, Artifacts, Tasks, and
write actions. Its ChatGPT connection therefore requires OAuth 2.1 with:

- protected-resource and authorization-server discovery metadata;
- authorization-code flow with S256 PKCE and exact redirect URI validation;
- the MCP `resource` parameter bound to the production MCP origin;
- per-user access tokens with least-privilege OpenLinker grants;
- rotating refresh tokens, reuse detection, expiry, logout, and revocation;
- a supported ChatGPT client identity mechanism (CIMD, DCR, or a predefined
  client) and an explicit token endpoint authentication method;
- `401` challenges that advertise protected-resource metadata;
- audit events that never store plaintext tokens or authorization codes.

Core's Google/GitHub sign-in OAuth and its `ol_user_*` tokens do not satisfy
this gate by themselves. Do not copy a User Token into a ChatGPT manifest, use
a browser JWT as an MCP token, or proxy all users through one service account.

## Browser composition

`skills/browse-and-run-agent/SKILL.md` defines the optional workflow that uses
ChatGPT's separately installed Browser capability and OpenLinker MCP tools in
one task. It deliberately does not install Browser, automate localhost/private
targets, transmit cookies or screenshots to OpenLinker, or treat page content
as instructions.

The workflow becomes releasable only after both the OAuth gate and production
MCP scans pass. Until then this directory is design and validation material.
