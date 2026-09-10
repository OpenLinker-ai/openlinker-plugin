// Build-time only: native Plugin consumers do not need Go or an Agent Node
// process. Resolve the exact dependency of this Plugin release, never a sibling
// checkout, workspace override, latest version, or manually copied contract.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const agentNodeModulePath = "github.com/OpenLinker-ai/openlinker-agent-node";
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function resolveAgentNodeModule(repositoryRoot = root) {
  const options = {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: { ...process.env, GOWORK: "off", GOFLAGS: "-mod=readonly" },
    timeout: 60_000,
    maxBuffer: 4 * 1024 * 1024,
    stdio: ["ignore", "pipe", "pipe"],
  };
  const go = (...args) => execFileSync("go", args, options);
  const manifest = JSON.parse(go("mod", "edit", "-json"));
  if (!Array.isArray(manifest.Require) || (manifest.Replace?.length ?? 0) !== 0) {
    throw new Error("Plugin module must declare immutable dependencies without replace");
  }
  const pins = manifest.Require.filter((item) => item.Path === agentNodeModulePath);
  if (pins.length !== 1 || !/^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(pins[0].Version)) {
    throw new Error("Plugin must declare exactly one immutable Agent Node module version");
  }
  const version = pins[0].Version;
  const sums = readFileSync(join(repositoryRoot, "go.sum"), "utf8")
    .trim().split(/\r?\n/).map((line) => line.trim().split(/\s+/));
  const expectedSum = (suffix) => {
    const rows = sums.filter(([path, selected]) => path === agentNodeModulePath && selected === version + suffix);
    if (rows.length !== 1 || rows[0].length !== 3 || !/^h1:[A-Za-z0-9+/]{43}=$/.test(rows[0][2])) {
      throw new Error("Plugin go.sum is missing an unambiguous Agent Node checksum");
    }
    return rows[0][2];
  };
  const sum = expectedSum("");
  const goModSum = expectedSum("/go.mod");
  const selected = JSON.parse(go("list", "-m", "-json", "-mod=readonly", agentNodeModulePath));
  if (selected.Path !== agentNodeModulePath || selected.Version !== version || selected.Replace || selected.Error) {
    throw new Error("Resolved Agent Node build-list entry differs from the declared immutable pin");
  }
  const downloaded = JSON.parse(go("mod", "download", "-json", `${agentNodeModulePath}@${version}`));
  if (downloaded.Path !== agentNodeModulePath || downloaded.Version !== version ||
      downloaded.Sum !== sum || downloaded.GoModSum !== goModSum || downloaded.Error ||
      typeof downloaded.Dir !== "string" || !downloaded.Dir) {
    throw new Error("Downloaded Agent Node module does not match Plugin checksum evidence");
  }
  // A correct cached .zip is insufficient if someone edited its extracted
  // directory. Verify the actual module cache before reading or executing it.
  go("mod", "verify");
  return { directory: downloaded.Dir, version, sum, goModSum };
}

export function readAgentHostContract(repositoryRoot = root) {
  const module = resolveAgentNodeModule(repositoryRoot);
  const contract = JSON.parse(readFileSync(join(module.directory, "pkg/adapters/agenthost/contract.json"), "utf8"));
  if (Object.keys(contract).sort().join(",") !== "browser_proxy,delegation_proxy,protocol" ||
      contract.protocol !== "openlinker.agent-host.v1" ||
      ![contract.browser_proxy, contract.delegation_proxy].every((args) =>
        Array.isArray(args) && args.length > 0 && args.every((arg) => typeof arg === "string" && arg.length > 0))) {
    throw new Error("Agent Node module has an incompatible Agent Host contract");
  }
  return contract;
}
