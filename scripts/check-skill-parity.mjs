import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const canonicalRoot = join(repoRoot, "shared/skills");
const codexRoot = join(repoRoot, "platforms/codex/openlinker/skills");
const claudeRoot = join(repoRoot, "platforms/claude/openlinker/skills");
const sharedSkills = [
  "find-and-run-agent",
  "inspect-openlinker-run",
  "serve-openlinker-agent",
  "setup-openlinker-cli",
  "setup-plugin-host",
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
  const codex = join(codexRoot, skill);
  const codexFiles = await filesUnder(codex);
  assert.deepEqual(codexFiles, canonicalFiles, `${relative(repoRoot, codex)}: file list differs`);
  for (const file of canonicalFiles) {
    const [left, right] = await Promise.all([readFile(join(canonical, file)), readFile(join(codex, file))]);
    assert.deepEqual(right, left, `${relative(repoRoot, codex)}/${file}: bytes differ`);
  }

  const claude = join(claudeRoot, skill);
  const claudeFiles = await filesUnder(claude);
  const portableFiles = canonicalFiles.filter((file) => !file.startsWith("agents/"));
  assert.deepEqual(
    claudeFiles,
    portableFiles,
    `${relative(repoRoot, claude)}: portable file list differs`,
  );
  for (const file of portableFiles) {
    const [left, right] = await Promise.all([readFile(join(canonical, file)), readFile(join(claude, file))]);
    assert.deepEqual(right, left, `${relative(repoRoot, claude)}/${file}: bytes differ`);
  }
}

for (const file of await filesUnder(claudeRoot)) {
  assert.equal(
    file.endsWith("agents/openai.yaml"),
    false,
    `Claude package contains Codex-only Agent metadata: ${file}`,
  );
}

console.log("dual-platform portable skill parity passed");
