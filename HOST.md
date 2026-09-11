# Plugin-owned execution host

`openlinker-plugin-host` owns native MCP, deep Agent Mode, Browser tool serving
and private Browser/delegation proxies. It uses the existing Plugin application
and official SDK Runtime Worker. Ordinary Codex/Claude bridging belongs to Agent
Node; the platform CLI is used only for caller Skills and platform commands.

Install from a Git marketplace or release package, then run `$setup-plugin-host`
(Codex) or `/openlinker:install-plugin-host` (Claude). Manual equivalent:

```sh
node "<installed-plugin-root>/scripts/install-plugin-host.mjs" --plan
node "<installed-plugin-root>/scripts/install-plugin-host.mjs"
node "<installed-plugin-root>/scripts/resolve-plugin-host.mjs" --require plugin.serve
```

No Go or OpenLinker CLI is needed: Node.js 20+, curl and tar suffice. Reload plugins
or start a new task afterward. The setup Skill remains available when MCP cannot
start. This installs the executable without enabling Agent Mode.

`agent configure/serve/status/doctor`, `plugin serve/browser-serve/browser-proxy/
delegation-proxy/capabilities` retain their prior arguments after replacing the
executable name. The MCP server name, fourteen tool IDs and agenthost v1 private
handshake stay compatible. Configuration fields/defaults, secrets, identity,
state/session/Profile paths and single-instance lock behavior are unchanged.
Caller MCP tools still use only User Tokens; Agent execution uses Agent credentials.

Host releases publish six separate platform archives. Git marketplace and native
packages contain the same `host-lock.json`, installer and resolver; no package
bundles six foreign-platform binaries. Explicit setup downloads only the selected
archive and adjacent checksum, validates them against the pinned archive digest,
extracts only the regular binary and metadata members, checks the binary digest
and source/version/capability self-check, then atomically installs both files.

The default cache is `~/Library/Application Support/OpenLinker/Plugin` on macOS,
`$XDG_DATA_HOME/openlinker/plugin` (or `~/.local/share/openlinker/plugin`) on Linux,
and `%LOCALAPPDATA%/OpenLinker/Plugin` on Windows. `OPENLINKER_PLUGIN_DATA` overrides
it; installer and resolver must receive the same value. Each host lives under
`hosts/<version>/<platform>/<archive-sha256>/`. Existing versions stay available
on failed upgrades. Repeated setup reuses a verified installation; concurrent
installs cannot expose a half-installed binary/metadata pair. A damaged existing
pin is rejected: explicitly remove only that reported host directory and rerun
setup, leaving Agent configuration/session/state untouched.

Launchers verify and exec the pinned installed host, preserving stdio/arguments.
They never search PATH for an execution host, fall back to CLI, or download during
tasks. `OPENLINKER_PLUGIN_HOST_BIN` is an explicit development/offline override,
requiring a regular binary and its adjacent verified `host-build-info.json`.
Developers can build a clean committed checkout with `scripts/build-plugin-host.mjs`.
CLI locks/installers remain only for standalone caller Skills.

Publish host archives from a tested immutable tag first, then run
`node scripts/generate-host-lock.mjs <tag> --write` to verify the actual public
archives and update all three locks. `npm run check:host-lock` is required in CI
and native packaging. Merge marketplace changes only after clean installation of
that public release succeeds. Native source packages may be released afterward;
the host lock identifies the binary source independently of delivery-only changes.

Provider images build this host from their archived Plugin source, require
`--build-arg OPENLINKER_PLUGIN_COMMIT=<full archived commit>`, and expose immutable
`/opt/openlinker/host-build-info.json` readable by the Worker UID. The image preparation
script binds the commit to `git archive`; build metadata verifies both SDK and Node
module pins/checksums, the executable package and target platform. No CLI archive
is downloaded or embedded. Browser Runtime/Chrome remain separate processes/images.

Old native packages still require their pinned full CLI. Upgrade the package and
host together before upgrading CLI; do not change a running Worker merely by
replacing its binary. Drain/stop it, preserve identity/session/spool/state, then
start the Plugin host. Agent Node migration is a separate procedure with its own
configuration defaults. Keep the previous binary/image for rollback.
