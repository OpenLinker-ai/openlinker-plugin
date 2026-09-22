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

## Private skill packages

The current source supports owner-associated, version-pinned skill packages from
Core schema 093 on native Codex/Claude hosts and correctly provisioned Provider
images. Hosts advertise `skill_packages.v1` and their provider-specific
`skill_packages.codex.v1` or `skill_packages.claude.v1` feature through the existing
SDK Worker. Agent Node's native adapters consume the same contract. Source
changes still require new releases and explicit rollout to existing installations.

Official images provision a separate `/skills` cache, owned by Runtime UID 10001
and skill-readers GID 10003. The entrypoint and UID-switch launcher retain that
supplementary group; Provider UID 10002 can read package directories (02750) and
files (0440), but cannot modify them or read private `/runtime` state. The standard
compose files mount a 64 MiB, noexec, nosuid, nodev tmpfs at `/skills`; `/workspace`
can remain read-only. Restarted hosts reconstruct pinned packages from assignments.
Skill selection changes do not require rebuilding the image. Custom Docker runs
must supply the same writable cache mount when using `--read-only`.

Images configure `OPENLINKER_SKILL_PACKAGES_CACHE_DIR=/skills` and
`OPENLINKER_SKILL_PACKAGES_GROUP_ID=10003`. Advertisement and execution validate
the shared cache's owner, group and permissions, then create/write/remove a probe
file on the actual mount. A read-only image layer therefore does not advertise
package support even if its permission bits are correct. Missing/unsafe caches and legacy
launchers without this configuration remain unsupported, and the explicit
`OPENLINKER_SKILL_PACKAGES_DISABLED=true` override remains available. Custom
wrappers that change identity must provision equivalent access and validate it;
merely mounting a writable workspace is insufficient.

The Agent owner imports SKILL.md and UTF-8 supporting files, then associates an
exact version in the skill workbench. Core supplies an immutable per-Run snapshot
in reserved assignment metadata. The host verifies its SHA-256, provider, paths
and trusted execution context before materializing files under
`<workspace>/.openlinker-skills/<agent-id>/<payload-sha256>/` for native hosts or
`/skills/<agent-id>/<payload-sha256>/` in Provider images. Existing files must
be byte-identical; they are never overwritten. For a damaged same-UID native
cache, the loader materializes a new verified copy, reuses it on later Runs and
starts a fresh session so stale file paths are not retained. A same-UID model
process can still attack the shared workspace or Host state; this is recovery
from prior cache damage, not a new isolation boundary. Package files have no executable
bit, so scripts are used through the appropriate interpreter. For Git workspaces,
the host only updates `info/exclude` when the workspace itself is a repository
and the gitdir/exclude remain inside it. Git runs with fsmonitor and system/global
config disabled and no inherited GIT_* overrides. A cache-local .gitignore covers
nested/external worktrees without modifying other repositories.
Already tracked cache files cause loading to fail instead of extending that leak.
Native caches stay within the provider sandbox's readable workspace. Image
Claude invocations include only the selected package directories with `--add-dir`;
OS permissions keep these directories read-only to the Provider.
Declared commands are located in the configured Provider PATH; official image
launchers check after dropping to the Provider UID and clearing capabilities.
No prerequisite executable is run by this check. It verifies lookup/access, not
shared libraries, credentials or every tool policy at execution time. Import and
loading do not install dependencies or expand tool, credential, network or Browser
permissions. Package files may be reproduced through model output: private API
access is not a confidentiality boundary against Agent callers.

For a new native session, the host includes SKILL.md instructions and supporting
file locations in the actual Codex/Claude request. Resumed sessions receive a small SKILL.md path index instead of the full
instructions again. Missing-session recovery injects them into the new
replacement session. A changed association snapshot selects a fresh
native session while retaining the platform conversation/history and existing
session files. Unchanged versions can resume normally. SDK durable load receipts
distinguish prepared instructions from a failed load or missing command; they do
not certify model use, task success or benchmark performance. Cache versions stay
available for older Runs and are not automatically garbage-collected in V1.

Long native sessions may automatically compact their context. Every turn includes
an index of the selected SKILL.md locations and instructs the model to reread lost
instructions. This supports recovery without repeatedly injecting full files;
a load receipt still does not prove that the model followed those instructions.

Shared parsing, immutable file preparation, session digests and instruction
fragments come from Node's `pkg/adapters/skillpackages` leaf. Plugin retains image
permissions, Browser policy and its own Provider/session execution.

## Shared app storage

The Agent application consumes the pinned Agent Node `pkg/adapters/appfiles`
leaf for config/status/identity writes, strict decoding, private secret reads and
cross-process locking. It retains its config defaults and paths, `.agent-mode.lock`
name, directory creation and acquire/release lifetime. This is compile-time reuse;
no Node executable is required. Existing platform-specific limitations are unchanged.

The pinned Node module defaults its own Codex/Claude product entries to native
session isolation, with an explicit operator opt-out. Its native mode reuses a
trusted host client's authentication while isolating tools and conversation storage;
it remains experimental and limited to trusted callers. Updating that dependency
does not enable session sandboxes in Plugin,
move Browser into Node, or extend the Linux Codex launcher credential proxy to
Node/Claude. Plugin retains its Provider/Browser process and container policies.

The unused `agent.ContextWithSignals` helper has been removed from the application
package. Embedding hosts should own their signal lifecycle with `signal.NotifyContext`.
Browser key-rewrap and lease inspection helpers live only in tests; they are not
new runtime capabilities. Execution-source changes still require a new published
host and regenerated marketplace locks before merging, as described above.
