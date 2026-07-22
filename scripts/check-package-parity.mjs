import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const codex = join(root, "platforms/codex/openlinker");
const claude = join(root, "platforms/claude/openlinker");

async function filesUnder(base, current = base) {
  const entries = await readdir(current, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(current, entry.name);
    if (entry.isDirectory()) files.push(...(await filesUnder(base, path)));
    else if (entry.isFile()) files.push(relative(base, path));
  }
  return files.sort();
}

async function compareTrees(left, right) {
  const [leftFiles, rightFiles] = await Promise.all([filesUnder(left), filesUnder(right)]);
  assert.deepEqual(rightFiles, leftFiles, `${relative(root, right)}: file list differs from ${relative(root, left)}`);
  for (const file of leftFiles) {
    const [leftBytes, rightBytes] = await Promise.all([readFile(join(left, file)), readFile(join(right, file))]);
    assert.deepEqual(rightBytes, leftBytes, `${relative(root, right)}/${file}: bytes differ`);
  }
}

await compareTrees(join(root, "shared/assets"), join(codex, "assets"));
await compareTrees(join(root, "shared/assets"), join(claude, "assets"));
await compareTrees(join(codex, "scripts"), join(claude, "scripts"));
await compareTrees(join(codex, "bin"), join(claude, "bin"));
assert.deepEqual(await readFile(join(claude, "LICENSE")), await readFile(join(codex, "LICENSE")), "platform LICENSE files differ");

console.log("dual-platform package parity passed");
