import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import { basename, dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const pairs = [
  ["README.md", "README.zh-CN.md"],
  ["docs/calling-agents.md", "docs/calling-agents.zh-CN.md"],
  ["docs/serving-as-agent.md", "docs/serving-as-agent.zh-CN.md"],
  ["docs/configuration.md", "docs/configuration.zh-CN.md"],
];

const documents = new Map();
for (const pair of pairs) {
  for (const file of pair) {
    documents.set(file, await readFile(resolve(root, file), "utf8"));
  }
}

for (const [english, chinese] of pairs) {
  assert.ok(documents.get(english).includes(basename(chinese)), `${english} must link to ${chinese}`);
  assert.ok(documents.get(chinese).includes(basename(english)), `${chinese} must link to ${english}`);
  const headingLevels = (content) => [...content.matchAll(/^(#{1,6})\s+/gm)].map((match) => match[1].length);
  assert.deepEqual(
    headingLevels(documents.get(chinese)),
    headingLevels(documents.get(english)),
    `${chinese} must preserve the heading structure of ${english}`,
  );
}

for (const [file, content] of documents) {
  for (const match of content.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
    const target = match[1].trim().replace(/^<|>$/g, "").split("#", 1)[0];
    if (!target || /^(?:https?:|mailto:)/.test(target)) continue;
    const path = resolve(root, dirname(file), decodeURIComponent(target));
    const info = await stat(path).catch(() => null);
    assert.ok(info?.isFile(), `${file} links to missing local file ${target}`);
  }
}

const allUserDocs = [...documents.values()].join("\n");
for (const invocation of [
  "$openlinker",
  "$setup-openlinker-cli",
  "$serve-openlinker-agent",
  "/openlinker:openlinker",
  "/openlinker:openlinker-setup",
  "/openlinker:openlinker-agent",
]) {
  assert.ok(allUserDocs.includes(invocation), `native invocation ${invocation} is undocumented`);
}

for (const forbidden of [
  /`\/openlinker`/,
  /`\/openlinker-setup`/,
  /`\/openlinker-agent`/,
]) {
  assert.equal(forbidden.test(allUserDocs), false, `unnamespaced Claude invocation ${forbidden} is advertised`);
}

for (const required of [
  "OPENLINKER_API_BASE",
  "OPENLINKER_USER_TOKEN",
  "OPENLINKER_AGENT_TOKEN",
  "OPENLINKER_AGENT_TOKEN_FILE",
  "OPENLINKER_NODE_ID",
]) {
  assert.ok(allUserDocs.includes(required), `configuration ${required} is undocumented`);
}

const agentSurface = JSON.parse(await readFile(resolve(root, "shared/contracts/agent-surface.json"), "utf8"));
const agentGuides = [
  documents.get("docs/serving-as-agent.md"),
  documents.get("docs/serving-as-agent.zh-CN.md"),
  documents.get("docs/configuration.md"),
  documents.get("docs/configuration.zh-CN.md"),
].join("\n");
for (const tool of agentSurface.tools) {
  assert.ok(agentGuides.includes(tool), `Agent-control tool ${tool} is undocumented`);
}

console.log("native usage documentation contract passed");
