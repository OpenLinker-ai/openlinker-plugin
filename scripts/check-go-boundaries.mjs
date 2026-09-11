import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const modulePath = "github.com/OpenLinker-ai/openlinker-plugin";
const environment = { ...process.env, GOWORK: "off" };
function packages(pattern) {
  const output = execFileSync("go", ["list", "-f", "{{.ImportPath}}", pattern], {
    cwd: root, env: environment, encoding: "utf8",
  }).trim();
  assert.ok(output, `boundary scan matched no packages: ${pattern}`);
  const values = output.split(/\r?\n/);
  assert.ok(values.every((value) => value.startsWith(`${modulePath}/`)), "unexpected module in boundary scan");
  return values;
}
function dependencies(pattern) {
  packages(pattern);
  return execFileSync("go", ["list", "-deps", "-f", "{{.ImportPath}}", pattern], {
    cwd: root, env: environment, encoding: "utf8",
  }).trim().split(/\r?\n/);
}

for (const dependency of dependencies("./...")) {
  assert.ok(!dependency.startsWith("github.com/OpenLinker-ai/openlinker-cli"), `reverse CLI dependency: ${dependency}`);

}
for (const dependency of dependencies("./packages/...")) {
  assert.ok(!dependency.startsWith("github.com/spf13/cobra"), `Cobra must stay in the host composition root: ${dependency}`);
}
for (const pattern of ["./packages/browser-runtime/...", "./cmd/openlinker-browser-runtime", "./cmd/openlinker-egress-gateway"]) {
  for (const dependency of dependencies(pattern)) {
    assert.ok(!dependency.startsWith("github.com/OpenLinker-ai/openlinker-go"), `SDK leakage into ${pattern}: ${dependency}`);
  }
}
console.log("Go dependency boundaries passed (nonempty, transitive scans; GOWORK=off)");
