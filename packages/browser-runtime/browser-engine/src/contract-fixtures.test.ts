import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import { normalizeContinuationURL } from "./page-continuation.js";
import { validateOriginBudgetDocument } from "./origin-budget.js";
import { ENGINE_CONTRACT_ID, parseRequest } from "./protocol.js";

interface ContractFixtures {
  public_urls: Array<{ value: string; accepted: boolean }>;
  coordinates: Array<{ x: number; y: number; accepted: boolean }>;
  continuations: Array<{ value: string; normalized: string | null }>;
  origin_budgets: Array<{ value: string; accepted: boolean }>;
}

const fixtures = JSON.parse(
  readFileSync(new URL("../contract-fixtures.json", import.meta.url), "utf8"),
) as ContractFixtures;
const now = Date.parse("2029-12-31T23:59:30Z");

test("TypeScript protocol matches the shared URL and coordinate fixtures", () => {
  for (const fixture of fixtures.public_urls) {
    const parse = () =>
      parseRequest(
        JSON.stringify(request({ kind: "navigate", url: fixture.value })),
        now,
      );
    if (fixture.accepted) {
      assert.doesNotThrow(parse, fixture.value);
    } else {
      assert.throws(parse, fixture.value);
    }
  }
  for (const fixture of fixtures.coordinates) {
    const parse = () =>
      parseRequest(
        JSON.stringify(
          request({ kind: "click", x: fixture.x, y: fixture.y }),
        ),
        now,
      );
    if (fixture.accepted) {
      assert.doesNotThrow(parse, `${fixture.x},${fixture.y}`);
    } else {
      assert.throws(parse, `${fixture.x},${fixture.y}`);
    }
  }
});

test("TypeScript continuation normalization matches the shared fixtures", () => {
  for (const fixture of fixtures.continuations) {
    assert.equal(
      normalizeContinuationURL(fixture.value) ?? null,
      fixture.normalized,
      fixture.value,
    );
  }
});

test("TypeScript origin-budget validation matches the shared fixtures", () => {
  for (const fixture of fixtures.origin_budgets) {
    const validate = () =>
      validateOriginBudgetDocument(Buffer.from(fixture.value, "utf8"));
    if (fixture.accepted) {
      assert.doesNotThrow(validate, fixture.value);
    } else {
      assert.throws(validate, fixture.value);
    }
  }
});

function request(action: Record<string, unknown>): Record<string, unknown> {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: "1",
    deadline: "2030-01-01T00:00:00.000000000Z",
    identity: {
      run_id: "11111111-1111-4111-8111-111111111111",
      agent_id: "22222222-2222-4222-8222-222222222222",
      principal_scope_id: "scope_333333333333",
      browser_session_id: "44444444-4444-4444-8444-444444444444",
      session_epoch: 1,
      attachment_id: "55555555-5555-4555-8555-555555555555",
      control_epoch: 1,
      controller: "agent",
      browser_interaction_policy: "restricted",
      browser_interaction_policy_generation: 1,
      browser_mutation_origins: [],
      browser_mutation_origins_sha256:
        "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
    },
    action,
  };
}
