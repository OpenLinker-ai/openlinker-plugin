import {
  constants,
  createHash,
  createPrivateKey,
  createPublicKey,
  sign,
  verify,
} from "node:crypto";

const CRX3_MAGIC = Buffer.from("Cr24", "ascii");
const CRX3_VERSION = 3;
const CRX3_SIGNATURE_CONTEXT = Buffer.from("CRX3 SignedData\0", "utf8");
const MAX_CRX_HEADER_BYTES = 1024 * 1024;

function encodeVarint(input) {
  if (!Number.isSafeInteger(input) || input < 0) {
    throw new Error("protobuf varint is invalid");
  }
  const result = [];
  let value = input;
  do {
    let byte = value % 128;
    value = Math.floor(value / 128);
    if (value > 0) byte |= 0x80;
    result.push(byte);
  } while (value > 0);
  return Buffer.from(result);
}

function encodeBytesField(fieldNumber, value) {
  if (
    !Number.isSafeInteger(fieldNumber) ||
    fieldNumber < 1 ||
    fieldNumber > 536_870_911 ||
    !Buffer.isBuffer(value)
  ) {
    throw new Error("protobuf bytes field is invalid");
  }
  return Buffer.concat([
    encodeVarint(fieldNumber * 8 + 2),
    encodeVarint(value.length),
    value,
  ]);
}

function decodeVarint(bytes, start) {
  let value = 0;
  let multiplier = 1;
  let offset = start;
  for (let index = 0; index < 8; index += 1) {
    if (offset >= bytes.length) throw new Error("protobuf varint is truncated");
    const byte = bytes[offset];
    offset += 1;
    value += (byte & 0x7f) * multiplier;
    if (!Number.isSafeInteger(value)) throw new Error("protobuf varint is too large");
    if ((byte & 0x80) === 0) return { value, offset };
    multiplier *= 128;
  }
  throw new Error("protobuf varint is too long");
}

function parseExactBytesMessage(bytes, expectedFields) {
  if (!Buffer.isBuffer(bytes) || !Array.isArray(expectedFields)) {
    throw new Error("protobuf message input is invalid");
  }
  const expected = new Set(expectedFields);
  const fields = new Map();
  let offset = 0;
  while (offset < bytes.length) {
    const tag = decodeVarint(bytes, offset);
    offset = tag.offset;
    const fieldNumber = Math.floor(tag.value / 8);
    const wireType = tag.value % 8;
    if (
      wireType !== 2 ||
      !expected.has(fieldNumber) ||
      fields.has(fieldNumber)
    ) {
      throw new Error("CRX3 protobuf fields are invalid");
    }
    const length = decodeVarint(bytes, offset);
    offset = length.offset;
    const end = offset + length.value;
    if (end > bytes.length) throw new Error("CRX3 protobuf field is truncated");
    fields.set(fieldNumber, bytes.subarray(offset, end));
    offset = end;
  }
  if (
    fields.size !== expected.size ||
    expectedFields.some((field) => !fields.has(field))
  ) {
    throw new Error("CRX3 protobuf fields are incomplete");
  }
  return fields;
}

function extensionIDFromPublicKey(publicKeyDER) {
  if (
    !Buffer.isBuffer(publicKeyDER) ||
    publicKeyDER.length < 64 ||
    publicKeyDER.length > 16 * 1024
  ) {
    throw new Error("CRX3 public key is invalid");
  }
  const digest = createHash("sha256").update(publicKeyDER).digest().subarray(0, 16);
  let result = "";
  for (const byte of digest) {
    result += String.fromCharCode(97 + (byte >> 4), 97 + (byte & 0x0f));
  }
  return result;
}

function signedCRX3Bytes(signedHeaderData, zip) {
  const length = Buffer.alloc(4);
  length.writeUInt32LE(signedHeaderData.length, 0);
  return Buffer.concat([CRX3_SIGNATURE_CONTEXT, length, signedHeaderData, zip]);
}

function createCRX3(zip, privateKeyPEM) {
  if (
    !Buffer.isBuffer(zip) ||
    zip.length < 22 ||
    zip.readUInt32LE(0) !== 0x04034b50
  ) {
    throw new Error("CRX3 ZIP payload is invalid");
  }
  const privateKey = createPrivateKey(privateKeyPEM);
  if (privateKey.asymmetricKeyType !== "rsa") {
    throw new Error("CRX3 signing key must be RSA");
  }
  const publicKey = createPublicKey(privateKey);
  const publicKeyDER = publicKey.export({ type: "spki", format: "der" });
  const crxID = createHash("sha256").update(publicKeyDER).digest().subarray(0, 16);
  const signedHeaderData = encodeBytesField(1, crxID);
  const signature = sign("sha256", signedCRX3Bytes(signedHeaderData, zip), {
    key: privateKey,
    padding: constants.RSA_PKCS1_PADDING,
  });
  const proof = Buffer.concat([
    encodeBytesField(1, publicKeyDER),
    encodeBytesField(2, signature),
  ]);
  const header = Buffer.concat([
    encodeBytesField(2, proof),
    encodeBytesField(10_000, signedHeaderData),
  ]);
  if (header.length > MAX_CRX_HEADER_BYTES) {
    throw new Error("CRX3 header is too large");
  }
  const prefix = Buffer.alloc(12);
  CRX3_MAGIC.copy(prefix, 0);
  prefix.writeUInt32LE(CRX3_VERSION, 4);
  prefix.writeUInt32LE(header.length, 8);
  return Buffer.concat([prefix, header, zip]);
}

function verifyCRX3(crx) {
  if (
    !Buffer.isBuffer(crx) ||
    crx.length < 34 ||
    !crx.subarray(0, 4).equals(CRX3_MAGIC) ||
    crx.readUInt32LE(4) !== CRX3_VERSION
  ) {
    throw new Error("CRX3 header is invalid");
  }
  const headerLength = crx.readUInt32LE(8);
  if (
    headerLength < 1 ||
    headerLength > MAX_CRX_HEADER_BYTES ||
    12 + headerLength + 22 > crx.length
  ) {
    throw new Error("CRX3 header length is invalid");
  }
  const header = parseExactBytesMessage(
    crx.subarray(12, 12 + headerLength),
    [2, 10_000],
  );
  const proof = parseExactBytesMessage(header.get(2), [1, 2]);
  const signedHeaderData = header.get(10_000);
  const signed = parseExactBytesMessage(signedHeaderData, [1]);
  const publicKeyDER = proof.get(1);
  const signature = proof.get(2);
  const crxID = signed.get(1);
  const zip = crx.subarray(12 + headerLength);
  if (
    crxID.length !== 16 ||
    !createHash("sha256").update(publicKeyDER).digest().subarray(0, 16).equals(crxID) ||
    zip.length < 22 ||
    zip.readUInt32LE(0) !== 0x04034b50
  ) {
    throw new Error("CRX3 signed identity is invalid");
  }
  const publicKey = createPublicKey({ key: publicKeyDER, type: "spki", format: "der" });
  if (
    publicKey.asymmetricKeyType !== "rsa" ||
    !verify("sha256", signedCRX3Bytes(signedHeaderData, zip), {
      key: publicKey,
      padding: constants.RSA_PKCS1_PADDING,
    }, signature)
  ) {
    throw new Error("CRX3 signature is invalid");
  }
  return {
    extensionID: extensionIDFromPublicKey(publicKeyDER),
    publicKeyDER: Buffer.from(publicKeyDER),
    signature: Buffer.from(signature),
    zip: Buffer.from(zip),
  };
}

export {
  CRX3_SIGNATURE_CONTEXT,
  createCRX3,
  extensionIDFromPublicKey,
  verifyCRX3,
};
