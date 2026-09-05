import assert from "node:assert/strict";
import test from "node:test";

import type { Request, WebSocketRoute } from "playwright-core";

import {
  restoreAgentPagePolicy,
  routePageRequest,
  routePageWebSocket,
  type ControlState,
} from "./engine.js";

function socket(options?: { closeError?: Error; url?: string }) {
  let connected = 0;
  let closed = 0;
  const value = {
    connectToServer() {
      connected += 1;
      return value;
    },
    async close() {
      closed += 1;
      if (options?.closeError) throw options.closeError;
    },
    url() {
      return options?.url ?? "wss://public.example/socket";
    },
  } as unknown as WebSocketRoute;
  return {
    value,
    connected: () => connected,
    closed: () => closed,
  };
}

function fullState(): ControlState {
  const value = state("agent");
  value.interactionPolicy = "full";
  value.mutationOrigins = new Set(["https://public.example"]);
  value.frameOrigins = () => ["https://public.example/page"];
  return value;
}

function state(controller: "agent" | "human"): ControlState {
  return {
    controller,
    attachmentKey: controller === "human" ? "attachment" : "",
    pageWebSockets: new Set(),
    interactionPolicy: "restricted",
    mutationOrigins: new Set(),
    mutationBlockSerial: 0,
    blockedMutationRequests: 0,
    actionInFlight: false,
    frameOrigins: () => [],
  };
}

test("page WebSockets connect only for the human controller epoch", async () => {
  const human = state("human");
  const admitted = socket();
  await routePageWebSocket(human, admitted.value);
  assert.equal(admitted.connected(), 1);
  assert.equal(admitted.closed(), 0);
  assert.equal(human.pageWebSockets.size, 1);

  const agent = state("agent");
  const rejected = socket();
  await routePageWebSocket(agent, rejected.value);
  assert.equal(rejected.connected(), 0);
  assert.equal(rejected.closed(), 1);
  assert.equal(agent.pageWebSockets.size, 0);
});

test("release closes human WebSockets before restoring Agent policy", async () => {
  const control = state("human");
  const first = socket();
  const second = socket();
  control.pageWebSockets.add(first.value);
  control.pageWebSockets.add(second.value);

  await restoreAgentPagePolicy(control);
  assert.equal(first.closed(), 1);
  assert.equal(second.closed(), 1);
  assert.equal(control.pageWebSockets.size, 0);
  assert.equal(control.controller, "agent");
  assert.equal(control.attachmentKey, "");
});

test("WebSocket close failure never admits the Agent", async () => {
  const control = state("human");
  const failing = socket({ closeError: new Error("close failed") });
  control.pageWebSockets.add(failing.value);

  await assert.rejects(restoreAgentPagePolicy(control), /close failed/);
  assert.equal(control.controller, "human");
  assert.equal(control.attachmentKey, "attachment");
});

test("full page methods require allowlisted top source and destination", async () => {
  const request = (destination: string, method = "POST") =>
    ({
      method: () => method,
      url: () => destination,
      frame: () => ({
        url: () => "https://public.example/form",
        page: () => ({
          mainFrame: () => ({ url: () => "https://public.example/page" }),
        }),
      }),
    }) as unknown as Request;
  const operate = async (control: ControlState, value: Request) => {
    let admitted = 0;
    let blocked = 0;
    await routePageRequest(control, value, {
      continue: async () => {
        admitted++;
      },
      abort: async () => {
        blocked++;
      },
    });
    return { admitted, blocked };
  };

  assert.deepEqual(
    await operate(fullState(), request("https://public.example/mutation")),
    { admitted: 1, blocked: 0 },
  );
  const crossOrigin = fullState();
  assert.deepEqual(
    await operate(crossOrigin, request("https://collector.example/mutation")),
    { admitted: 0, blocked: 1 },
  );
  assert.equal(crossOrigin.mutationBlockSerial, 1);
  assert.deepEqual(
    await operate(fullState(), request("https://public.example/trace", "TRACE")),
    { admitted: 0, blocked: 1 },
  );
  assert.deepEqual(
    await operate(state("agent"), request("https://public.example/mutation")),
    { admitted: 0, blocked: 1 },
  );
});

test("full page WebSockets fail closed outside the exact wss scope", async () => {
  const admittedState = fullState();
  const admitted = socket();
  await routePageWebSocket(admittedState, admitted.value);
  assert.equal(admitted.connected(), 1);

  for (const url of [
    "ws://public.example/socket",
    "wss://collector.example/socket",
  ]) {
    const control = fullState();
    const rejected = socket({ url });
    await routePageWebSocket(control, rejected.value);
    assert.equal(rejected.connected(), 0);
    assert.equal(rejected.closed(), 1);
    assert.equal(control.mutationBlockSerial, 1);
  }

  const ambiguousInitiator = fullState();
  ambiguousInitiator.frameOrigins = () => [
    "https://public.example/page",
    "https://third-party-frame.example/embed",
  ];
  const rejectedForForeignFrame = socket();
  await routePageWebSocket(ambiguousInitiator, rejectedForForeignFrame.value);
  assert.equal(rejectedForForeignFrame.connected(), 0);
  assert.equal(rejectedForForeignFrame.closed(), 1);
  assert.equal(ambiguousInitiator.mutationBlockSerial, 1);
});
