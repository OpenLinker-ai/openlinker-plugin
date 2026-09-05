# Browser image acceptance

This release acceptance gate drives the production Browser Runtime and Egress
Gateway images through the real UDS contract and a real headless Chromium.
It is separate from unit tests and runs in the Provider image PR/tag workflow,
because it creates a short-lived public HTTPS fixture through a pinned
Cloudflare Quick Tunnel. The tunnel has no uptime guarantee and is not a
production dependency.

Run from the CLI repository:

```sh
./test/browser-image/run.sh
```

The workflow runs that exact entry point on matching native GitHub-hosted
amd64 and arm64 runners for both release architectures. A local Docker
installation with the corresponding native support or QEMU/binfmt registration
can replay either half explicitly:

```sh
DOCKER_DEFAULT_PLATFORM=linux/amd64 ./test/browser-image/run.sh
DOCKER_DEFAULT_PLATFORM=linux/arm64 ./test/browser-image/run.sh
```

The harness inspects the resulting Browser image architecture and selects the
matching repository-pinned Chromium version fixture; changing the platform is
not a build-only substitute for actually starting Chromium and completing the
packet-observed matrix.

The script exits non-zero when Docker, the public fixture, any image build, or
any assertion is unavailable; it has no skip path. The Provider image workflow
runs the same entry point before multi-architecture image builds, including
tagged releases. A successful unit-test or image-build job is not a substitute
for this gate.

If the pinned Quick Tunnel service is unavailable, an operator may supply two
equivalent temporary HTTPS origins backed by the same fixture image:

```sh
OPENLINKER_BROWSER_ACCEPTANCE_FIXTURE_URL=https://fixture.example \
  OPENLINKER_BROWSER_ACCEPTANCE_COLLECTOR_URL=https://collector.example \
  ./test/browser-image/run.sh
```

The origins must be distinct. The override is not a skip: the harness verifies
both fixture markers, exercises the same Browser/Egress path, and checks the
shared fixture metrics and packet trace.
If that endpoint requires a one-time public GET to establish non-sensitive
routing state, set `OPENLINKER_BROWSER_ACCEPTANCE_PRIME_URL` as well. The
priming URL is validated by the same public-target policy and is navigated only
after Browser preflight.

For an automatically created Quick Tunnel, readiness requires both an assigned
HTTPS URL and cloudflared's registered-connection event. The host does not
preflight that URL because host TLS interception can differ from the isolated
Browser/Egress path; the subsequent real Chromium title, semantic marker,
status counters, and packet assertions are the authoritative fixture check.
An operator-supplied URL still receives the host marker check before use.

For local diagnosis only, set
`OPENLINKER_BROWSER_ACCEPTANCE_KEEP_ON_FAILURE=1` to preserve the
prefix-named test containers, networks, and volumes after a failed run. The
default and CI behavior always clean them up.

The gate builds the current Browser, Egress, fixture, client, and packet
observer images, then proves:

- the Browser container has one internal network and no published ports;
- the dual-homed Egress Gateway starts on its public network before its
  internal attachment, and a bounded secure-DNS readiness probe from the exact
  Gateway network namespace proves that its default route is public;
- HTTPS CONNECT can move to another address only within one bounded,
  fully-public secure-DNS answer set when the first public edge is unavailable;
- Browser preflight starts Chromium and checks the real Gateway;
- preflight reports the repository-locked Browser version, locale, timezone,
  distribution, and font-manifest evidence;
- the real Runtime UDS and independent shared Browser MCP server instances with
  `Host=codex` and `Host=claude` expose exactly one `browser_session` tool, emit
  environment evidence only on the first tool result in one MCP session, then
  emit it again from a replacement MCP server instance representing
  Provider/MCP recovery; this harness does not launch either provider CLI, so
  credential-backed native-Plugin/direct-MCP host equivalence remains a
  separate release gate, and the Provider lifecycle test remains responsible
  for the real process boundary;
- a public HTTPS page can be observed and screenshotted;
- a non-autofocused search box can be focused without pointer activation,
  typed into, and submitted only as a public GET search;
- `restricted` rejects button activation and prevents POST, WebSocket, and
  Service Worker traffic from reaching the fixture;
- `full` still rejects state-changing input before dispatch when the committed
  Document origin is outside the Owner-authorized exact-origin set;
- `full` on an Owner-authorized exact origin activates a real button through
  the pinned element path and admits the fixture's same-origin POST and
  WebSocket only through the Egress Gateway;
- a one-way durable toggle is dispatched exactly once and remains committed
  after reload, while the same control remains unchanged under `restricted`;
- same-origin POST, PUT, PATCH, DELETE, form submission, checkbox, radio,
  select, Enter, Space, and custom-control pointer/mouse/click ordering work in
  real Chromium;
- a real ordered batch scrolls the page and reports an exact completed-action
  prefix when a later nested action fails;
- `full` sequential input exercises both US-layout and non-layout Unicode
  event sequences in real Chromium, and a focus-stealing fixture proves that
  the remaining units stop with a no-retry unknown-outcome result;
- state-changing requests and WebSockets to the second, unallowlisted public
  origin are blocked before reaching the fixture, including top-level and
  child-frame actions, while an allowlisted same-origin child frame succeeds;
- a mutation whose server-side commit is followed by response loss returns
  `BROWSER_MUTATION_OUTCOME_UNKNOWN`, stays usable, and is never retried;
- old clients are rejected through the real UDS path after either policy
  generation or mutation-origin set/digest changes;
- private literals, private DNS, redirect-to-private, and controlled DNS
  rebinding fail closed;
- checkpoint/restore works for the same Browser Session and preserves a real
  secure-cookie login-state fixture across the replacement attachment;
- an ambiguous challenge remains gated across `pushState`, is released only
  after a clean cross-document classification, and is reclassified on a real
  back/forward cache restore;
- three ordinary 403 documents remain recoverable, while a fourth attempt is
  rejected locally without a fourth fixture request;
- a 429 `Retry-After` is bounded and enforced locally without another request;
- a high-confidence challenge fences and terminates only its attachment, after
  which a fresh attachment can preflight successfully;
- a different Browser Session starts blank;
- closing an attachment survives a new UDS client connection;
- 40 identical read-only actions under independent `restricted` and `full`
  Attachments report p50/p95/p99 latency on the same public fixture;
- direct TCP and DNS/UDP from the Browser namespace do not escape;
- stopping the Gateway produces a recoverable egress error without direct
  fallback; and
- Chromium has the required QUIC, DoH, and WebRTC restrictions and exposes no
  remote-debugging TCP port.

The no-bypass half is behavioral, not a launch-flag proxy. A test-only
`tcpdump` observer shares the Browser Runtime network namespace and captures
all interfaces while real Chromium:

- resolves and opens a randomized public HTTPS hostname;
- attempts a same-origin WebTransport connection;
- performs a WebRTC offer with a public STUN endpoint; and
- retries public navigation after the Gateway is stopped.

The live observer uses libpcap immediate mode to avoid capture buffering. It
is a test-only image and is built for the Docker server's native architecture,
then joins the target Browser container with `--network container:<runtime>`.
CI runs both the observer and production Browser/Egress/client/fixture matrix
natively on their matching architecture. A local cross-architecture replay
may still execute the production and behavioral components through QEMU while
keeping the observer on the host PF_PACKET ABI, avoiding unsupported QEMU
TPACKET translation. The resulting pcap contains the target Browser network
namespace's real packets and is read back by the same three assertions below.

The captured trace must contain real TCP traffic to the Egress Gateway, zero
UDP packets, and zero TCP packets to any non-Gateway destination. Chromium
launch flags remain a secondary configuration assertion. Separate direct
TCP/DNS probes then prove the container network itself also has no fallback.
The DNS-rebinding probe uses a unique per-run hostname with a one-second TTL.
The harness first proves the public phase from the same Docker public network,
then real Chromium sends the same hostname through the production Gateway and
must receive its permanent private-target block. This prevents recursive DNS
caches from turning the two-phase assertion into a stale external-service
result.

The harness never receives an OpenLinker token, Provider key, Browser Profile
key, or channel credential. Its fixture and client images are test-only and
are not referenced by production Compose or image targets.

The release-only credential-backed Provider matrix is implemented separately
at `test/browser-provider-live/run.sh`. It reuses this exact Browser Runtime,
Gateway and dynamic HTTPS fixture, but launches the pinned Codex and Claude
Provider images in both native Plugin and direct MCP modes. It is intentionally
not part of ordinary pull-request acceptance because it requires both external
Provider credentials; tag and explicit release workflows run it fail-closed.
