#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";

export function parseProviders(value = "codex claude") {
  const providers = value.trim().split(/[\s,]+/).filter(Boolean);
  if (
    providers.length === 0 ||
    new Set(providers).size !== providers.length ||
    providers.some((provider) => provider !== "codex" && provider !== "claude")
  ) {
    throw new Error("live Providers must be a non-empty set of codex and claude");
  }
  return providers;
}

// Every selected Provider must pass both Browser client modes; a narrower
// Provider set narrows what is published, never what each Provider must prove.
export function verifyResults(results, expectedMarker, providers = ["codex", "claude"]) {
  const expectedQuadrants = new Set(
    providers.flatMap((provider) => [
      `${provider}/native/plugin_native`,
      `${provider}/mcp/direct_mcp`,
    ]),
  );
  if (results.length !== expectedQuadrants.size) {
    throw new Error(`expected ${expectedQuadrants.size} Provider quadrants, got ${results.length}`);
  }
  const invariantKeys = [
    "browser_tool",
    "browser_tool_surface_count",
    "browser_contract_id",
    "browser_interaction_policy",
    "browser_interaction_policy_generation",
    "browser_mutation_origins_sha256",
    "fixture_origin",
    "observed_marker_sha256",
    "provider_final_response_sha256",
  ];
  const baseline = results[0];
  const expectedMarkerSHA256 = createHash("sha256")
    .update(expectedMarker)
    .digest("hex");
  for (const result of results) {
    const quadrant = [
      result.provider,
      result.browser_client_mode_requested,
      result.browser_client_mode_selected,
    ].join("/");
    if (!expectedQuadrants.delete(quadrant)) {
      throw new Error(`unexpected or duplicate Provider quadrant ${quadrant}`);
    }
    if (
      result.status !== "passed" ||
      result.browser_tool_surface_count !== 1 ||
      result.observed_marker_sha256 !== expectedMarkerSHA256 ||
      result.provider_final_response_sha256 !== expectedMarkerSHA256 ||
      result.ready_lifecycle_evidence_observed !== true ||
      result.closed_lifecycle_observed !== true ||
      typeof result.provider_version !== "string" ||
      result.provider_version.length === 0
    ) {
      throw new Error(`incomplete Provider evidence for ${quadrant}`);
    }
    for (const key of invariantKeys) {
      if (result[key] !== baseline[key]) {
        throw new Error(`Provider quadrants disagree on ${key}`);
      }
    }
  }
  if (expectedQuadrants.size !== 0) {
    throw new Error(`missing Provider quadrants: ${[...expectedQuadrants].join(", ")}`);
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const [resultPath, expectedMarker, providerList] = process.argv.slice(2);
  if (!resultPath || !expectedMarker) {
    throw new Error("usage: verify-results.mjs <jsonl> <expected-marker> [providers]");
  }
  const results = readFileSync(resultPath, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  verifyResults(results, expectedMarker, parseProviders(providerList));
}
