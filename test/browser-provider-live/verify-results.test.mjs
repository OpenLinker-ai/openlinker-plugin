import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { verifyResults } from "./verify-results.mjs";

const marker = "provider-live-0123456789abcdef";
const markerSHA256 = createHash("sha256").update(marker).digest("hex");

function evidence(provider, requested, selected) {
  return {
    status: "passed",
    provider,
    provider_version: `${provider}-test-version`,
    browser_client_mode_requested: requested,
    browser_client_mode_selected: selected,
    browser_tool: "browser_session",
    browser_tool_surface_count: 1,
    browser_contract_id: "openlinker.browser.v2",
    browser_interaction_policy: "full",
    browser_interaction_policy_generation: 1,
    browser_mutation_origins_sha256: "a".repeat(64),
    fixture_origin: "https://fixture.example",
    observed_marker_sha256: markerSHA256,
    provider_final_response_sha256: markerSHA256,
    ready_lifecycle_evidence_observed: true,
    closed_lifecycle_observed: true,
  };
}

function matrix() {
  return [
    evidence("codex", "native", "plugin_native"),
    evidence("codex", "mcp", "direct_mcp"),
    evidence("claude", "native", "plugin_native"),
    evidence("claude", "mcp", "direct_mcp"),
  ];
}

test("accepts only the complete equivalent four-quadrant matrix", () => {
  assert.doesNotThrow(() => verifyResults(matrix(), marker));
});

test("rejects missing, duplicate, mismatched, and incomplete evidence", () => {
  assert.throws(() => verifyResults(matrix().slice(0, 3), marker));

  const duplicate = matrix();
  duplicate[3] = { ...duplicate[2] };
  assert.throws(() => verifyResults(duplicate, marker));

  const mismatch = matrix();
  mismatch[1].browser_contract_id = "other";
  assert.throws(() => verifyResults(mismatch, marker));

  const incomplete = matrix();
  incomplete[0].closed_lifecycle_observed = false;
  assert.throws(() => verifyResults(incomplete, marker));
});
