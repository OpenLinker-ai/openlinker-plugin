import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { resolve } from "node:path";

// Delivery-only commits may follow the host tag. Execution sources may not drift.
export function hostSourceDigest(ref = "HEAD", root = resolve(import.meta.dirname, "..")) {
  const tree = execFileSync("git", ["ls-tree", "-r", "--full-tree", ref, "--", "go.mod", "go.sum", "cmd", "internal", "packages", "scripts/build-plugin-host.mjs"], { cwd: root });
  if (!tree.length) throw new Error("empty Plugin host source tree");
  return createHash("sha256").update(tree).digest("hex");
}
