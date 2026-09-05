const MAX_NATIVE_MESSAGE_BYTES = 8 * 1024 * 1024;

class NativeFrameDecoder {
  #buffer = Buffer.alloc(0);

  push(chunk) {
    if (!Buffer.isBuffer(chunk)) throw new Error("native message chunk must be bytes");
    this.#buffer = Buffer.concat([this.#buffer, chunk]);
    const values = [];
    for (;;) {
      if (this.#buffer.byteLength < 4) break;
      const length = this.#buffer.readUInt32LE(0);
      if (length < 2 || length > MAX_NATIVE_MESSAGE_BYTES) {
        throw new Error("native message length is invalid");
      }
      if (this.#buffer.byteLength < 4 + length) break;
      const raw = this.#buffer.subarray(4, 4 + length);
      this.#buffer = this.#buffer.subarray(4 + length);
      const value = JSON.parse(raw.toString("utf8"));
      if (value === null || typeof value !== "object" || Array.isArray(value)) {
        throw new Error("native message must be an object");
      }
      values.push(value);
    }
    return values;
  }
}

function encodeNativeMessage(value) {
  const raw = Buffer.from(JSON.stringify(value), "utf8");
  if (raw.byteLength < 2 || raw.byteLength > MAX_NATIVE_MESSAGE_BYTES) {
    throw new Error("native message length is invalid");
  }
  const frame = Buffer.allocUnsafe(4 + raw.byteLength);
  frame.writeUInt32LE(raw.byteLength, 0);
  raw.copy(frame, 4);
  return frame;
}

export { encodeNativeMessage, MAX_NATIVE_MESSAGE_BYTES, NativeFrameDecoder };
