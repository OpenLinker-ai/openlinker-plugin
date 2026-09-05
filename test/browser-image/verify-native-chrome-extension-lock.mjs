import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

async function verifyExtensionLock(
  lockPath,
  artifactPath,
  expectedID,
  expectedVersion,
) {
  if (
    !/^[a-p]{32}$/.test(expectedID ?? "") ||
    !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(expectedVersion ?? "")
  ) {
    throw new Error("extension lock verifier arguments are invalid");
  }
  const raw = await readFile(lockPath, "utf8");
  const value = JSON.parse(raw);
  const expectedKeys = [
    "activation_path",
    "extension_id",
    "filename",
    "sha256",
    "source_reference",
    "version",
  ].sort();
  const keys = Object.keys(value).sort();
  for (const key of expectedKeys) {
    if (raw.match(new RegExp(`"${key}"`, "g"))?.length !== 1) {
      throw new Error("extension lock contains a duplicate or missing field");
    }
  }
  if (
    keys.length !== expectedKeys.length ||
    keys.some((key, index) => key !== expectedKeys[index]) ||
    value.extension_id !== expectedID ||
    value.version !== expectedVersion ||
    value.filename !== path.basename(artifactPath) ||
    typeof value.source_reference !== "string" ||
    value.source_reference.length < 1 ||
    value.source_reference.length > 512 ||
    typeof value.activation_path !== "string" ||
    !value.activation_path.startsWith("/") ||
    value.activation_path.includes("..") ||
    value.activation_path.length > 512 ||
    !/^[0-9a-f]{64}$/.test(value.sha256)
  ) {
    throw new Error("extension lock is invalid or does not match build arguments");
  }
  const digest = createHash("sha256")
    .update(await readFile(artifactPath))
    .digest("hex");
  if (digest !== value.sha256) {
    throw new Error("extension artifact digest does not match its lock");
  }
  return value;
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  const [lockPath, artifactPath, expectedID, expectedVersion] = process.argv.slice(2);
  await verifyExtensionLock(lockPath, artifactPath, expectedID, expectedVersion);
}

export { verifyExtensionLock };
