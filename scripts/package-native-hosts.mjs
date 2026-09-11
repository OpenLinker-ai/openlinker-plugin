import { cpSync, mkdirSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { readHostLock } from "../platforms/codex/openlinker/scripts/plugin-host-lock.mjs";

const output = resolve(process.argv[2] ?? "dist/native");
for (const platform of ["codex", "claude"]) {
  readHostLock(resolve(import.meta.dirname, `../platforms/${platform}/openlinker`));
  const target = join(output, platform, "openlinker");
  mkdirSync(target, { recursive: true });
  cpSync(resolve(import.meta.dirname, `../platforms/${platform}/openlinker`), target, { recursive: true });
  // Explicit setup installs only this machine's pinned host; keep packages small.
  rmSync(join(target, "host"), { recursive: true, force: true });
}
