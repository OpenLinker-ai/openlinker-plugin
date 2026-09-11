// Exercise the real public host using a source-only marketplace copy and private data.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cp, mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const work = await realpath(await mkdtemp(join(tmpdir(), "openlinker-marketplace-")));
try {
  for (const host of ["codex", "claude"]) {
    const pluginRoot = join(work, host, "openlinker");
    await cp(resolve(import.meta.dirname, `../platforms/${host}/openlinker`), pluginRoot, { recursive: true });
    const { installPluginHost } = await import(pathToFileURL(join(pluginRoot, "scripts/install-plugin-host.mjs")));
    const { resolvePluginHost } = await import(pathToFileURL(join(pluginRoot, "scripts/resolve-plugin-host.mjs")));
    const dataDir = join(work, "private data");
    const installed = await installPluginHost({ pluginRoot, dataDir });
    const binary = resolvePluginHost({ pluginRoot, dataDir, explicit: "", required: ["plugin.serve", "plugin.browser.serve"] });
    assert.equal(binary, installed.destination);
    for (const surface of ["serve", "browser-serve"]) {
      const result = spawnSync(binary, ["plugin", surface, "--host", host], {
        encoding: "utf8", timeout: 15000,
        env: { PATH: "/nonexistent", HOME: work, USERPROFILE: work, SystemRoot: process.env.SystemRoot ?? "", OPENLINKER_AGENT_CONFIG: join(work, "missing-agent.json") },
        input: '{"jsonrpc":"2.0","id":1,"method":"initialize"}\n{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n',
      });
      assert.equal(result.status, 0, result.stderr || String(result.error));
      const responses = result.stdout.trim().split(/\r?\n/).map(line => JSON.parse(line));
      assert.ok(responses.find(response => response.id === 1)?.result);
      const names = responses.find(response => response.id === 2).result.tools.map(tool => tool.name);
      if (surface === "serve") {
        assert.equal(names.length, 14);
        for (const name of ["search_agents", "run_agent", "get_agent_mode_status"]) assert.ok(names.includes(name));
      } else assert.deepEqual(names, ["browser_session"]);
      console.log(`${host} ${surface}: ${names.length} tools, no Go/CLI/Node executable on PATH; ${installed.version}`);
    }
    // Cover the package launcher, not just the resolved binary, on POSIX.
    if (process.platform !== "win32") {
      const result = spawnSync(join(pluginRoot, "bin/openlinker-plugin"), ["context"], {
        encoding: "utf8", timeout: 15000,
        env: { PATH: process.env.PATH, HOME: work, PLUGIN_ROOT: pluginRoot, OPENLINKER_PLUGIN_DATA: dataDir, OPENLINKER_CLI_BIN: "/nonexistent" },
      });
      assert.equal(result.status, 0, result.stderr);
      assert.equal(JSON.parse(result.stdout).host_version, installed.version);
    }
  }
} finally {
  await rm(work, { recursive: true, force: true });
}
