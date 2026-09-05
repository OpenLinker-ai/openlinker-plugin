import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  OriginBudgetCorruptError,
  OriginBudgetStore,
} from "./origin-budget.js";
import type { Identity } from "./protocol.js";

const identity: Identity = {
  run_id: "11111111-1111-4111-8111-111111111111",
  agent_id: "22222222-2222-4222-8222-222222222222",
  principal_scope_id: "principal-a",
  browser_session_id: "33333333-3333-4333-8333-333333333333",
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

test("enforces exact windows and restores only checkpointed state", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  let now = 1_000_000;
  const options = {
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 2,
    maxNavigationsPerMinute: 1,
    now: () => now,
  };
  const budget = new OriginBudgetStore(options);
  await budget.load();
  assert.equal(budget.chargeAction(identity, "https://example.com"), undefined);
  assert.equal(budget.chargeAction(identity, "https://example.com"), undefined);
  assert.equal(budget.chargeAction(identity, "https://example.com"), 60_000);
  assert.equal(
    budget.chargeNavigation(identity, "https://example.com"),
    undefined,
  );
  assert.equal(
    budget.chargeNavigation(identity, "https://example.com"),
    60_000,
  );
  await budget.checkpoint();

  const restored = new OriginBudgetStore(options);
  await restored.load();
  assert.equal(restored.chargeAction(identity, "https://example.com"), 60_000);
  now += 60_001;
  assert.equal(
    restored.chargeNavigation(identity, "https://example.com"),
    undefined,
  );
});

test("charges batches atomically by their model-issued action count", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 3,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await budget.load();
  assert.equal(
    budget.chargeAction(identity, "https://example.com", 2),
    undefined,
  );
  assert.equal(
    budget.chargeAction(identity, "https://example.com", 2),
    60_000,
  );
  assert.equal(
    budget.chargeAction(identity, "https://example.com"),
    undefined,
  );
  assert.equal(
    budget.chargeAction(identity, "https://example.com"),
    60_000,
  );
});

test("isolates identity and rejects malformed budget state without deleting it", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  const options = {
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 1,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  };
  const budget = new OriginBudgetStore(options);
  await budget.load();
  assert.equal(budget.chargeAction(identity, "https://example.com"), undefined);
  assert.equal(budget.chargeAction(identity, "https://example.com"), 60_000);
  assert.equal(
    budget.chargeAction(
      { ...identity, principal_scope_id: "principal-b" },
      "https://example.com",
    ),
    undefined,
  );
  await budget.checkpoint();
  const statePath = path.join(root, ".openlinker", "origin-budgets.v1.json");
  const valid = await readFile(statePath);
  assert.ok(valid.byteLength < 256 * 1024);
  await writeFile(statePath, Buffer.from(`{"contract_id":"wrong","entries":[]}`));

  const rejected = new OriginBudgetStore(options);
  await assert.rejects(rejected.load(), OriginBudgetCorruptError);
  await assert.rejects(rejected.load(), OriginBudgetCorruptError);
  assert.equal(
    await readFile(statePath, "utf8"),
    `{"contract_id":"wrong","entries":[]}`,
  );
});

test("rejects duplicate-key JSON without deleting the evidence", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  const statePath = path.join(root, ".openlinker", "origin-budgets.v1.json");
  await mkdir(path.dirname(statePath), { recursive: true });
  await writeFile(
    statePath,
    Buffer.from(
      `{"contract_id":"openlinker.browser.origin-budgets.v1","contract_id":"openlinker.browser.origin-budgets.v1","entries":[]}`,
    ),
  );
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 2,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await assert.rejects(budget.load(), OriginBudgetCorruptError);
  assert.match(await readFile(statePath, "utf8"), /"contract_id".*"contract_id"/);
});

test("honors bounded Retry-After without charging navigation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  let now = 1_000_000;
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 10,
    maxNavigationsPerMinute: 10,
    now: () => now,
  });
  await budget.load();
  budget.setRetryAfter(identity, "https://example.com", 90_000);
  assert.equal(budget.retryDelay(identity, "https://example.com"), 90_000);
  now += 90_001;
  assert.equal(budget.retryDelay(identity, "https://example.com"), undefined);
});

test("keeps the newest origin when deterministically bounding state", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 1,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await budget.load();
  for (let index = 0; index <= 256; index++) {
    assert.equal(
      budget.chargeAction(identity, `https://origin-${index}.example`),
      undefined,
    );
  }
  assert.equal(
    budget.chargeAction(identity, "https://origin-256.example"),
    60_000,
  );
  await budget.checkpoint();
  const state = JSON.parse(
    await readFile(
      path.join(root, ".openlinker", "origin-budgets.v1.json"),
      "utf8",
    ),
  ) as { entries: unknown[] };
  assert.equal(state.entries.length, 256);
});

test("compacts worst-case windows before a bounded checkpoint", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-budget-"));
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 600,
    maxNavigationsPerMinute: 60,
    now: () => 1_000_000,
  });
  await budget.load();
  for (let origin = 0; origin < 100; origin++) {
    for (let action = 0; action < 600; action++) {
      assert.equal(
        budget.chargeAction(identity, `https://dense-${origin}.example`),
        undefined,
      );
    }
  }
  await budget.checkpoint();
  const raw = await readFile(
    path.join(root, ".openlinker", "origin-budgets.v1.json"),
  );
  assert.ok(raw.byteLength <= 256 * 1024);
  const state = JSON.parse(raw.toString("utf8")) as { entries: unknown[] };
  assert.ok(state.entries.length >= 1);
  assert.ok(state.entries.length < 100);
});
