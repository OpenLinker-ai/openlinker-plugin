import assert from "node:assert/strict";
import { chmod, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { mkdtemp } from "node:fs/promises";

import {
  PAGE_CONTINUATION_CONTRACT_ID,
  PageContinuationCorruptError,
  PageContinuationStore,
  pageContinuationPath,
  pageContinuationSessionKey,
} from "./page-continuation.js";
import type { Identity } from "./protocol.js";

test("persists an exact Session page without raw identity leakage", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
  const identity = fixtureIdentity(1);
  const store = new PageContinuationStore(root);
  await store.record(
    identity,
    "https://example.com/path?state=kept#not-persisted",
    Date.parse("2026-07-26T00:00:00.000Z"),
  );

  const file = pageContinuationPath(root);
  const raw = await readFile(file, "utf8");
  assert.ok(raw.includes(PAGE_CONTINUATION_CONTRACT_ID));
  assert.ok(raw.includes("https://example.com/path?state=kept"));
  assert.ok(!raw.includes("#not-persisted"));
  for (const forbidden of [
    identity.run_id,
    identity.agent_id,
    identity.principal_scope_id,
    identity.browser_session_id,
    identity.attachment_id,
  ]) {
    assert.ok(!raw.includes(forbidden), `continuation leaked ${forbidden}`);
  }
  assert.equal((await stat(file)).mode & 0o077, 0);

  const restarted = new PageContinuationStore(root);
  assert.equal(
    await restarted.lookup(
      identity,
      Date.parse("2026-07-26T00:01:00.000Z"),
    ),
    "https://example.com/path?state=kept",
  );
});

test("restores a fenced reattachment but never another Browser Session", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
  const original = fixtureIdentity(1);
  const store = new PageContinuationStore(root);
  await store.record(original, "https://example.com/session-one");

  assert.equal(await store.lookup(original), "https://example.com/session-one");
  assert.equal(await store.lookup(fixtureIdentity(2)), undefined);
  assert.equal(
    await store.lookup({ ...original, session_epoch: 2 }),
    "https://example.com/session-one",
  );
});

test("bounds state with retention and LRU eviction", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
  const store = new PageContinuationStore(root);
  const start = Date.parse("2026-07-26T00:00:00.000Z");
  for (let index = 0; index < 33; index++) {
    await store.record(
      fixtureIdentity(index + 1),
      `https://example.com/session-${index + 1}`,
      start + index * 1000,
    );
  }
  assert.equal(
    await store.lookup(fixtureIdentity(1), start + 34_000),
    undefined,
  );
  assert.equal(
    await store.lookup(fixtureIdentity(33), start + 34_000),
    "https://example.com/session-33",
  );

  const expired = new PageContinuationStore(root);
  assert.equal(
    await expired.lookup(
      fixtureIdentity(33),
      start + 31 * 24 * 60 * 60 * 1000,
    ),
    undefined,
  );
});

test("removes a Session continuation when the page is not persistable", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
  const identity = fixtureIdentity(1);
  const store = new PageContinuationStore(root);
  await store.record(identity, "https://example.com/allowed");
  await store.record(identity, "about:blank");
  assert.equal(await store.lookup(identity), undefined);
});

test("writes only on URL change or an explicit checkpoint", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
  const identity = fixtureIdentity(1);
  const store = new PageContinuationStore(root);
  const file = pageContinuationPath(root);
  await store.record(
    identity,
    "https://example.com/stable",
    Date.parse("2026-07-26T00:00:00.000Z"),
  );
  const initial = await readFile(file, "utf8");
  await store.record(
    identity,
    "https://example.com/stable",
    Date.parse("2026-07-26T00:01:00.000Z"),
  );
  assert.equal(await readFile(file, "utf8"), initial);
  await store.record(
    identity,
    "https://example.com/stable",
    Date.parse("2026-07-26T00:02:00.000Z"),
    true,
  );
  const checkpointed = await readFile(file, "utf8");
  assert.notEqual(checkpointed, initial);
  assert.ok(checkpointed.includes("2026-07-26T00:02:00.000Z"));
});

test("rejects malformed, permissive, and duplicate state", async () => {
  const cases: unknown[] = [
    { contract_id: PAGE_CONTINUATION_CONTRACT_ID, entries: [], extra: true },
    {
      contract_id: PAGE_CONTINUATION_CONTRACT_ID,
      entries: [
        {
          session_key: "a".repeat(64),
          url: "http://127.0.0.1/",
          updated_at: "2026-07-26T00:00:00.000Z",
        },
      ],
    },
    {
      contract_id: PAGE_CONTINUATION_CONTRACT_ID,
      entries: [0, 1].map(() => ({
        session_key: "a".repeat(64),
        url: "https://example.com/",
        updated_at: "2026-07-26T00:00:00.000Z",
      })),
    },
  ];
  for (const [index, value] of cases.entries()) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-page-"));
    const file = pageContinuationPath(root);
    await mkdir(path.dirname(file), { mode: 0o700 });
    await writeFile(file, JSON.stringify(value), { mode: 0o600 });
    await assert.rejects(
      new PageContinuationStore(root).lookup(fixtureIdentity(index + 1)),
      PageContinuationCorruptError,
    );
  }

  const permissiveRoot = await mkdtemp(
    path.join(tmpdir(), "openlinker-page-"),
  );
  const permissiveFile = pageContinuationPath(permissiveRoot);
  await mkdir(path.dirname(permissiveFile), { mode: 0o700 });
  await writeFile(
    permissiveFile,
    JSON.stringify({
      contract_id: PAGE_CONTINUATION_CONTRACT_ID,
      entries: [
        {
          session_key: pageContinuationSessionKey(fixtureIdentity(1)),
          url: "https://example.com/",
          updated_at: "2026-07-26T00:00:00.000Z",
        },
      ],
    }),
    { mode: 0o600 },
  );
  await chmod(permissiveFile, 0o644);
  await assert.rejects(
    new PageContinuationStore(permissiveRoot).lookup(fixtureIdentity(1)),
    PageContinuationCorruptError,
  );
});

function fixtureIdentity(index: number): Identity {
  const suffix = index.toString(16).padStart(12, "0");
  return {
    run_id: "11111111-1111-4111-8111-111111111111",
    agent_id: "22222222-2222-4222-8222-222222222222",
    principal_scope_id: "ps1_fixture_scope",
    browser_session_id: `33333333-3333-4333-8333-${suffix}`,
    session_epoch: 1,
    attachment_id: "44444444-4444-4444-8444-444444444444",
    control_epoch: 1,
    controller: "agent",
    browser_interaction_policy: "restricted",
    browser_interaction_policy_generation: 1,
    browser_mutation_origins: [],
    browser_mutation_origins_sha256:
      "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
  };
}
