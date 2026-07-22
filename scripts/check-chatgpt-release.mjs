import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { constants } from "node:fs";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const json = async (path) => JSON.parse(await readFile(join(root, path), "utf8"));

const surface = await json("shared/contracts/caller-surface.json");
const app = await json("chatgpt/app-config.example.json");
const skill = await readFile(join(root, "chatgpt/skills/browse-and-run-agent/SKILL.md"), "utf8");

assert.equal(surface.hosted_mcp.authenticated_app_requires_oauth_2_1, true);
assert.equal(surface.hosted_mcp.chatgpt_app_status, "blocked_pending_oauth");
assert.equal(surface.hosted_mcp.connector_id, null);
assert.equal(app.mcp_endpoint, surface.hosted_mcp.public_url);
assert.equal(app.authentication.type, "oauth_2_1");
assert.equal(app.authentication.pkce, "S256");
assert.equal(app.authentication.refresh_tokens_required, true);
assert.equal(app.connector_id, null);
assert.match(skill, /separately available ChatGPT Browser capability/);
assert.match(skill, /private, link-local, metadata/);
assert.match(skill, /Browsing and search do not authorize execution/);
assert.match(skill, /Never send raw screenshots/);

let appMappingExists = true;
try {
  await access(join(root, "platforms/codex/openlinker/.app.json"), constants.F_OK);
} catch {
  appMappingExists = false;
}
assert.equal(appMappingExists, false, "do not publish .app.json before a real connector ID and OAuth readiness");

console.log("ChatGPT App readiness contracts passed");
