const NATIVE_HOST_NAME = "ai.openlinker.browser";
const MAX_REQUEST_ID_BYTES = 128;

function isExactObject(value, keys) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  return (
    actual.length === expected.length &&
    actual.every((key, index) => key === expected[index])
  );
}

function validRequestID(value) {
  const bytes = new TextEncoder().encode(String(value)).byteLength;
  return (
    (typeof value === "string" || Number.isSafeInteger(value)) &&
    bytes >= 1 &&
    bytes <= MAX_REQUEST_ID_BYTES
  );
}

function validExtensionID(value) {
  return typeof value === "string" && /^[a-p]{32}$/.test(value);
}

function validVersion(value) {
  return (
    typeof value === "string" &&
    /^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(value)
  );
}

function invalidRequest(id = null) {
  return {
    jsonrpc: "2.0",
    id: validRequestID(id) ? id : null,
    error: { code: -32600, message: "invalid request" },
  };
}

function createExtensionInfo(manifest, extensionID) {
  if (
    manifest === null ||
    typeof manifest !== "object" ||
    Array.isArray(manifest) ||
    !validVersion(manifest.version) ||
    !validExtensionID(extensionID)
  ) {
    throw new Error("extension identity is invalid");
  }
  return {
    type: "extension",
    version: manifest.version,
    metadata: { extensionId: extensionID },
  };
}

function handleNativeRequest(value, manifest, extensionID) {
  if (
    !isExactObject(value, ["jsonrpc", "id", "method", "params"]) ||
    value.jsonrpc !== "2.0" ||
    !validRequestID(value.id) ||
    typeof value.method !== "string" ||
    !isExactObject(value.params, [])
  ) {
    return invalidRequest(value?.id);
  }
  if (value.method === "ping") {
    return { jsonrpc: "2.0", id: value.id, result: "pong" };
  }
  if (value.method === "getInfo") {
    try {
      return {
        jsonrpc: "2.0",
        id: value.id,
        result: createExtensionInfo(manifest, extensionID),
      };
    } catch {
      return invalidRequest(value.id);
    }
  }
  return {
    jsonrpc: "2.0",
    id: value.id,
    error: { code: -32601, message: "method not found" },
  };
}

export {
  MAX_REQUEST_ID_BYTES,
  NATIVE_HOST_NAME,
  createExtensionInfo,
  handleNativeRequest,
};
