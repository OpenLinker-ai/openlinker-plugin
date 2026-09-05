import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  NATIVE_HOST_NAME,
  createExtensionInfo,
  handleNativeRequest,
} from "../extension/protocol.mjs";
import {
  CONNECTION_STATES,
  NativeConnectionController,
} from "../extension/connection-state.mjs";

const extensionRoot = new URL("../extension/", import.meta.url);

test("OpenLinker extension manifest is minimal and does not claim OpenAI identity", async () => {
  const manifest = JSON.parse(
    await readFile(new URL("manifest.json", extensionRoot), "utf8"),
  );
  assert.deepEqual(Object.keys(manifest).sort(), [
    "action",
    "background",
    "description",
    "manifest_version",
    "name",
    "permissions",
    "version",
  ]);
  assert.equal(manifest.manifest_version, 3);
  assert.equal(manifest.name, "OpenLinker Browser Runtime");
  assert.equal(manifest.action.default_popup, "openlinker-runtime/index.html");
  assert.deepEqual(manifest.background, {
    service_worker: "service-worker.mjs",
    type: "module",
  });
  assert.deepEqual(manifest.permissions, ["nativeMessaging"]);
  assert.equal(JSON.stringify(manifest).includes("openai"), false);
  assert.equal(JSON.stringify(manifest).includes("codex"), false);
  assert.match(manifest.version, /^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/);
});

test("OpenLinker extension exposes only bounded ping and identity methods", () => {
  const manifest = { version: "1.2.3.4" };
  const extensionID = "abcdefghijklmnopabcdefghijklmnop";
  assert.equal(NATIVE_HOST_NAME, "ai.openlinker.browser");
  assert.deepEqual(createExtensionInfo(manifest, extensionID), {
    type: "extension",
    version: "1.2.3.4",
    metadata: { extensionId: extensionID },
  });
  assert.deepEqual(
    handleNativeRequest(
      { jsonrpc: "2.0", id: "request-1", method: "ping", params: {} },
      manifest,
      extensionID,
    ),
    { jsonrpc: "2.0", id: "request-1", result: "pong" },
  );
  assert.deepEqual(
    handleNativeRequest(
      { jsonrpc: "2.0", id: "request-2", method: "getInfo", params: {} },
      manifest,
      extensionID,
    ),
    {
      jsonrpc: "2.0",
      id: "request-2",
      result: {
        type: "extension",
        version: "1.2.3.4",
        metadata: { extensionId: extensionID },
      },
    },
  );
});

test("OpenLinker extension rejects malformed and unknown Native Host commands", () => {
  const manifest = { version: "1.2.3.4" };
  const extensionID = "abcdefghijklmnopabcdefghijklmnop";
  assert.deepEqual(
    handleNativeRequest(
      { jsonrpc: "2.0", id: "request-3", method: "evaluate", params: {} },
      manifest,
      extensionID,
    ),
    {
      jsonrpc: "2.0",
      id: "request-3",
      error: { code: -32601, message: "method not found" },
    },
  );
  for (const request of [
    null,
    {},
    { jsonrpc: "2.0", id: "request-4", method: "ping", params: { url: "https://example.com" } },
    { jsonrpc: "2.0", id: "x".repeat(129), method: "ping", params: {} },
  ]) {
    const response = handleNativeRequest(request, manifest, extensionID);
    assert.equal(response.jsonrpc, "2.0");
    assert.equal(response.error.code, -32600);
  }
});

function fakePort() {
  const messageListeners = [];
  const disconnectListeners = [];
  const sent = [];
  return {
    disconnect() {
      for (const listener of disconnectListeners) listener();
    },
    onDisconnect: {
      addListener(listener) {
        disconnectListeners.push(listener);
      },
    },
    onMessage: {
      addListener(listener) {
        messageListeners.push(listener);
      },
    },
    postMessage(message) {
      sent.push(message);
    },
    receive(message) {
      for (const listener of messageListeners) listener(message);
    },
    sent,
  };
}

test("MV3 connection reconnects with a new port and stops after a bounded budget", () => {
  const connectedPorts = [];
  const timers = [];
  const controller = new NativeConnectionController({
    connectNative() {
      const port = fakePort();
      connectedPorts.push(port);
      return port;
    },
    handleRequest: (message) => ({ responseTo: message.id }),
    scheduleTimer(callback, delay) {
      const timer = { callback, delay };
      timers.push(timer);
      return timer;
    },
    cancelTimer() {},
  });

  controller.start();
  assert.equal(controller.state, CONNECTION_STATES.RECONNECTING);
  connectedPorts[0].receive({ id: "host-1" });
  assert.equal(controller.state, CONNECTION_STATES.READY);
  assert.deepEqual(connectedPorts[0].sent, [{ responseTo: "host-1" }]);

  connectedPorts[0].disconnect();
  assert.equal(controller.state, CONNECTION_STATES.DISCONNECTED);
  assert.equal(timers[0].delay, 100);
  timers.shift().callback();
  assert.equal(controller.state, CONNECTION_STATES.RECONNECTING);
  connectedPorts[1].receive({ id: "host-2" });
  assert.equal(controller.state, CONNECTION_STATES.READY);
  assert.equal(controller.reconnectAttempts, 0);

  let attempts = 0;
  const retryTimers = [];
  const exhausted = new NativeConnectionController({
    connectNative() {
      attempts += 1;
      throw new Error("host unavailable");
    },
    handleRequest: () => ({}),
    maxReconnectAttempts: 3,
    scheduleTimer(callback, delay) {
      const timer = { callback, delay };
      retryTimers.push(timer);
      return timer;
    },
    cancelTimer() {},
  });
  exhausted.start();
  while (retryTimers.length > 0) retryTimers.shift().callback();
  assert.equal(exhausted.state, CONNECTION_STATES.CLOSED);
  assert.equal(attempts, 4);
});

test("MV3 connection invokes browser timer APIs with their global receiver", () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timer = { kind: "browser-timer" };
  let scheduled;
  let cancelled;
  try {
    globalThis.setTimeout = function (callback, delay) {
      assert.equal(this, globalThis);
      scheduled = { callback, delay };
      return timer;
    };
    globalThis.clearTimeout = function (value) {
      assert.equal(this, globalThis);
      cancelled = value;
    };
    const controller = new NativeConnectionController({
      connectNative() {
        throw new Error("host unavailable");
      },
      handleRequest: () => ({}),
    });
    controller.start();
    assert.equal(scheduled.delay, 100);
    controller.close();
    assert.equal(cancelled, timer);
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test("extension sources contain no remote code or generic browser control surface", async () => {
  const source = await Promise.all(
    [
      "connection-state.mjs",
      "protocol.mjs",
      "service-worker.mjs",
      "openlinker-runtime/activate.mjs",
    ].map(
      async (file) => readFile(new URL(file, extensionRoot), "utf8"),
    ),
  );
  const combined = source.join("\n");
  for (const forbidden of [
    "eval(",
    "new Function",
    "chrome.debugger",
    "chrome.scripting",
    "chrome.tabs.executeScript",
    "fetch(",
    "XMLHttpRequest",
    "WebSocket",
    "com.openai",
    "hehggadaopoacecdllhhajmbjkdcmajg",
  ]) {
    assert.equal(combined.includes(forbidden), false, forbidden);
  }
});
