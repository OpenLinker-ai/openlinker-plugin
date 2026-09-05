import assert from "node:assert/strict";
import http from "node:http";
import test from "node:test";

import {
  gatewayBlockedResponse,
  isTunnelConnectionFailure,
  probeEgressGateway,
} from "./egress-policy.js";

test("recognizes only the egress gateway blocked decision", () => {
  assert.equal(
    gatewayBlockedResponse({ "x-openlinker-egress-decision": "blocked" }),
    true,
  );
  assert.equal(
    gatewayBlockedResponse({ "x-openlinker-egress-decision": "allowed" }),
    false,
  );
  assert.equal(gatewayBlockedResponse({}), false);
});

test("recognizes Chromium HTTPS tunnel failures", () => {
  assert.equal(
    isTunnelConnectionFailure(
      new Error("page.goto: net::ERR_TUNNEL_CONNECTION_FAILED"),
    ),
    true,
  );
  assert.equal(
    isTunnelConnectionFailure(
      new Error("page.goto: net::ERR_CONNECTION_TIMED_OUT"),
    ),
    false,
  );
  assert.equal(isTunnelConnectionFailure("ERR_TUNNEL_CONNECTION_FAILED"), false);
});

test("probes the real gateway health wire contract", async () => {
  const server = http.createServer((request, response) => {
    assert.equal(request.method, "GET");
    assert.equal(request.url, "/.well-known/openlinker-egress-health");
    response.setHeader("X-OpenLinker-Egress-Decision", "healthy");
    response.writeHead(204);
    response.end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const address = server.address();
    assert.ok(address !== null && typeof address === "object");
    assert.equal(
      await probeEgressGateway(`http://127.0.0.1:${address.port}`, 1_000),
      true,
    );
  } finally {
    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error === undefined) {
          resolve();
        } else {
          reject(error);
        }
      });
    });
  }
});

test("fails a gateway health probe closed", async () => {
  const server = http.createServer((_request, response) => {
    response.setHeader("X-OpenLinker-Egress-Decision", "blocked");
    response.writeHead(403);
    response.end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const address = server.address();
    assert.ok(address !== null && typeof address === "object");
    assert.equal(
      await probeEgressGateway(`http://127.0.0.1:${address.port}`, 1_000),
      false,
    );
  } finally {
    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error === undefined) {
          resolve();
        } else {
          reject(error);
        }
      });
    });
  }
});
