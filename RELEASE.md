# Release Process

Chinese documentation: [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)

Plugin owns a reusable Go module, small native host archives, and separate
Provider/Browser images. These are different release stages. The private npm
package is repository tooling, not an npm publication.

## Release order

1. Test the Go module with `GOWORK=off`, run the transitive dependency gate,
   and publish an immutable Plugin module tag. This source/module stage must
   not depend on a new downstream CLI archive.
2. Pin that published module and real checksums in CLI. Run its standalone
   tests and all six release targets, then publish the CLI archives.
3. Update `shared/cli-lock.json` and both host copies from that published CLI.
   Commit the lock in a subsequent native-package release tag; never move an
   existing module tag to insert the new lock.
4. Explicitly dispatch the Release workflow on the chosen immutable tag with
   `publish_native`. It verifies the CLI artifact before native packaging.
   Explicitly dispatch Provider images with both `run_live_provider` and
   `publish_images` only after all credential-backed/image gates pass.

The tag-triggered `go-module` job is independent of native CLI-lock readiness.
A local workspace or temporary module proxy validates local source only. It
does not prove the Plugin dependency is publicly available. Do not release
relative `replace` directives, invented checksums, or unverified candidates.

## Artifact gates

Run these source checks before tagging:

```bash
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
GOWORK=off go mod verify
GOWORK=off go build ./cmd/...
```

After the compatible CLI artifacts exist:

```bash
npm run lock:cli -- v0.x.y --write
npm run release:check
npm run check:provider-cli -- --expected-sdk v0.2.0-rc7
```

Replace the example CLI version with the actual published release. The
Provider CLI gate verifies the archive SHA-256 from the shared lock, target
OS/architecture, immutable Plugin and SDK module versions/checksums, absence
of replacement modules, and the CLI binary's real 40-character `vcs.revision`
from a clean Git checkout (`vcs.modified=false`).
Old CLI locks fail until a migrated CLI is released. Images record the verified
`cli_commit`, `cli_release`, `cli_archive_sha256`, `plugin_module_version`,
and `openlinker_go_version` in `/opt/openlinker/cli-build-info.json`.

Provider images download the locked CLI, not CLI source. Their minimal native
Browser packages are built deterministically from the same Plugin checkout,
without downloading this repository's own unpublished release. Native host
archives stay limited to manifests, Skills, commands, and CLI resolvers; do not
include Go sources, browser executables, private state, or credentials.

## Acceptance and rollback

Validate native manifests/parity/installers, engine and Native Messaging tests,
Provider sessions/cancellation, Browser identity/Profile contracts, composed
service topology, network isolation, live Codex/Claude Browser runs, SBOMs and
provenance before image publication. Missing credentials or unavailable immutable
artifacts are blockers, not passing acceptance.

Do not deploy twv1 from this repository's release workflow. Host-specific
acceptance and deployment are root operations and require separate authorization.
Source pull requests run real Browser/Egress acceptance without requiring a
downstream CLI artifact. Provider image builds run on explicit workflow dispatch;
publication additionally requires an immutable tag and a successful live gate.
Keep the previous immutable CLI/Plugin/image set available for rollback.
Never migrate, re-key, delete, or recreate runtime volumes as part of this
source ownership change.

## Tagging

Publishing is an explicit maintainer action. Use immutable semantic-version
tags; native-package tags must match the version contract enforced by repository
checks. Document pre-1.0 breaking changes in `CHANGELOG.md`.
