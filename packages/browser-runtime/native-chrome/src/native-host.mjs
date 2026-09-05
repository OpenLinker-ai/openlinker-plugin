#!/usr/bin/env node

import { randomUUID } from "node:crypto";
import {
  chmodSync,
  existsSync,
  lstatSync,
  unlinkSync,
} from "node:fs";
import { createServer } from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { encodeNativeMessage, NativeFrameDecoder } from "./native-framing.mjs";

const CONTROL_CONTRACT = "openlinker.native-chrome.control.v2";
const MAX_CONTROL_BYTES = 1024 * 1024;
const REQUIRED_CAPABILITIES = [
  "act",
  "back",
  "batch",
  "checkpoint",
  "click",
  "close",
  "forward",
  "full",
  "keypress",
  "navigate",
  "observe",
  "ops_observe_frame",
  "ops_observe_status",
  "policy_evidence",
  "restricted",
  "screenshot",
  "scroll",
  "select",
  "semantic",
  "type_non_secret",
  "wait",
];
const ACTION_KINDS = new Set([
  "navigate",
  "click",
  "type_non_secret",
  "scroll",
  "keypress",
  "select",
  "wait",
  "back",
  "forward",
  "screenshot",
  "checkpoint",
  "preflight",
  "batch",
]);
const VIEWER_OPERATIONS = new Set(["enter", "frame", "input", "exit"]);
const OBSERVER_OPERATIONS = new Set(["observe_status", "observe_frame"]);

function requireEnvironment(environment = process.env) {
  const config = {
    socketPath: requireAbsolutePath(
      environment.OPENLINKER_NATIVE_CHROME_SOCKET,
      "OPENLINKER_NATIVE_CHROME_SOCKET",
    ),
    extensionID: requireMatch(
      environment.OPENLINKER_NATIVE_CHROME_EXTENSION_ID,
      /^[a-p]{32}$/,
      "extension ID",
    ),
    extensionVersion: requireMatch(
      environment.OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION,
      /^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/,
      "extension version",
    ),
    nativeHostProtocol: requireMatch(
      environment.OPENLINKER_NATIVE_CHROME_PROTOCOL,
      /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/,
      "Native Host protocol",
    ),
    assetManifestSHA256: requireMatch(
      environment.OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256,
      /^[0-9a-f]{64}$/,
      "asset manifest digest",
    ),
  };
  const sourceOrigin = process.argv[2] ?? "";
  if (
    environment.OPENLINKER_NATIVE_CHROME_REQUIRE_ORIGIN === "true" &&
    sourceOrigin !== `chrome-extension://${config.extensionID}/`
  ) {
    throw new Error("native messaging source origin is invalid");
  }
  return config;
}

function requireAbsolutePath(value, label) {
  if (typeof value !== "string" || !path.isAbsolute(value) || path.normalize(value) !== value) {
    throw new Error(`${label} must be an absolute normalized path`);
  }
  return value;
}

function requireMatch(value, pattern, label) {
  if (typeof value !== "string" || !pattern.test(value)) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function exactObject(value, keys, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (
    actual.length !== expected.length ||
    actual.some((key, index) => key !== expected[index])
  ) {
    throw new Error(`${label} fields are invalid`);
  }
  return value;
}

function validateControlRequest(value) {
  const request = exactObject(
    value,
    ["contract_id", "request_id", "method", "params"],
    "control request",
  );
  if (
    request.contract_id !== CONTROL_CONTRACT ||
    typeof request.request_id !== "string" ||
    !/^[0-9a-f-]{36}$/.test(request.request_id)
  ) {
    throw new Error("control request identity is invalid");
  }
  if (
    !new Set([
      "preflight",
      "authorize_action",
      "authorize_viewer",
      "authorize_observer",
    ]).has(request.method)
  ) {
    throw new Error("control request method is invalid");
  }
  const params = exactObject(
    request.params,
    request.method === "preflight"
      ? [
          "asset_manifest_sha256",
          "extension_id",
          "extension_version",
          "native_host_protocol",
          "capabilities",
        ]
      : request.method === "authorize_action"
        ? [
            "browser_session_id",
            "session_epoch",
            "attachment_id",
            "control_epoch",
            "interaction_policy",
            "interaction_policy_generation",
            "mutation_origins_sha256",
            "action_kind",
          ]
        : request.method === "authorize_viewer"
          ? [
            "browser_session_id",
            "session_epoch",
            "attachment_id",
            "control_epoch",
            "interaction_policy",
            "interaction_policy_generation",
            "mutation_origins_sha256",
            "viewer_operation",
          ]
          : [
              "browser_session_id",
              "session_epoch",
              "attachment_id",
              "control_epoch",
              "interaction_policy",
              "interaction_policy_generation",
              "mutation_origins_sha256",
              "observer_operation",
            ],
    "control request params",
  );
  if (request.method !== "preflight") validateAuthorityParams(params);
  if (request.method === "authorize_action" && !ACTION_KINDS.has(params.action_kind)) {
    throw new Error("action kind is invalid");
  }
  if (
    request.method === "authorize_viewer" &&
    !VIEWER_OPERATIONS.has(params.viewer_operation)
  ) {
    throw new Error("viewer operation is invalid");
  }
  if (
    request.method === "authorize_observer" &&
    !OBSERVER_OPERATIONS.has(params.observer_operation)
  ) {
    throw new Error("observer operation is invalid");
  }
  return { request, params };
}

function validateAuthorityParams(params) {
  for (const field of ["browser_session_id", "attachment_id"]) {
    if (typeof params[field] !== "string" || !/^[0-9a-f-]{36}$/.test(params[field])) {
      throw new Error("Browser authority identity is invalid");
    }
  }
  for (const field of [
    "session_epoch",
    "control_epoch",
    "interaction_policy_generation",
  ]) {
    if (!Number.isSafeInteger(params[field]) || params[field] < 1) {
      throw new Error("Browser authority generation is invalid");
    }
  }
  if (!new Set(["restricted", "full"]).has(params.interaction_policy)) {
    throw new Error("Browser interaction policy is invalid");
  }
  if (
    typeof params.mutation_origins_sha256 !== "string" ||
    !/^[0-9a-f]{64}$/.test(params.mutation_origins_sha256)
  ) {
    throw new Error("Browser mutation-origin digest is invalid");
  }
}

function createAuthorityFence() {
  const recentRequestIDs = new Set();
  const requestOrder = [];
  let preflightComplete = false;
  let authority;

  return {
    admit(request, params) {
      if (recentRequestIDs.has(request.request_id)) {
        throw new Error("duplicate control request identity");
      }
      recentRequestIDs.add(request.request_id);
      requestOrder.push(request.request_id);
      if (requestOrder.length > 4096) {
        recentRequestIDs.delete(requestOrder.shift());
      }
      if (request.method === "preflight") {
        preflightComplete = true;
        return;
      }
      if (!preflightComplete) {
        throw new Error("Native Host preflight is required");
      }
      const next = {
        browser_session_id: params.browser_session_id,
        session_epoch: params.session_epoch,
        attachment_id: params.attachment_id,
        control_epoch: params.control_epoch,
        interaction_policy: params.interaction_policy,
        interaction_policy_generation: params.interaction_policy_generation,
        mutation_origins_sha256: params.mutation_origins_sha256,
      };
      const authorityPreflight =
        request.method === "authorize_action" && params.action_kind === "preflight";
      if (authority === undefined) {
        if (!authorityPreflight) {
          throw new Error("Native Host Browser authority preflight is required");
        }
        authority = next;
        return;
      }
      if (authorityPreflight) {
        if (
          next.browser_session_id !== authority.browser_session_id ||
          next.session_epoch !== authority.session_epoch + 1 ||
          next.attachment_id === authority.attachment_id ||
          next.control_epoch <= authority.control_epoch ||
          next.interaction_policy !== authority.interaction_policy ||
          next.interaction_policy_generation !==
            authority.interaction_policy_generation ||
          next.mutation_origins_sha256 !== authority.mutation_origins_sha256
        ) {
          throw new Error("Native Host Browser authority rotation is invalid");
        }
        authority = next;
        return;
      }
      if (request.method === "authorize_observer") {
        for (const field of [
          "browser_session_id",
          "session_epoch",
          "attachment_id",
          "control_epoch",
          "interaction_policy",
          "interaction_policy_generation",
          "mutation_origins_sha256",
        ]) {
          if (next[field] !== authority[field]) {
            throw new Error("Native Host Ops Observer authority changed");
          }
        }
        return;
      }
      for (const field of [
        "browser_session_id",
        "session_epoch",
        "attachment_id",
        "interaction_policy",
        "interaction_policy_generation",
        "mutation_origins_sha256",
      ]) {
        if (next[field] !== authority[field]) {
          throw new Error("Native Host Browser authority changed");
        }
      }
      if (next.control_epoch < authority.control_epoch) {
        throw new Error("Native Host control epoch is stale");
      }
      authority = next;
    },
  };
}

async function authorizeControlRequest(request, params, options) {
  const {
    authorityFence,
    config,
    extensionHealth,
    hostProcessGenerationNonce,
    authorizationNonce = randomUUID,
  } = options;
  // The Native Host exists only while Chrome's extension-owned Native
  // Messaging port remains open; stdin ending shuts the Host and its control
  // socket down. Observer requests therefore retain the exact Host authority
  // fence without repeating ping + getInfo inside the 500 ms frame budget.
  if (request.method !== "authorize_observer") {
    await extensionHealth(15_000);
  }
  if (request.method === "preflight") {
    if (
      params.asset_manifest_sha256 !== config.assetManifestSHA256 ||
      params.extension_id !== config.extensionID ||
      params.extension_version !== config.extensionVersion ||
      params.native_host_protocol !== config.nativeHostProtocol ||
      JSON.stringify(params.capabilities) !== JSON.stringify(REQUIRED_CAPABILITIES)
    ) {
      throw new Error("preflight capability evidence is invalid");
    }
    authorityFence.admit(request, params);
    return {
      asset_manifest_sha256: config.assetManifestSHA256,
      extension_id: config.extensionID,
      extension_version: config.extensionVersion,
      host_process_generation_nonce: hostProcessGenerationNonce,
      native_host_protocol: config.nativeHostProtocol,
      capabilities: REQUIRED_CAPABILITIES,
    };
  }
  authorityFence.admit(request, params);
  return { authorized: true, nonce: authorizationNonce() };
}

function startNativeHost(environment = process.env) {
  const config = requireEnvironment(environment);
  const decoder = new NativeFrameDecoder();
  const pending = new Map();
  let nextRequestID = 1;
  const authorityFence = createAuthorityFence();
  const hostProcessGenerationNonce = randomUUID();
  const controlSockets = new Set();
  let server;
  let shuttingDown = false;
  let socketIdentity;

  const removeSocket = () => {
    if (!existsSync(config.socketPath) || socketIdentity === undefined) return;
    const status = lstatSync(config.socketPath);
    if (
      status.isSocket() &&
      status.dev === socketIdentity.dev &&
      status.ino === socketIdentity.ino
    ) {
      unlinkSync(config.socketPath);
    }
  };
  const shutdown = () => {
    if (shuttingDown) return;
    shuttingDown = true;
    for (const socket of controlSockets) socket.destroy();
    controlSockets.clear();
    const finish = () => {
      removeSocket();
      process.exit(process.exitCode ?? 0);
    };
    if (server?.listening) {
      server.close(finish);
    } else {
      finish();
    }
  };

  const nativeWrite = (message) => process.stdout.write(encodeNativeMessage(message));
  const nativeRequest = (method, params, timeoutMs = 15_000) => {
    if (pending.size >= 16) return Promise.reject(new Error("extension request limit reached"));
    const id = `openlinker-${nextRequestID}`;
    nextRequestID += 1;
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        pending.delete(id);
        reject(new Error("extension request timed out"));
      }, timeoutMs);
      pending.set(id, { resolve, reject, timeout });
      nativeWrite({ jsonrpc: "2.0", id, method, params });
    });
  };

  const handleNativeMessage = (value) => {
    const message = exactObject(
      value,
      Object.hasOwn(value, "method")
        ? Object.hasOwn(value, "id")
          ? ["jsonrpc", "id", "method", "params"]
          : ["jsonrpc", "method", "params"]
        : Object.hasOwn(value, "error")
          ? ["jsonrpc", "id", "error"]
          : ["jsonrpc", "id", "result"],
      "native message",
    );
    if (message.method === "codexRuntime/hello" && message.id !== undefined) {
      nativeWrite({
        jsonrpc: "2.0",
        id: message.id,
        result: {
          manifestSchemaVersion: 2,
          nativeHostProtocolVersion: 2,
          supportedProtocolVersions: [2],
          supportedMethods: [],
        },
      });
      return;
    }
    if (message.id === undefined || message.method !== undefined) return;
    const entry = pending.get(String(message.id));
    if (entry === undefined) return;
    pending.delete(String(message.id));
    clearTimeout(entry.timeout);
    if (message.error !== undefined) {
      entry.reject(new Error("extension request failed"));
    } else {
      entry.resolve(message.result);
    }
  };

  process.stdin.on("data", (chunk) => {
    try {
      for (const message of decoder.push(chunk)) handleNativeMessage(message);
    } catch {
      process.exitCode = 70;
      process.stdin.pause();
      shutdown();
    }
  });
  process.stdin.on("end", () => {
    for (const entry of pending.values()) {
      clearTimeout(entry.timeout);
      entry.reject(new Error("native transport closed"));
    }
    pending.clear();
    shutdown();
  });
  process.stdin.resume();

  const extensionHealth = async (timeoutMs = 15_000) => {
    const deadline = Date.now() + timeoutMs;
    const pong = await nativeRequest("ping", {}, Math.max(1, deadline - Date.now()));
    if (pong !== "pong") throw new Error("extension ping failed");
    const info = await nativeRequest("getInfo", {}, Math.max(1, deadline - Date.now()));
    if (info === null || typeof info !== "object" || Array.isArray(info)) {
      throw new Error("extension identity is invalid");
    }
    const metadata = info.metadata;
    if (
      info.type !== "extension" ||
      info.version !== config.extensionVersion ||
      metadata === null ||
      typeof metadata !== "object" ||
      Array.isArray(metadata) ||
      metadata.extensionId !== config.extensionID
    ) {
      throw new Error("extension identity is invalid");
    }
  };

  if (existsSync(config.socketPath)) {
    const status = lstatSync(config.socketPath);
    if (!status.isSocket() || status.uid !== process.getuid()) {
      throw new Error("refusing to replace an unowned Native Host socket");
    }
    unlinkSync(config.socketPath);
  }
  server = createServer((socket) => {
    controlSockets.add(socket);
    let buffered = "";
    socket.setEncoding("utf8");
    socket.on("close", () => controlSockets.delete(socket));
    socket.on("error", () => undefined);
    socket.on("data", (chunk) => {
      buffered += chunk;
      if (Buffer.byteLength(buffered) > MAX_CONTROL_BYTES) {
        socket.destroy(new Error("control request is too large"));
        return;
      }
      const newline = buffered.indexOf("\n");
      if (newline < 0) return;
      const raw = buffered.slice(0, newline);
      buffered = "";
      let validated;
      try {
        validated = validateControlRequest(JSON.parse(raw));
      } catch (error) {
        socket.end(
          `${JSON.stringify({
            contract_id: CONTROL_CONTRACT,
            request_id: "invalid",
            ok: false,
            error: error instanceof Error ? error.message : "invalid request",
          })}\n`,
        );
        return;
      }
      const { request, params } = validated;
      const complete = (result) =>
        socket.end(
          `${JSON.stringify({
            contract_id: CONTROL_CONTRACT,
            request_id: request.request_id,
            ok: true,
            result,
          })}\n`,
        );
      authorizeControlRequest(request, params, {
        authorityFence,
        config,
        extensionHealth,
        hostProcessGenerationNonce,
      })
        .then(complete)
        .catch((error) => {
          socket.end(
            `${JSON.stringify({
              contract_id: CONTROL_CONTRACT,
              request_id: request.request_id,
              ok: false,
              error: error instanceof Error ? error.message : "extension unavailable",
            })}\n`,
          );
        });
    });
  });
  server.listen(config.socketPath, () => {
    chmodSync(config.socketPath, 0o600);
    const status = lstatSync(config.socketPath);
    socketIdentity = { dev: status.dev, ino: status.ino };
  });

  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
  return server;
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  try {
    startNativeHost();
  } catch {
    process.exitCode = 70;
  }
}

export {
  ACTION_KINDS,
  authorizeControlRequest,
  CONTROL_CONTRACT,
  REQUIRED_CAPABILITIES,
  createAuthorityFence,
  requireEnvironment,
  startNativeHost,
  validateControlRequest,
};
