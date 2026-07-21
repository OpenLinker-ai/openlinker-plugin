import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { extname, join, relative, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const textExtensions = new Set(["", ".cmd", ".json", ".md", ".mjs", ".ps1", ".sh", ".svg", ".yaml", ".yml"]);
const ignoredDirectories = new Set([".git", "node_modules"]);
const forbidden = [
  ["User Token", /ol_user_[A-Za-z0-9_-]{8,}/],
  ["Agent Token", /ol_agent_[A-Za-z0-9_-]{8,}/],
  ["Anthropic key", /sk-ant-[A-Za-z0-9_-]{8,}/],
  ["OpenAI key", /sk-(?:proj|svcacct)-[A-Za-z0-9_-]{8,}/],
  ["test environment", /twv1\.kinzhi\.net/i],
  ["private repository owner", /kinzhi\/openlinker/i],
  ["absolute macOS user path", /\/Users\/[A-Za-z0-9._-]+\//],
  ["absolute Linux user path", /\/home\/[A-Za-z0-9._-]+\//],
  ["unfinished placeholder", /\[(?:TODO|TBD):/i],
];

async function walk(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (ignoredDirectories.has(entry.name)) continue;
    const path = join(directory, entry.name);
    if (entry.isDirectory()) files.push(...(await walk(path)));
    else if (entry.isFile() && textExtensions.has(extname(entry.name))) files.push(path);
  }
  return files;
}

for (const file of await walk(root)) {
  const content = await readFile(file, "utf8");
  for (const [label, pattern] of forbidden) {
    assert.equal(pattern.test(content), false, `${relative(root, file)} contains ${label}`);
  }
}

console.log("secret and path scan passed");
