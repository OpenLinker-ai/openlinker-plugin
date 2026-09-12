import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const source = readFileSync(new URL("./fixture/main.go", import.meta.url), "utf8");
function script(name) {
  const delimiter = String.fromCharCode(96);
  const html = source.split(`const ${name} = ${delimiter}`)[1]?.split(delimiter)[0];
  assert.ok(html, `missing ${name} fixture`);
  return html.split("<script>")[1].split("</script>")[0];
}
function fixture() {
  const origin = "https://collector.example";
  const listeners = new Map();
  const frameListeners = new Map();
  const timers = [];
  const navigations = [];
  const state = { textContent: "collector_frame=loading" };
  const frame = {
    contentWindow: {},
    addEventListener: (name, callback) => frameListeners.set(name, callback),
    set src(value) { navigations.push(value); },
  };
  vm.runInNewContext(script("fullFramesHTML").replace("%s", JSON.stringify(origin)), {
    window: { addEventListener: (name, callback) => listeners.set(name, callback) },
    document: { getElementById: id => id === "collector-child" ? frame : state },
    setTimeout: callback => timers.push(callback),
  });
  return { origin, frame, state, listeners, frameListeners, timers, navigations };
}

test("blank/error iframe load cannot make the collector fixture ready", () => {
  const f = fixture();
  f.frameListeners.get("load")?.();
  assert.equal(f.state.textContent, "collector_frame=loading");
  const message = f.listeners.get("message");
  assert.equal(typeof message, "function");
  for (const event of [
    { source: f.frame.contentWindow, origin: "null", data: "openlinker-child-ready" },
    { source: f.frame.contentWindow, origin: "https://wrong.example", data: "openlinker-child-ready" },
    { source: {}, origin: f.origin, data: "openlinker-child-ready" },
    { source: f.frame.contentWindow, origin: f.origin, data: "wrong-message" },
  ]) message(event);
  assert.equal(f.state.textContent, "collector_frame=loading");
});

test("only the initialized child document confirms readiness and stops reloads", () => {
  const f = fixture();
  const events = new Map();
  vm.runInNewContext(script("fullChildHTML"), {
    document: { getElementById: () => ({ addEventListener: (name, fn) => events.set(name, fn) }) },
    parent: { postMessage(data) {
      assert.equal(typeof events.get("click"), "function", "button must be initialized before readiness");
      f.listeners.get("message")({ source: f.frame.contentWindow, origin: f.origin, data });
    } },
    fetch: () => assert.fail("readiness must not execute a mutation"),
  });
  assert.equal(f.state.textContent, "collector_frame=ready");
  while (f.timers.length) f.timers.shift()();
  assert.equal(f.navigations.length, 1);
});

test("unavailable collector retries only five fixture navigations and never becomes ready", () => {
  const f = fixture();
  for (let step = 0; f.timers.length && step < 10; step++) f.timers.shift()();
  assert.equal(f.timers.length, 0);
  assert.equal(f.state.textContent, "collector_frame=loading");
  assert.deepEqual(f.navigations, Array.from({ length: 5 }, (_, i) =>
    `${f.origin}/full/child?scope=collector&attempt=${i + 1}`));
});
