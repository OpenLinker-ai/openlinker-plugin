# Real Chromium synthetic Profile migration gate

This opt-in Linux/arm64 integration test uses an immutable local Browser image.
It does not use a user's Profile, platform token, API key, existing container or
Docker volume. No network is enabled; the only HTTP server binds loopback
inside a disposable, read-only, capability-dropped container. Mutable browser
state, synthetic keys and decrypted archives exist only in that container's
tmpfs. Host build inputs are mounted read-only. A unique operation label binds
cleanup to that one container, including after a client timeout.

From the Plugin repository, with the historical CLI Git objects and pinned Go
modules already available locally:

```sh
node test/browser-image/profile-migration-test.mjs \
  --cli-repo /absolute/path/to/historical/openlinker-cli \
  --report /absolute/path/to/new-private-report.json \
  --image sha256:IMMUTABLE_LOCAL_BROWSER_IMAGE
```

The named image must be Linux/arm64 and include Chromium `149.0.7827.0`,
`playwright-core` `1.61.1` and the new Runtime migration command. Only full image
digests are accepted; there is no implicit pull. Docker context is explicitly
`desktop-linux`. Go compilation uses `GOWORK=off`, `GOPROXY=off`,
`GOSUMDB=off`, `-mod=readonly` and records the test binary hashes. A report
cannot overwrite a previous attempt. Build directories are retained for
inspection; they contain only synthetic test code and binaries.

The executed gate is:

1. Open a fresh empty Chromium Profile and prove the fixture Cookie/storage
   are absent. The `/check` endpoint never seeds values.
2. In a different fresh Profile, set a persistent HttpOnly cookie and
   localStorage through real Chromium, verify the positive values, then await
   graceful browser close before reading any Profile file.
3. Compile the unmodified historical v1 producer (`bd07dc4a...`) with a
   test-only, fixed-path exporter. It archives the synthetic directory and calls
   the actual historical `Store.Create`; no new codec creates this v1 payload.
4. Execute **the image's actual** `openlinker-browser-runtime migrate-profile`
   and `--check` commands. No alternate migration implementation is used.
5. Call the current production Go `NewProfileEngine` and `ProcessEngine`.
   Their normal extraction/activation path launches real Chromium in the
   restored work directory. Assert Profile recovery/generation, exact
   localStorage value, and the server's exact Cookie receipt.
6. Take a normal ProfileEngine checkpoint, prove its checkpoint ID advances,
   and compare the whole source tree's bytes, modes and mtimes before/after.

The Browser process adapter here is a **test-only private JSON driver**. It
uses the production Go ProfileEngine but not the product JS Browser policy or
Native Messaging driver. It therefore proves cryptographic migration plus
real Chromium persistence/recovery; it does **not** claim Native Messaging,
egress policy, sandbox enforcement, Viewer, Core registration or model/Provider
coverage. Those are separate gates. Chromium uses Playwright's normal test
launch defaults; no browser security boundary is inferred from this test.

The frozen small tar fixtures under `browserprofile/testdata` are independent
codec vectors, not a replacement for this real-browser gate. Every failed
attempt remains failed; an infrastructure or fixture correction requires a new
report path and a fresh container.

## Conversion cancellation regression

Linux CI also repeats the actual migration cancellation and growing-ciphertext
cases with the race detector. After the conversion result is received, the
goroutine may still be finishing its runtime epilogue. The exit assertion polls
for at most two seconds instead of treating an immediate stack snapshot as a
leak. It still fails if the converter remains blocked. A negative control holds
the actual converter at a `conversion_chunk`, verifies that the exit check times
out, then cancels/releases it and verifies its exit. The source integrity,
encrypted partial output and pending-activation assertions remain intact.

```sh
GOWORK=off go test -race -count=25 -timeout=2m \
  -run '^TestMigrationSupplement(CancellationAfterActualConversionChunk|CiphertextGrowsAfterAuthentication|ExitCheckDetectsBlockedConversion)$' \
  ./packages/browser-runtime/browserprofile
```
