import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { readHostLock } from "../platforms/codex/openlinker/scripts/plugin-host-lock.mjs";

const root = resolve(import.meta.dirname, "..");
const lock = readHostLock(resolve(root, "shared"));
for (const host of ["codex", "claude"]) {
  assert.deepEqual(readHostLock(resolve(root, `platforms/${host}/openlinker`)), lock);
  assert.equal(readFileSync(resolve(root, `platforms/${host}/openlinker/host-lock.json`), "utf8"), readFileSync(resolve(root, "shared/host-lock.json"), "utf8"));
}
console.log(`Plugin host lock passed: ${lock.version} (${lock.plugin_commit})`);
