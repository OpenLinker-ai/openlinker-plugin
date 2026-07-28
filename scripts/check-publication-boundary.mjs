#!/usr/bin/env node

import { access } from "node:fs/promises";
import { constants } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export const privateRootOnlyPaths = Object.freeze([
  "docs/acceptance",
  "docs/superpowers/plans",
  "experiments",
  "test/probes",
]);

export async function publicationBoundaryFailures(
  root = repositoryRoot,
  paths = privateRootOnlyPaths,
) {
  const failures = [];
  for (const path of paths) {
    try {
      await access(resolve(root, path), constants.F_OK);
      failures.push(`${path}: private-root-only material exists in the public Plugin repository`);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
  }
  return failures;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const failures = await publicationBoundaryFailures();
  if (failures.length > 0) {
    console.error(failures.join("\n"));
    process.exitCode = 1;
  } else {
    console.log("public Plugin publication boundary passed");
  }
}
