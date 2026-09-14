# Offline Browser Profile contract migration

This source entrypoint migrates one explicitly identified encrypted Profile from
`openlinker.browser.v1` to `openlinker.browser.v2`. Source implementation, released
images, and a particular deployment's acceptance are separate facts: use only an
independently verified Browser Runtime build containing this command and the
pending-migration startup guard. An older Runtime must not mount the new target.

The command is for a trusted local operator, not an Agent/model tool or a remote
API. It does not change Core authorization, register a Worker, obtain credentials,
start Chrome, initialize the normal Browser service, or merge existing Profiles.
It is supported only on Linux; other platforms return a fixed failure report.

## Commands

```sh
openlinker-browser-runtime migrate-profile --request-file /run/migration/request.json
openlinker-browser-runtime migrate-profile --check --request-file /run/migration/request.json
openlinker-browser-runtime migrate-profile --finalize --request-file /run/migration/request.json
```

`--check` and `--finalize` are mutually exclusive. Omitting both requests a new
migration. Supply `--request-file` exactly once as a separate flag/value pair;
unknown, repeated, positional, and `--request-file=...` arguments are rejected.
There are no key-value, stdin, force, automatic-retry, or ignore-marker options.
The request path must be absolute and canonical.

The operation has a ten-minute context budget, bounded further by any earlier
caller deadline. SIGINT and SIGTERM cancel it cooperatively. Local filesystem
I/O is not guaranteed to be interruptible at every kernel operation.

## Request file

The UTF-8 JSON request must be an owner-only regular non-symlink file, at most
16 KiB. Duplicate or unknown members and trailing content are rejected. Paths
and identities belong in this private file, not in a model prompt. Do not commit
it. Possession of the request is not a Core authorization grant.

The following is a field template, not an executable request. Replace every
placeholder from the exact authorized source; do not substitute current/latest
values when a precondition differs.

```json
{
  "schema": "openlinker.browser.profile-migration.v1",
  "operation_id": "<operation-uuid>",
  "source_contract": "openlinker.browser.v1",
  "target_contract": "openlinker.browser.v2",
  "source_store": "/source/encrypted-profiles",
  "destination_store": "/destination/new-encrypted-profiles",
  "root_key_file": "/key/profile-root-key",
  "expected_identity": {
    "agent_id": "<exact-agent-uuid>",
    "principal_scope_id": "<exact-authorized-principal-scope>",
    "profile_slot": "default",
    "profile_generation": 1
  },
  "root_key_generation": 1,
  "source": {
    "profile_directory_sha256": "<source-directory-digest>",
    "metadata_sha256": "<metadata-file-sha256>",
    "checkpoint_id_sha256": "<checkpoint-id-sha256>",
    "payload_sha256": "<encrypted-payload-file-sha256>",
    "current_pointer_mtime": "<exact-original-RFC3339Nano-time>"
  },
  "retention": {
    "mode": "preserve"
  }
}
```

Both Profile and root-key generations are exact preconditions, not defaults to
discover. The existing paired root key is loaded only from its explicit file;
the command does not create a missing key or perform root-key rotation. Source
and destination must be distinct, non-nested, non-aliased stores. The private
destination parent must already exist; the final store must not exist, even as
an empty directory.

`preserve` retains the original pointer mtime and refuses an expired Profile. An
explicitly approved restoration instead supplies this retention object:

```json
{
  "mode": "restore-approved-anchor",
  "anchor": "<previously-approved-RFC3339Nano-time>",
  "authorization_sha256": "<operator-authorization-sha256>",
  "prior_restoration_receipt_sha256": "<previous-restoration-receipt-sha256>"
}
```

The anchor must be the already approved instant, not the time of another copy
or retry. Future or expired anchors are rejected. This association is local
operation evidence, not a new network authorization mechanism.

## Isolation and activation

Stop every source/destination writer and verify the intended Agent, volumes,
spools and absence of inflight work separately. Mount the source and existing
key read-only. Use a fresh independent destination, a non-root Linux process,
no network, a read-only container rootfs, dropped capabilities, no-new-privileges,
and resource limits. Do not mount platform/provider credentials, SDK state,
provider homes, or an unrelated workspace into the migration process.

Migration authenticates the complete v1 stream and writes fresh v2 encryption
without extracting or rewriting its archive. It authenticates the target and
compares complete archive bytes internally. Plaintext, plaintext digests, key
material, URLs and Browser data are not report fields. Source contents and
mtime are not modified or quarantined on failure.

A published reservation is **not** an activatable Profile. A pending marker and
the shared manager lock block normal Runtime use until complete verification,
receipt/retention persistence and marker removal succeed. Preserve failed
encrypted targets and markers; do not manually remove the marker, empty a
directory, or retry migration against an existing target.

`--check` performs read-only authentication and evidence checks using existing
locks. `--finalize` is only for the exact operation whose complete target was
written but whose pending marker remains. It repeats full verification before
removing the marker. An incomplete, changed, unrelated, or in-use target is not
finalizable. An already finalized exact target is checked without rewriting it.

## Output and recovery

The command emits one JSON report to stdout and exits zero only on success.
Failure codes/types and stages are fixed, safe values; raw filesystem or JSON
errors are never printed. Reports include `published` and `activation_ready`,
which must be interpreted independently of the exit code.

If stdout itself fails, the command attempts a safe JSON report on stderr with
`failure_code: "output_failed"`, `stage: "output"`, `completed_stage`, and
`outcome_uncertain: true`, retaining the actual publication/activation flags.
The command never retries a migration because report delivery failed. Inspect
the original request and encrypted target, run `--check`, and use `--finalize`
only when its exact-operation requirements hold.

Cryptographic conversion does not prove normal Browser consumption. Before
enabling the retained Worker, independently validate the selected new Browser
image, exact migrated Profile/key mounts, and unchanged runtime identity and
security configuration. A subsequent real Browser Run must load the migrated
Profile and advance its checkpoint without creating a new empty Profile.
Preserving bytes does not guarantee external website sessions remain valid.
