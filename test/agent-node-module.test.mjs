import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmod, mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";
import { agentNodeModulePath, readAgentHostContract, resolveAgentNodeModule } from "../scripts/resolve-agent-node-module.mjs";

test("build assets resolve one checksummed Node module and reject workspace, replace, missing sum and cache tampering", { timeout: 120_000 }, async (t) => {
  const fixture = await mkdtemp(join(tmpdir(), "openlinker-plugin-node-module-"));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  const caller = join(fixture, "plugin");
  const version = "v1.0.0";
  const contract = {
    protocol: "openlinker.agent-host.v1",
    browser_proxy: ["plugin", "browser-proxy", "--host"],
    delegation_proxy: ["plugin", "delegation-proxy", "--host"],
  };
  async function file(path, contents) {
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, contents);
  }
  const moduleMod = `module ${agentNodeModulePath}\n\ngo 1.23.0\n`;
  const prefix = `${agentNodeModulePath}@${version}`;
  const archiveRoot = join(fixture, "archive");
  const proxyRoot = join(fixture, "proxy", agentNodeModulePath.replace(/[A-Z]/g, (char) => `!${char.toLowerCase()}`), "@v");
  await file(join(archiveRoot, prefix, "go.mod"), moduleMod);
  await file(join(archiveRoot, prefix, "pkg/adapters/agenthost/contract.json"), JSON.stringify(contract));
  await file(join(proxyRoot, `${version}.mod`), moduleMod);
  await file(join(proxyRoot, `${version}.info`), JSON.stringify({ Version: version, Time: "2026-01-01T00:00:00Z" }));
  execFileSync("zip", ["-q", "-r", "-D", join(proxyRoot, `${version}.zip`), prefix], { cwd: archiveRoot, timeout: 10_000 });
  const manifest = `module example.invalid/plugin\n\ngo 1.23.0\nrequire ${agentNodeModulePath} ${version}\n`;
  await file(join(caller, "go.mod"), manifest);
  const environment = {
    GOPROXY: `${pathToFileURL(join(fixture, "proxy")).href},off`, GOSUMDB: "off",
    GOPRIVATE: "", GONOPROXY: "none", GONOSUMDB: "none",
    GOMODCACHE: join(fixture, "mod-cache"), GOCACHE: join(fixture, "build-cache"),
    GOWORK: join(fixture, "must-not-use-workspace.work"), GOENV: "off", GOTOOLCHAIN: "local",
  };
  const prior = Object.fromEntries(Object.keys(environment).map((key) => [key, process.env[key]]));
  Object.assign(process.env, environment);
  t.after(() => {
    for (const [key, value] of Object.entries(prior)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  execFileSync("go", ["mod", "download", "all"], {
    cwd: caller, env: { ...process.env, GOWORK: "off", GOFLAGS: "-modcacherw" }, timeout: 30_000, stdio: "pipe",
  });
  const sums = await readFile(join(caller, "go.sum"), "utf8");
  const selected = resolveAgentNodeModule(caller);
  assert.equal(selected.version, version);
  assert.deepEqual(readAgentHostContract(caller), contract);

  await file(join(caller, "go.mod"), "module example.invalid/plugin\n\ngo 1.23.0\n");
  assert.throws(() => resolveAgentNodeModule(caller), /immutable dependencies|immutable Agent Node/);
  await file(join(caller, "go.mod"), manifest + `replace ${agentNodeModulePath} ${version} => ../archive/${prefix}\n`);
  assert.throws(() => resolveAgentNodeModule(caller), /without replace/);
  await file(join(caller, "go.mod"), manifest);
  await file(join(caller, "go.sum"), sums.split("\n").filter((line) => !line.startsWith(`${agentNodeModulePath} ${version} `)).join("\n"));
  assert.throws(() => resolveAgentNodeModule(caller), /checksum/);
  await file(join(caller, "go.sum"), sums + sums);
  assert.throws(() => resolveAgentNodeModule(caller), /checksum/);
  await file(join(caller, "go.sum"), sums);
  assert.deepEqual(readAgentHostContract(caller), contract, "successful resolution must recover after invalid manifests");

  const contractPath = join(selected.directory, "pkg/adapters/agenthost/contract.json");
  await chmod(contractPath, 0o600);
  await writeFile(contractPath, JSON.stringify({ ...contract, browser_proxy: ["unsafe-proxy"] }));
  assert.throws(() => readAgentHostContract(caller), /go mod verify|dir has been modified/);
  await writeFile(contractPath, JSON.stringify(contract));
  assert.deepEqual(readAgentHostContract(caller), contract);
  await file(join(caller, "go.mod"), manifest + `replace ${agentNodeModulePath} ${version} => ../archive/${prefix}\n`);
  assert.throws(() => readAgentHostContract(caller), /without replace/, "prior success cannot mask reintroduced replacement");
});
