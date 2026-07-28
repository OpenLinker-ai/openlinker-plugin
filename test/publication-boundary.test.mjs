import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  privateRootOnlyPaths,
  publicationBoundaryFailures,
} from "../scripts/check-publication-boundary.mjs";

test("the public Plugin tree excludes private-root-only material", async () => {
  assert.deepEqual(await publicationBoundaryFailures(), []);
});

test("the publication gate rejects an injected internal design tree", async () => {
  const root = await mkdtemp(join(tmpdir(), "openlinker-plugin-publication-"));
  try {
    await mkdir(join(root, "docs", "acceptance"), { recursive: true });
    assert.deepEqual(
      await publicationBoundaryFailures(root, privateRootOnlyPaths),
      [
        "docs/acceptance: private-root-only material exists in the public Plugin repository",
      ],
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
