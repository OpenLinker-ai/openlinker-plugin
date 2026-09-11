# Contributing to OpenLinker Plugin

Chinese documentation: [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)

This repository owns native Codex/Claude packages and the reusable Provider and
Browser execution implementation. Native archives include verified Plugin host binaries.

## Development setup

```bash
npm install
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
npm --prefix packages/browser-runtime/browser-engine ci --ignore-scripts
npm run test:browser-engine
npm run test:native-chrome
```

Root npm tooling has no runtime dependencies; the engine has its own pinned npm
graph. State when optional host validators, Docker, credentials, or immutable
release artifacts are unavailable.

## Scope boundaries

- `packages/agent-adapters`: Provider/session execution, capability selection,
  Agent configuration/diagnostics/lifecycle, and SDK Browser extensions.
- `packages/browser-runtime`: pure Browser protocols/client/tool server,
  Profiles, engine/native assets, Browser services, network policy, and egress.
- Native manifests, canonical Skills, small packages, locked CLI resolution,
  Dockerfiles, portable compose, and image/Provider regression gates belong here.
- Plugin owns native MCP/Agent/Browser command composition in `internal/pluginhost`;
  Cobra is confined there. Plugin must never import CLI; the platform CLI has no execution adapters.
- Pure Browser packages/services must not transitively import SDK. Keep the
  existing SDK Runtime Worker as the sole delivery/recovery implementation.
- Core/Cloud behavior, Agent Node application changes, and twv1 operations do
  not belong here. Never embed secrets, runtime state, or browser executables
  in native host archives.

## Pull request expectations

Preserve command/MCP/wire contracts, credential isolation, Session identity,
Profile formats, UID/GID and volume names. Update both documentation languages,
keep canonical Skills byte-identical, and preserve fail-closed artifact and
publication gates. Boundary scans must match real packages and check transitive
imports; a missing source/test root must not silently pass.

Local temporary workspaces/proxies are allowed for source validation, not as
evidence of a published dependency. Do not add permanent relative `replace`
directives. Describe release order and compatibility impact explicitly.

## Release checks

Follow [RELEASE.md](./RELEASE.md). Module CI/release is independent of CLI. Native/image publication verifies the Plugin
host, exact SDK/Node module checksums, platform and source evidence. Credentials or unavailable artifacts
must be reported as unrun/blocked acceptance, never as passing evidence.

## Security and license

Follow [SECURITY.md](./SECURITY.md) for private vulnerability reports.
Contributions use this repository's Apache-2.0 license.
