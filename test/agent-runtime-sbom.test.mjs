import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { promisify } from "node:util";
import test from "node:test";

import { buildAgentRuntimePackages } from "../scripts/build-agent-runtime-packages.mjs";

const execFileAsync = promisify(execFile);
const root = resolve(import.meta.dirname, "..");

test("Agent Runtime SPDX SBOM enumerates only the bounded package files", async () => {
  const temporary = await mkdtemp(join(tmpdir(), "openlinker-runtime-sbom-"));
  try {
    const packages = await buildAgentRuntimePackages(join(temporary, "packages"));
    const output = join(temporary, "codex.spdx.json");
    await execFileAsync(process.execPath, [
      join(root, "scripts", "generate-agent-runtime-sbom.mjs"),
      "--root", packages.codexMarketplace,
      "--name", "openlinker-agent-runtime-codex-plugin",
      "--version", "v0.1.2",
      "--out", output,
    ]);
    const sbom = JSON.parse(await readFile(output, "utf8"));
    assert.equal(sbom.spdxVersion, "SPDX-2.3");
    assert.equal(sbom.packages.length, 1);
    assert.equal(sbom.files.length, 6);
    assert.ok(sbom.files.every((file) =>
      file.checksums.some((checksum) => checksum.algorithm === "SHA256")));
    assert.equal(
      JSON.stringify(sbom).includes("OPENLINKER_USER_TOKEN"),
      false,
    );
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});
