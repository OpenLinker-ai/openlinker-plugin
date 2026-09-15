# Independent synthetic Profile compatibility vectors

Every key, identity and archive byte in this directory is public **synthetic test
data**. Never use these keys outside tests. The archive is a deterministic tar
with a marker file, not a real Chrome Profile and not proof of cookie recovery.

`golden-v1.json` is produced by the unmodified CLI implementation at
`bd07dc4a25ba331b9ab80e14ccb88399888ab26c`, before its Browser contract v2 change.
`golden-v2.json` freezes the unmodified Plugin implementation at
`07d03eb28a5c3e530acabdc167638a8c33cce20c`. Provenance lists the exact Git blob and
SHA-256 for every extracted production source file. Both snapshots predate the
new migration codec; the new codec does not generate the v1 acceptance fixture.

The exporter only chooses synthetic inputs, calls the actual historical Store,
and records its output. It implements no cryptography or storage algorithm.
Generation compiles the extracted source in an isolated temporary module, with
the original import paths and exact historical `x/sys` dependency (plus the
v2 protocol's historical `x/net` and `x/text` pins). Unrelated
application dependencies are excluded, not replaced: the unrelated
`browserprotocol/runtime_viewer.go` SDK transport binding is not needed by the
Profile producer and is omitted (recorded in provenance). No selected source
file is changed. `GOWORK=off`,
`GOPROXY=off`, `GOSUMDB=off`, `-mod=readonly` prohibit workspace or network
substitution. Historical Git objects and the pinned dependency modules must already
be present locally; a missing prerequisite fails rather than downloading it.

Reproduce and compare the frozen bytes (default, non-writing):

```sh
node packages/browser-runtime/browserprofile/testdata/regenerate-goldens.mjs \
  --cli-repo /absolute/path/to/openlinker-cli \
  --plugin-repo /absolute/path/to/openlinker-plugin
```

`--write` intentionally regenerates the committed fixtures and must receive
normal code review. The fixtures include the exact directory digest, KDF key,
both AAD record types, encrypted metadata/payload/current-pointer bytes,
checkpoint ID and clear synthetic archive. Fresh test stores copy these bytes;
their pointer mtime is a test-selected recent instant, never real Profile data.

Compatibility tests distinguish native codec checks from Linux-only complete
migration. A Darwin skip is not Linux migration acceptance. Real Chrome
cookie/storage recovery is a separate synthetic integration gate.
