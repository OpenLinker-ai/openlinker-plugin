import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const canonicalRoot = join(repoRoot, "shared/skills");
const platformRoots = [
  join(repoRoot, "platforms/codex/openlinker/skills"),
  join(repoRoot, "platforms/claude/openlinker/skills"),
];
const sharedSkills = [
  "find-and-run-agent",
  "inspect-openlinker-run",
  "serve-openlinker-agent",
  "setup-openlinker-cli",
  "use-isolated-browser",
];

async function filesUnder(root, current = root) {
  const entries = await readdir(current, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(current, entry.name);
    if (entry.isDirectory()) files.push(...(await filesUnder(root, path)));
    else if (entry.isFile()) files.push(relative(root, path));
  }
  return files.sort();
}

for (const skill of sharedSkills) {
  const canonical = join(canonicalRoot, skill);
  const canonicalFiles = await filesUnder(canonical);
  for (const platformRoot of platformRoots) {
    const mirrored = join(platformRoot, skill);
    const mirroredFiles = await filesUnder(mirrored);
    assert.deepEqual(mirroredFiles, canonicalFiles, `${relative(repoRoot, mirrored)}: file list differs`);
    for (const file of canonicalFiles) {
      const [left, right] = await Promise.all([readFile(join(canonical, file)), readFile(join(mirrored, file))]);
      assert.deepEqual(right, left, `${relative(repoRoot, mirrored)}/${file}: bytes differ`);
    }
  }
}

console.log("dual-platform skill parity passed");
