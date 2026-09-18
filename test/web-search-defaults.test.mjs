import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (file) => readFile(path.join(root, file), "utf8");
const variables = ["OPENLINKER_CODEX_WEB_SEARCH", "OPENLINKER_CLAUDE_WEB_SEARCH"];

// Search is on by default. Every layer that can supply a value when the operator
// sets nothing has to say true, or the first one that says false wins: an image
// ENV reaches the entrypoint as a set value, so its "true" fallback never runs.
test("every packaged default for provider web search is on", async () => {
  const dockerfile = await read("Dockerfile.providers");
  for (const name of variables) {
    const baked = [...dockerfile.matchAll(new RegExp(`\\b${name}=(\\S+)`, "g"))].map((match) => match[1]);
    assert.deepEqual(baked, ["true"], `Dockerfile.providers bakes ${name}=${baked.join(",")}`);
  }

  const entrypoint = await read("cmd/openlinker-runtime-entrypoint/main.go");
  for (const name of variables) {
    assert.match(entrypoint, new RegExp(`defaultString\\(os\\.Getenv\\("${name}"\\), "true"\\)`), `entrypoint fallback for ${name}`);
  }

  const compose = {
    "deploy/compose.codex.yml": ["OPENLINKER_CODEX_WEB_SEARCH"],
    "deploy/compose.claude.yml": ["OPENLINKER_CLAUDE_WEB_SEARCH"],
    "deploy/compose.providers.yml": variables,
  };
  for (const [file, names] of Object.entries(compose)) {
    const text = await read(file);
    for (const name of names) {
      assert.match(text, new RegExp(`${name}: \\$\\{${name}:-true\\}`), `${file} default for ${name}`);
      assert.doesNotMatch(text, new RegExp(`${name}:-(?!true\\})`), `${file} has another default for ${name}`);
    }
  }

  const example = await read("deploy/.env.providers.example");
  for (const name of variables) {
    assert.match(example, new RegExp(`^${name}=true$`, "m"), `.env.providers.example sets ${name}`);
  }
});
