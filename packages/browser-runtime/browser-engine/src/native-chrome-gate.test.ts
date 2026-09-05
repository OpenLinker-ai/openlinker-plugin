import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  NativeChromeGate,
  REQUIRED_CAPABILITIES,
} from "./native-chrome-gate.js";

const validEnvironment = {
  OPENLINKER_NATIVE_CHROME_ENABLED: "true",
  OPENLINKER_NATIVE_CHROME_SOCKET: "/browser-tmp/native-host.sock",
  OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT:
    "/opt/openlinker/native-chrome/extension",
  OPENLINKER_NATIVE_CHROME_EXTENSION_ID:
    "abcdefghijklmnopabcdefghijklmnop",
  OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION: "1.2.3.4",
  OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH:
    "/openlinker-runtime/index.html",
  OPENLINKER_NATIVE_CHROME_PROTOCOL: "openlinker.native-chrome.v2",
  OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256: "a".repeat(64),
};

test("native Chrome gate is opt-in and enables the image-installed extension", () => {
  assert.equal(NativeChromeGate.fromEnvironment({}), undefined);
  const gate = NativeChromeGate.fromEnvironment(validEnvironment);
  assert.deepEqual(gate?.ignoredDefaultArguments(), [
    "--disable-extensions",
    "--disable-default-apps",
  ]);
  assert.deepEqual(
    gate?.launchArguments([
      "--disable-background-networking",
      "--disable-default-apps",
      "--disable-sync",
    ]),
    ["--disable-background-networking", "--disable-sync"],
  );
  assert.equal(
    gate?.isInternalPage({
      url: () =>
        "chrome-extension://abcdefghijklmnopabcdefghijklmnop/openlinker-runtime/index.html",
    } as never),
    true,
  );
});

test("native Chrome gate rejects incomplete or untrusted image configuration", () => {
  assert.throws(
    () =>
      NativeChromeGate.fromEnvironment({
        ...validEnvironment,
        OPENLINKER_NATIVE_CHROME_EXTENSION_ID: "invalid",
      }),
    /EXTENSION_ID/,
  );
  assert.throws(
    () =>
      NativeChromeGate.fromEnvironment({
        ...validEnvironment,
        OPENLINKER_NATIVE_CHROME_SOCKET: "relative.sock",
      }),
    /absolute normalized path/,
  );
});

test("native Chrome gate binds a new Host and rebinds only the same authority", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-native-gate-"));
  const socketPath = path.join(root, "native.sock");
  let hostProcessGenerationNonce = "11111111-1111-4111-8111-111111111111";
  const calls: Array<{ method: string; actionKind?: string }> = [];
  const server = createServer((socket) => {
    let input = "";
    socket.setEncoding("utf8");
    socket.on("data", (chunk: string) => {
      input += chunk;
      const newline = input.indexOf("\n");
      if (newline < 0) return;
      const request = JSON.parse(input.slice(0, newline)) as {
        contract_id: string;
        request_id: string;
        method: string;
        params: { action_kind?: string };
      };
      calls.push(
        request.params.action_kind === undefined
          ? { method: request.method }
          : {
              method: request.method,
              actionKind: request.params.action_kind,
            },
      );
      const result =
        request.method === "preflight"
          ? {
              asset_manifest_sha256: "a".repeat(64),
              extension_id: "abcdefghijklmnopabcdefghijklmnop",
              extension_version: "1.2.3.4",
              host_process_generation_nonce: hostProcessGenerationNonce,
              native_host_protocol: "openlinker.native-chrome.v2",
              capabilities: REQUIRED_CAPABILITIES,
            }
          : {
              authorized: true,
              nonce: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            };
      socket.end(
        `${JSON.stringify({
          contract_id: request.contract_id,
          request_id: request.request_id,
          ok: true,
          result,
        })}\n`,
      );
    });
  });
  try {
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(socketPath, resolve);
    });
    const gate = NativeChromeGate.fromEnvironment({
      ...validEnvironment,
      OPENLINKER_NATIVE_CHROME_SOCKET: socketPath,
      OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT: root,
    });
    assert.ok(gate);
    await writeFile(path.join(root, "manifest.json"), "{}\n");
    await gate.activate({
      newPage: async () => ({
        close: async () => undefined,
        goto: async () => undefined,
        url: () =>
          "chrome-extension://abcdefghijklmnopabcdefghijklmnop/openlinker-runtime/index.html",
      }),
    } as never);
    const identity = {
      run_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      agent_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      principal_scope_id: "principal",
      browser_session_id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
      session_epoch: 1,
      attachment_id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
      control_epoch: 1,
      controller: "agent" as const,
      browser_interaction_policy: "restricted" as const,
      browser_interaction_policy_generation: 1,
      browser_mutation_origins: [],
      browser_mutation_origins_sha256: "f".repeat(64),
    };
    const request = {
      contract_id: "openlinker.browser.engine.v2" as const,
      action_id: "action-1",
      deadline: new Date(Date.now() + 10_000).toISOString(),
      identity,
      action: { kind: "navigate" as const },
    };

    await gate.authorize(request);
    await gate.authorizeObserver({
      contract_id: "openlinker.browser.engine.ops-observer.v1",
      action_id: "2",
      deadline: new Date(Date.now() + 500).toISOString(),
      identity,
      operation: "observe_status",
    });
    assert.deepEqual(calls, [
      { method: "preflight" },
      { method: "preflight" },
      { method: "authorize_action", actionKind: "preflight" },
      { method: "authorize_action", actionKind: "navigate" },
      { method: "authorize_observer" },
    ]);
    await assert.rejects(
      gate.authorizeObserver({
        contract_id: "openlinker.browser.engine.ops-observer.v1",
        action_id: "3",
        deadline: new Date(Date.now() + 500).toISOString(),
        identity: { ...identity, control_epoch: 2 },
        operation: "observe_frame",
      }),
      /does not match active Browser authority/,
    );
    hostProcessGenerationNonce = "22222222-2222-4222-8222-222222222222";
    await gate.authorize({
      ...request,
      action_id: "action-2",
      action: { kind: "navigate" },
    });
    assert.deepEqual(calls.slice(-3), [
      { method: "preflight" },
      { method: "authorize_action", actionKind: "preflight" },
      { method: "authorize_action", actionKind: "navigate" },
    ]);

    hostProcessGenerationNonce = "33333333-3333-4333-8333-333333333333";
    await assert.rejects(
      gate.authorize({
        ...request,
        action_id: "action-3",
        identity: { ...identity, control_epoch: 2 },
        action: { kind: "navigate" },
      }),
      /changed Browser authority/,
    );
    assert.equal(calls.at(-1)?.method, "preflight");
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await rm(root, { recursive: true, force: true });
  }
});
