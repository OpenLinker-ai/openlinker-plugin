# Contributing to OpenLinker Plugin

Thanks for helping improve the official OpenLinker plugins for Codex and
Claude Code.

## Development Setup

```bash
npm install
npm test
```

The repository intentionally has no runtime npm dependencies. Some optional
host validators used by the release process require Codex or Claude tooling;
state clearly when those checks are unavailable locally.

## Scope Boundaries

Allowed here:

- native Codex and Claude plugin manifests, Skills, commands, and local MCP entrypoints
- checksum-pinned OpenLinker CLI resolution and installation
- caller, Agent Mode, and isolated Browser guidance and packaging
- ChatGPT App readiness contracts that remain fail closed until their stated prerequisites exist

Out of scope:

- Core protocol, authentication, scheduling, persistence, or server state machines
- Cloud billing, wallets, orders, marketplace operations, or hosted account behavior
- SDK Runtime Worker implementations and Agent Node Adapter behavior
- embedding provider credentials, Agent Tokens, User Tokens, or browser binaries in plugin packages

## Pull Request Expectations

- Keep shared Skills byte-identical across host packages by using the existing generators and checks.
- Do not weaken CLI checksum pins, publication boundaries, secret handling, or opt-in Agent Mode controls.
- Update English and Chinese user documentation together when changing documented behavior.
- Do not add real tokens, private URLs, customer data, local state, or provider-session material.
- Include compatibility and release-ordering notes when changing the pinned CLI contract.

## Checks

```bash
npm test
```

Release-related changes must also pass:

```bash
npm run release:check
```

## Security

Do not open public vulnerability issues. Follow [SECURITY.md](./SECURITY.md).

## License

By contributing, you agree that your contribution is licensed under the
Apache-2.0 license used by this repository.
