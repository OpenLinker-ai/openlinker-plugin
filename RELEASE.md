# Release Process

Native MCP and deep Agent/Browser execution are built as `openlinker-plugin-host`.
See [HOST.md](./HOST.md) for compatibility and installation.

1. Publish the immutable Node protocol module before consuming its exact version/checksums.
2. Test and tag Plugin source; module CI remains independent of CLI artifacts.
3. Pushing an immutable tag runs the host release job: build six per-platform host
   archives and checksums, validate SDK/Node pins, source commit and target, then
   publish without overwriting assets. For the first host release, source tests
   can run before host locks exist; marketplace CI must not pass without real locks.
   Run `node scripts/generate-host-lock.mjs <tag> --write` against the public release,
   commit the three locks, and verify installation and MCP startup from both Git
   marketplace package directories before merging. Native archives are lightweight:
   dispatch Release with `publish_native` on the later immutable delivery tag.
4. Provider images build the host from the same archived Plugin checkout. Pass
   `OPENLINKER_PLUGIN_COMMIT` bound to that archive. The image exposes root-owned,
   Worker-readable `/opt/openlinker/host-build-info.json`. No CLI artifact is downloaded.
5. Caller Skills retain the independent, checksum-pinned CLI installer. Update its
   three CLI lock copies only from a real published CLI release, never move an existing tag.

Run `npm test`, `npm run test:go`, `npm run check:go-boundaries`,
`npm run check:agent-runtime-integration`, `GOWORK=off go mod verify`, and
`npm run release:check` before publication. `node scripts/package-native-hosts.mjs dist/native` checks host locks and packages
manifests/Skills/commands/installers. No foreign-platform binaries, credentials,
state, Go sources or Browser engine are bundled. Git and archive installations
use the same explicit current-platform installer; ordinary tools never download.

Before publishing images, run engine/native tests, Linux Browser/Egress isolation,
real Provider session/cancellation and Codex/Claude Browser gates, SBOM/provenance.
Keep `run_live_provider` and `publish_images` release checks; missing credentials
or artifacts are unverified, never passing evidence. Published tags must be immutable.
No local replacements, temporary module proxies or fabricated checksums are release evidence.

Deploying twv1 and cutting over a running Worker remain root operations. Drain/stop
the old Worker before changing executable, retain identities/session/Profile/spool
and credentials, and keep previous immutable artifacts for rollback. Old native
packages require their old full CLI; upgrade the Plugin package/host together.
