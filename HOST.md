# Plugin-owned execution host

`openlinker-plugin-host` owns native MCP, deep Agent Mode, Browser tool serving
and private Browser/delegation proxies. It uses the existing Plugin application
and official SDK Runtime Worker. Ordinary Codex/Claude bridging belongs to Agent
Node; the platform CLI is used only for caller Skills and platform commands.

```sh
GOWORK=off go build -o /tmp/openlinker-plugin-host ./cmd/openlinker-plugin-host
/tmp/openlinker-plugin-host context
/tmp/openlinker-plugin-host plugin serve --host codex
/tmp/openlinker-plugin-host agent doctor --provider claude
```

`agent configure/serve/status/doctor`, `plugin serve/browser-serve/browser-proxy/
delegation-proxy/capabilities` retain their prior arguments after replacing the
executable name. The MCP server name, fourteen tool IDs and agenthost v1 private
handshake stay compatible. Configuration fields/defaults, secrets, identity,
state/session/Profile paths and single-instance lock behavior are unchanged.
Caller MCP tools still use only User Tokens; Agent execution uses Agent credentials.

Native releases package six Plugin host binaries with SHA-256, exact Plugin source,
SDK/Node module versions and build information. Launchers select only their host
platform, verify the binary, then exec it with original stdio/arguments/signals.
Node.js 20+ is required by the package resolver. Source checkouts contain no release
binaries: build a clean committed checkout using `scripts/package-native-hosts.mjs`
and install its output, or set `OPENLINKER_PLUGIN_HOST_BIN` to a built host with its
adjacent `host-build-info.json`. A missing/mismatched host fails explicitly; tasks
never download or silently fall back to the CLI. CLI locks/installers remain for
standalone caller Skills only and do not select the MCP/Worker executable.

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
