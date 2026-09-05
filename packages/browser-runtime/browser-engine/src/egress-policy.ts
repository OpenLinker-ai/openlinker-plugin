import http from "node:http";

import {
  EGRESS_DECISION_HEADER,
  EGRESS_HEALTH_DECISION,
  EGRESS_HEALTH_PATH,
} from "./egress-contract.generated.js";

export function gatewayBlockedResponse(
  headers: Readonly<Record<string, string>>,
): boolean {
  return headers[EGRESS_DECISION_HEADER] === "blocked";
}

export function isTunnelConnectionFailure(error: unknown): boolean {
  return (
    error instanceof Error &&
    error.message.includes("ERR_TUNNEL_CONNECTION_FAILED")
  );
}

export async function probeEgressGateway(
  proxyOrigin: string,
  timeout: number,
): Promise<boolean> {
  const healthURL = new URL(EGRESS_HEALTH_PATH, proxyOrigin);
  const boundedTimeout = Math.max(1, Math.min(timeout, 1_000));
  return new Promise((resolve) => {
    let settled = false;
    const finish = (healthy: boolean): void => {
      if (settled) {
        return;
      }
      settled = true;
      resolve(healthy);
    };
    const request = http.get(
      healthURL,
      {
        headers: {
          Connection: "close",
        },
      },
      (response) => {
        const healthy =
          response.statusCode === 204 &&
          response.headers[EGRESS_DECISION_HEADER] === EGRESS_HEALTH_DECISION;
        response.resume();
        response.once("end", () => finish(healthy));
        response.once("error", () => finish(false));
      },
    );
    request.setTimeout(boundedTimeout, () => {
      request.destroy(new Error("egress gateway health probe timed out"));
    });
    request.once("error", () => finish(false));
  });
}
