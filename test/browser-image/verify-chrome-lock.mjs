import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const EXPECTED_KEYS = [
  "distribution",
  "filename",
  "sha256",
  "source_reference",
  "upstream_filename",
  "upstream_sha256",
  "version",
].sort();

async function verifyChromeLock(
  lockPath,
  artifactPath,
  upstreamPath,
  expectedDistribution,
  expectedVersion,
) {
  if (
    expectedDistribution !== "chrome_for_testing" ||
    !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){3}$/.test(expectedVersion ?? "")
  ) {
    throw new Error("Chrome lock verifier arguments are invalid");
  }
  const raw = await readFile(lockPath, "utf8");
  const value = JSON.parse(raw);
  const keys = Object.keys(value).sort();
  for (const key of EXPECTED_KEYS) {
    if (raw.match(new RegExp(`"${key}"`, "g"))?.length !== 1) {
      throw new Error("Chrome lock record contains a duplicate or missing field");
    }
  }
  const expectedSource =
    `https://storage.googleapis.com/chrome-for-testing-public/${expectedVersion}/linux64/chrome-linux64.zip`;
  if (
    keys.length !== EXPECTED_KEYS.length ||
    keys.some((key, index) => key !== EXPECTED_KEYS[index]) ||
    value.distribution !== expectedDistribution ||
    value.version !== expectedVersion ||
    value.filename !== "chrome-linux-amd64.tar" ||
    value.filename !== path.basename(artifactPath) ||
    value.upstream_filename !== "chrome-linux64.zip" ||
    value.upstream_filename !== path.basename(upstreamPath) ||
    value.source_reference !== expectedSource ||
    !/^[0-9a-f]{64}$/.test(value.sha256) ||
    !/^[0-9a-f]{64}$/.test(value.upstream_sha256)
  ) {
    throw new Error("Chrome lock record is invalid or does not match build arguments");
  }
  const [artifact, upstream] = await Promise.all([
    readFile(artifactPath),
    readFile(upstreamPath),
  ]);
  const artifactDigest = createHash("sha256").update(artifact).digest("hex");
  const upstreamDigest = createHash("sha256").update(upstream).digest("hex");
  if (artifactDigest !== value.sha256) {
    throw new Error("normalized Chrome artifact digest does not match its lock record");
  }
  if (upstreamDigest !== value.upstream_sha256) {
    throw new Error("upstream Chrome archive digest does not match its lock record");
  }
  return value;
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  const [
    lockPath,
    artifactPath,
    upstreamPath,
    expectedDistribution,
    expectedVersion,
  ] = process.argv.slice(2);
  await verifyChromeLock(
    lockPath,
    artifactPath,
    upstreamPath,
    expectedDistribution,
    expectedVersion,
  );
}

export { EXPECTED_KEYS, verifyChromeLock };
