import assert from "node:assert/strict";
import test from "node:test";

import {
  authorizeControlRequest,
  createAuthorityFence,
  REQUIRED_CAPABILITIES,
  requireEnvironment,
  validateControlRequest,
} from "../src/native-host.mjs";
import { encodeNativeMessage, NativeFrameDecoder } from "../src/native-framing.mjs";

const environment = {
  OPENLINKER_NATIVE_CHROME_SOCKET: "/run/openlinker/native-chrome.sock",
  OPENLINKER_NATIVE_CHROME_EXTENSION_ID:
    "abcdefghijklmnopabcdefghijklmnop",
  OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION: "1.2.3.4",
  OPENLINKER_NATIVE_CHROME_PROTOCOL: "openlinker.native-chrome.v2",
  OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256: "a".repeat(64),
};

test("Native Messaging framing is bounded and incremental", () => {
  const decoder = new NativeFrameDecoder();
  const frame = encodeNativeMessage({ jsonrpc: "2.0", id: "one", result: "pong" });
  assert.deepEqual(decoder.push(frame.subarray(0, 3)), []);
  assert.deepEqual(decoder.push(frame.subarray(3)), [
    { jsonrpc: "2.0", id: "one", result: "pong" },
  ]);
  const invalid = Buffer.alloc(4);
  invalid.writeUInt32LE(9 * 1024 * 1024, 0);
  assert.throws(() => decoder.push(invalid), /length/);
});

test("Native Host environment requires locked image identities", () => {
  assert.deepEqual(requireEnvironment(environment), {
    socketPath: "/run/openlinker/native-chrome.sock",
    extensionID: "abcdefghijklmnopabcdefghijklmnop",
    extensionVersion: "1.2.3.4",
    nativeHostProtocol: "openlinker.native-chrome.v2",
    assetManifestSHA256: "a".repeat(64),
  });
  assert.throws(
    () => requireEnvironment({ ...environment, OPENLINKER_NATIVE_CHROME_EXTENSION_ID: "invalid" }),
    /extension ID/,
  );
});

test("control protocol carries typed authority but no URL or text", () => {
  const request = {
    contract_id: "openlinker.native-chrome.control.v2",
    request_id: "11111111-1111-4111-8111-111111111111",
    method: "authorize_action",
    params: {
      browser_session_id: "22222222-2222-4222-8222-222222222222",
      session_epoch: 1,
      attachment_id: "33333333-3333-4333-8333-333333333333",
      control_epoch: 1,
      interaction_policy: "restricted",
      interaction_policy_generation: 1,
      mutation_origins_sha256: "a".repeat(64),
      action_kind: "navigate",
    },
  };
  assert.equal(validateControlRequest(request).params.action_kind, "navigate");
  assert.throws(
    () =>
      validateControlRequest({
        ...request,
        params: { ...request.params, url: "https://secret.example" },
      }),
    /fields/,
  );
  assert.throws(
    () => validateControlRequest({ ...request, params: { ...request.params, action_kind: "evaluate" } }),
    /action kind/,
  );
  const observer = {
    ...request,
    request_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    method: "authorize_observer",
    params: {
      ...request.params,
      observer_operation: "observe_frame",
    },
  };
  delete observer.params.action_kind;
  assert.equal(
    validateControlRequest(observer).params.observer_operation,
    "observe_frame",
  );
  assert.throws(
    () => validateControlRequest({
      ...observer,
      params: { ...observer.params, url: "https://secret.example/path?token=x" },
    }),
    /fields/,
  );
});

test("preflight requires the complete sorted browser_session capability set", () => {
  assert.equal(REQUIRED_CAPABILITIES.length, 21);
  assert.deepEqual([...REQUIRED_CAPABILITIES].sort(), REQUIRED_CAPABILITIES);
});

test("Native Host fences preflight, replay and Browser authority generation", () => {
  const fence = createAuthorityFence();
  const action = {
    contract_id: "openlinker.native-chrome.control.v2",
    request_id: "11111111-1111-4111-8111-111111111111",
    method: "authorize_action",
    params: {
      browser_session_id: "22222222-2222-4222-8222-222222222222",
      session_epoch: 1,
      attachment_id: "33333333-3333-4333-8333-333333333333",
      control_epoch: 1,
      interaction_policy: "restricted",
      interaction_policy_generation: 1,
      mutation_origins_sha256: "a".repeat(64),
      action_kind: "navigate",
    },
  };
  assert.throws(() => fence.admit(action, action.params), /preflight/);
  const preflight = {
    ...action,
    request_id: "44444444-4444-4444-8444-444444444444",
    method: "preflight",
  };
  fence.admit(preflight, {});
  fence.admit(
    {
      ...preflight,
      request_id: "12121212-1212-4212-8212-121212121212",
    },
    {},
  );
  const admitted = {
    ...action,
    request_id: "55555555-5555-4555-8555-555555555555",
    params: { ...action.params, action_kind: "preflight" },
  };
  fence.admit(admitted, admitted.params);
  assert.throws(() => fence.admit(admitted, admitted.params), /duplicate/);
  const observed = {
    ...action,
    request_id: "abababab-abab-4bab-8bab-abababababab",
    method: "authorize_observer",
    params: {
      ...action.params,
      observer_operation: "observe_status",
    },
  };
  delete observed.params.action_kind;
  fence.admit(observed, observed.params);
  const changedObserver = {
    ...observed,
    request_id: "cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd",
    params: { ...observed.params, control_epoch: 2 },
  };
  assert.throws(
    () => fence.admit(changedObserver, changedObserver.params),
    /Observer authority changed/,
  );
  const rotated = {
    ...action,
    request_id: "88888888-8888-4888-8888-888888888888",
    params: {
      ...action.params,
      session_epoch: 2,
      attachment_id: "99999999-9999-4999-8999-999999999999",
      control_epoch: 2,
      action_kind: "preflight",
    },
  };
  fence.admit(rotated, rotated.params);
  const oldAuthority = {
    ...action,
    request_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  };
  assert.throws(
    () => fence.admit(oldAuthority, oldAuthority.params),
    /authority changed/,
  );
  const stale = {
    ...action,
    request_id: "66666666-6666-4666-8666-666666666666",
    params: {
      ...rotated.params,
      browser_session_id: "77777777-7777-4777-8777-777777777777",
      action_kind: "navigate",
    },
  };
  assert.throws(() => fence.admit(stale, stale.params), /authority changed/);
});

test("Ops Observer keeps the Host authority fence without a redundant extension round trip", async () => {
  const fence = createAuthorityFence();
  const config = {
    assetManifestSHA256: "b".repeat(64),
    extensionID: "abcdefghijklmnopabcdefghijklmnop",
    extensionVersion: "1.2.3.4",
    nativeHostProtocol: "openlinker.native-chrome.v2",
  };
  const authority = {
    browser_session_id: "22222222-2222-4222-8222-222222222222",
    session_epoch: 1,
    attachment_id: "33333333-3333-4333-8333-333333333333",
    control_epoch: 1,
    interaction_policy: "restricted",
    interaction_policy_generation: 1,
    mutation_origins_sha256: "a".repeat(64),
  };
  let healthChecks = 0;
  const options = {
    authorityFence: fence,
    config,
    extensionHealth: async () => {
      healthChecks += 1;
    },
    hostProcessGenerationNonce: "44444444-4444-4444-8444-444444444444",
    authorizationNonce: () => "55555555-5555-4555-8555-555555555555",
  };
  await authorizeControlRequest(
    {
      contract_id: "openlinker.native-chrome.control.v2",
      request_id: "11111111-1111-4111-8111-111111111111",
      method: "preflight",
    },
    {
      asset_manifest_sha256: config.assetManifestSHA256,
      extension_id: config.extensionID,
      extension_version: config.extensionVersion,
      native_host_protocol: config.nativeHostProtocol,
      capabilities: REQUIRED_CAPABILITIES,
    },
    options,
  );
  const actionPreflight = {
    contract_id: "openlinker.native-chrome.control.v2",
    request_id: "22222222-2222-4222-8222-222222222222",
    method: "authorize_action",
    params: { ...authority, action_kind: "preflight" },
  };
  await authorizeControlRequest(
    actionPreflight,
    actionPreflight.params,
    options,
  );
  assert.equal(healthChecks, 2);
  const observer = {
    contract_id: "openlinker.native-chrome.control.v2",
    request_id: "33333333-3333-4333-8333-333333333333",
    method: "authorize_observer",
    params: { ...authority, observer_operation: "observe_status" },
  };
  const result = await authorizeControlRequest(observer, observer.params, options);
  assert.equal(healthChecks, 2);
  assert.deepEqual(result, {
    authorized: true,
    nonce: "55555555-5555-4555-8555-555555555555",
  });

  const changed = {
    ...observer,
    request_id: "66666666-6666-4666-8666-666666666666",
    params: { ...observer.params, control_epoch: 2 },
  };
  await assert.rejects(
    authorizeControlRequest(changed, changed.params, options),
    /Observer authority changed/,
  );
  assert.equal(healthChecks, 2);
});
