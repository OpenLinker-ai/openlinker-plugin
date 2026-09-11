# Release Process

Native MCP and deep Agent/Browser execution are built as `openlinker-plugin-host`.
See [HOST.md](./HOST.md) for compatibility and installation.

1. Publish the immutable Node protocol module before consuming its exact version/checksums.
2. Test and tag Plugin source; module CI remains independent of CLI artifacts.
3. Dispatch Release on that clean immutable tag with `publish_native`. It builds
   six host binaries, validates their SDK/Node dependencies, entry point and target,
   records source/version/SHA-256/build info, and bundles them in both native archives.
4. Provider images build the host from the same archived Plugin checkout. Pass
   `OPENLINKER_PLUGIN_COMMIT` bound to that archive. The image exposes root-owned,
   Worker-readable `/opt/openlinker/host-build-info.json`. No CLI artifact is downloaded.
5. Caller Skills retain the independent, checksum-pinned CLI installer. Update its
   three CLI lock copies only from a real published CLI release, never move an existing tag.

Run `npm test`, `npm run test:go`, `npm run check:go-boundaries`,
`npm run check:agent-runtime-integration`, `GOWORK=off go mod verify`, and
`npm run release:check` before publication. From a clean committed checkout,
`node scripts/package-native-hosts.mjs dist/native <tag>` verifies all six targets.
Archives include host binaries and metadata, manifests/Skills/commands; exclude
credentials, state, Go source and Browser engine binaries. Git/source installations
need a separately built/verified host; there is no task-time download or CLI fallback.

Before publishing images, run engine/native tests, Linux Browser/Egress isolation,
real Provider session/cancellation and Codex/Claude Browser gates, SBOM/provenance.
Keep `run_live_provider` and `publish_images` release checks; missing credentials
or artifacts are unverified, never passing evidence. Published tags must be immutable.
No local replacements, temporary module proxies or fabricated checksums are release evidence.

Deploying twv1 and cutting over a running Worker remain root operations. Drain/stop
the old Worker before changing executable, retain identities/session/Profile/spool
and credentials, and keep previous immutable artifacts for rollback. Old native
packages require their old full CLI; upgrade the Plugin package/host together.
