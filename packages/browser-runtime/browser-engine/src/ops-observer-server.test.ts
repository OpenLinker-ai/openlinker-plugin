import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, rm, stat } from "node:fs/promises";
import { createConnection } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import type { BrowserEngine } from "./engine.js";
import { OpsObserverServer } from "./ops-observer-server.js";
import {
  ENGINE_OPS_OBSERVER_CONTRACT_ID,
  opsObserverSuccess,
  type EngineOpsObserverRequest,
  type EngineOpsObserverResponse,
} from "./protocol.js";

function request(): EngineOpsObserverRequest {
  const origins: string[] = [];
  return {
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: "1",
    deadline: new Date(Date.now() + 2_000).toISOString(),
    identity: {
      run_id: "11111111-1111-4111-8111-111111111111",
      agent_id: "22222222-2222-4222-8222-222222222222",
      principal_scope_id: "scope",
      browser_session_id: "33333333-3333-4333-8333-333333333333",
      session_epoch: 1,
      attachment_id: "44444444-4444-4444-8444-444444444444",
      control_epoch: 1,
      controller: "agent",
      browser_interaction_policy: "restricted",
      browser_interaction_policy_generation: 1,
      browser_mutation_origins: origins,
      browser_mutation_origins_sha256: createHash("sha256")
        .update(JSON.stringify(origins))
        .digest("hex"),
    },
    operation: "observe_status",
  };
}

test("Engine Ops Observer UDS is opt-in, owner-only and one-shot", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-ops-"));
  const socketPath = path.join(root, "observer.sock");
  let observed: EngineOpsObserverRequest | undefined;
  const engine = {
    executeOpsObserver: async (
      value: EngineOpsObserverRequest,
    ): Promise<EngineOpsObserverResponse> => {
      observed = value;
      await new Promise((resolve) => setTimeout(resolve, 10));
      return opsObserverSuccess(value.action_id, "about:blank", "Blank");
    },
  } as unknown as BrowserEngine;
  try {
    assert.equal(
      await OpsObserverServer.start(engine, {
        OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED: "false",
      }),
      undefined,
    );
    const server = await OpsObserverServer.start(engine, {
      OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED: "true",
      OPENLINKER_BROWSER_ENGINE_OPS_SOCKET: socketPath,
    });
    assert.ok(server);
    assert.equal((await stat(socketPath)).mode & 0o777, 0o600);
    const response = await exchange(socketPath, request());
    assert.equal(response.status, "ok");
    assert.equal(response.page_url, "about:blank");
    assert.equal(observed?.operation, "observe_status");
    await server.close();
    await assert.rejects(stat(socketPath), { code: "ENOENT" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

function exchange(
  socketPath: string,
  value: EngineOpsObserverRequest,
): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    let output = "";
    socket.setEncoding("utf8");
    // Match ProcessEngine: one framed request followed by CloseWrite. The
    // Engine must still keep its writable half alive for the async response.
    socket.on("connect", () => socket.end(`${JSON.stringify(value)}\n`));
    socket.on("data", (chunk: string) => {
      output += chunk;
    });
    socket.on("end", () => {
      try {
        resolve(JSON.parse(output) as Record<string, unknown>);
      } catch (error) {
        reject(error);
      }
    });
    socket.on("error", reject);
  });
}
