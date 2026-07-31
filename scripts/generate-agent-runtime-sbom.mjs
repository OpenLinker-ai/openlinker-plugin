#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";

function argumentValue(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 && index + 1 < process.argv.length
    ? process.argv[index + 1]
    : "";
}

async function filesBelow(root, current = root) {
  const entries = await readdir(current, { withFileTypes: true });
  const files = [];
  for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
    const path = join(current, entry.name);
    if (entry.isDirectory()) {
      files.push(...await filesBelow(root, path));
    } else if (entry.isFile()) {
      files.push({
        path,
        name: relative(root, path).replaceAll("\\", "/"),
      });
    } else {
      throw new Error(`unsupported artifact entry: ${path}`);
    }
  }
  return files;
}

function digest(algorithm, value) {
  return createHash(algorithm).update(value).digest("hex");
}

const root = resolve(argumentValue("--root"));
const name = argumentValue("--name");
const version = argumentValue("--version");
const output = resolve(argumentValue("--out"));
if (!name || !version || !argumentValue("--root") || !argumentValue("--out")) {
  throw new Error(
    "usage: generate-agent-runtime-sbom.mjs --root <dir> --name <name> --version <version> --out <file>",
  );
}

const fileEntries = [];
for (const [index, file] of (await filesBelow(root)).entries()) {
  const content = await readFile(file.path);
  fileEntries.push({
    SPDXID: `SPDXRef-File-${index + 1}`,
    fileName: `./${file.name}`,
    checksums: [
      { algorithm: "SHA1", checksumValue: digest("sha1", content) },
      { algorithm: "SHA256", checksumValue: digest("sha256", content) },
    ],
  });
}
const verificationInput = fileEntries
  .map((file) => file.checksums[0].checksumValue)
  .sort()
  .join("");
const namespaceDigest = digest(
  "sha256",
  JSON.stringify({ name, version, files: fileEntries }),
);
const packageID = "SPDXRef-Package";
const document = {
  spdxVersion: "SPDX-2.3",
  dataLicense: "CC0-1.0",
  SPDXID: "SPDXRef-DOCUMENT",
  name,
  documentNamespace: `https://openlinker.ai/spdx/${name}/${version}/${namespaceDigest}`,
  creationInfo: {
    created: "1970-01-01T00:00:00Z",
    creators: ["Organization: OpenLinker"],
  },
  packages: [{
    name,
    SPDXID: packageID,
    versionInfo: version,
    downloadLocation: "NOASSERTION",
    filesAnalyzed: true,
    packageVerificationCode: {
      packageVerificationCodeValue: digest("sha1", verificationInput),
    },
    licenseConcluded: "Apache-2.0",
    licenseDeclared: "Apache-2.0",
    copyrightText: "NOASSERTION",
  }],
  files: fileEntries,
  relationships: [
    {
      spdxElementId: "SPDXRef-DOCUMENT",
      relationshipType: "DESCRIBES",
      relatedSpdxElement: packageID,
    },
    ...fileEntries.map((file) => ({
      spdxElementId: packageID,
      relationshipType: "CONTAINS",
      relatedSpdxElement: file.SPDXID,
    })),
  ],
};
await writeFile(output, `${JSON.stringify(document, null, 2)}\n`, "utf8");
