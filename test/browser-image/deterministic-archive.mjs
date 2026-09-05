import { inflateRawSync } from "node:zlib";

const ZIP_LOCAL_SIGNATURE = 0x04034b50;
const ZIP_CENTRAL_SIGNATURE = 0x02014b50;
const ZIP_END_SIGNATURE = 0x06054b50;
const MAX_ARCHIVE_ENTRIES = 4096;
const MAX_ARCHIVE_BYTES = 2 * 1024 * 1024 * 1024;

const crcTable = new Uint32Array(256);
for (let index = 0; index < crcTable.length; index += 1) {
  let value = index;
  for (let bit = 0; bit < 8; bit += 1) {
    value = (value & 1) === 0 ? value >>> 1 : 0xedb88320 ^ (value >>> 1);
  }
  crcTable[index] = value >>> 0;
}

function crc32(bytes) {
  let value = 0xffffffff;
  for (const byte of bytes) {
    value = crcTable[(value ^ byte) & 0xff] ^ (value >>> 8);
  }
  return (value ^ 0xffffffff) >>> 0;
}

function validArchivePath(value, directory = false) {
  if (
    typeof value !== "string" ||
    value.length < 1 ||
    Buffer.byteLength(value) > 512 ||
    value.includes("\0") ||
    value.includes("\\") ||
    value.startsWith("/") ||
    value !== value.normalize("NFC") ||
    directory !== value.endsWith("/")
  ) {
    return false;
  }
  const parts = value.split("/");
  if (directory) parts.pop();
  return (
    parts.length > 0 &&
    parts.every((part) => part !== "" && part !== "." && part !== "..")
  );
}

function deterministicZip(files) {
  if (!Array.isArray(files) || files.length < 1 || files.length > MAX_ARCHIVE_ENTRIES) {
    throw new Error("deterministic ZIP file count is invalid");
  }
  const entries = files.map((file) => {
    const isDirectory = file.type === "directory";
    if (
      !validArchivePath(file.path, isDirectory) ||
      (!isDirectory && !Buffer.isBuffer(file.data)) ||
      (isDirectory && file.data !== undefined) ||
      (file.executable !== undefined && typeof file.executable !== "boolean")
    ) {
      throw new Error("deterministic ZIP entry is invalid");
    }
    return {
      path: file.path,
      data: isDirectory ? Buffer.alloc(0) : file.data,
      isDirectory,
      executable: file.executable === true,
    };
  });
  entries.sort((left, right) =>
    Buffer.compare(Buffer.from(left.path), Buffer.from(right.path)),
  );
  if (new Set(entries.map((entry) => entry.path)).size !== entries.length) {
    throw new Error("deterministic ZIP contains a duplicate path");
  }
  const localParts = [];
  const centralParts = [];
  let localOffset = 0;
  let totalBytes = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.path, "utf8");
    const digest = crc32(entry.data);
    totalBytes += entry.data.length;
    if (entry.data.length > 0xffffffff || totalBytes > MAX_ARCHIVE_BYTES) {
      throw new Error("deterministic ZIP content is too large");
    }
    const local = Buffer.alloc(30);
    local.writeUInt32LE(ZIP_LOCAL_SIGNATURE, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0x0021, 12);
    local.writeUInt32LE(digest, 14);
    local.writeUInt32LE(entry.data.length, 18);
    local.writeUInt32LE(entry.data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    localParts.push(local, name, entry.data);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(ZIP_CENTRAL_SIGNATURE, 0);
    central.writeUInt16LE(0x0314, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0x0021, 14);
    central.writeUInt32LE(digest, 16);
    central.writeUInt32LE(entry.data.length, 20);
    central.writeUInt32LE(entry.data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt16LE(0, 30);
    central.writeUInt16LE(0, 32);
    central.writeUInt16LE(0, 34);
    central.writeUInt16LE(0, 36);
    const unixMode = entry.isDirectory
      ? 0o040555
      : entry.executable
        ? 0o100555
        : 0o100444;
    central.writeUInt32LE((unixMode << 16) >>> 0, 38);
    central.writeUInt32LE(localOffset, 42);
    centralParts.push(central, name);
    localOffset += local.length + name.length + entry.data.length;
  }
  const centralDirectory = Buffer.concat(centralParts);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(ZIP_END_SIGNATURE, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(centralDirectory.length, 12);
  end.writeUInt32LE(localOffset, 16);
  end.writeUInt16LE(0, 20);
  return Buffer.concat([...localParts, centralDirectory, end]);
}

function readZipEntries(bytes, expectedRoot) {
  if (
    !Buffer.isBuffer(bytes) ||
    bytes.length < 22 ||
    bytes.length > MAX_ARCHIVE_BYTES ||
    !(
      expectedRoot === "" ||
      /^[A-Za-z0-9._-]+\/$/.test(expectedRoot ?? "")
    )
  ) {
    throw new Error("ZIP archive input is invalid");
  }
  let endOffset = -1;
  const minimum = Math.max(0, bytes.length - 65_557);
  for (let offset = bytes.length - 22; offset >= minimum; offset -= 1) {
    if (bytes.readUInt32LE(offset) === ZIP_END_SIGNATURE) {
      endOffset = offset;
      break;
    }
  }
  if (endOffset < 0) throw new Error("ZIP end record is missing");
  const commentLength = bytes.readUInt16LE(endOffset + 20);
  if (
    endOffset + 22 + commentLength !== bytes.length ||
    bytes.readUInt16LE(endOffset + 4) !== 0 ||
    bytes.readUInt16LE(endOffset + 6) !== 0
  ) {
    throw new Error("ZIP end record is invalid");
  }
  const entryCount = bytes.readUInt16LE(endOffset + 10);
  const directorySize = bytes.readUInt32LE(endOffset + 12);
  const directoryOffset = bytes.readUInt32LE(endOffset + 16);
  if (
    entryCount < 1 ||
    entryCount > MAX_ARCHIVE_ENTRIES ||
    bytes.readUInt16LE(endOffset + 8) !== entryCount ||
    directoryOffset + directorySize !== endOffset
  ) {
    throw new Error("ZIP central directory is invalid");
  }
  const entries = [];
  const seen = new Set();
  let offset = directoryOffset;
  let totalBytes = 0;
  for (let index = 0; index < entryCount; index += 1) {
    if (
      offset + 46 > endOffset ||
      bytes.readUInt32LE(offset) !== ZIP_CENTRAL_SIGNATURE
    ) {
      throw new Error("ZIP central entry is invalid");
    }
    const flags = bytes.readUInt16LE(offset + 8);
    const method = bytes.readUInt16LE(offset + 10);
    const expectedCRC = bytes.readUInt32LE(offset + 16);
    const compressedSize = bytes.readUInt32LE(offset + 20);
    const uncompressedSize = bytes.readUInt32LE(offset + 24);
    const nameLength = bytes.readUInt16LE(offset + 28);
    const extraLength = bytes.readUInt16LE(offset + 30);
    const entryCommentLength = bytes.readUInt16LE(offset + 32);
    const externalAttributes = bytes.readUInt32LE(offset + 38);
    const localOffset = bytes.readUInt32LE(offset + 42);
    const next = offset + 46 + nameLength + extraLength + entryCommentLength;
    if (
      next > endOffset ||
      // Bits 1-2 are compression-option hints. Chrome for Testing currently
      // applies the "fast" hint to every file, including stored PNG entries;
      // the bits change neither framing nor trust semantics for method 0.
      (flags & ~(0x0002 | 0x0004 | 0x0008 | 0x0800)) !== 0 ||
      ![0, 8].includes(method) ||
      compressedSize === 0xffffffff ||
      uncompressedSize === 0xffffffff
    ) {
      throw new Error("ZIP central entry features are unsupported");
    }
    const nameBytes = bytes.subarray(offset + 46, offset + 46 + nameLength);
    const name = nameBytes.toString("utf8");
    if (!Buffer.from(name, "utf8").equals(nameBytes)) {
      throw new Error("ZIP entry name is not UTF-8");
    }
    const isDirectory = name.endsWith("/");
    if (
      !validArchivePath(name, isDirectory) ||
      (expectedRoot !== "" && !name.startsWith(expectedRoot)) ||
      (expectedRoot !== "" && name === expectedRoot && !isDirectory) ||
      seen.has(name)
    ) {
      throw new Error("ZIP entry path is invalid or duplicated");
    }
    seen.add(name);
    const unixMode = externalAttributes >>> 16;
    const fileType = unixMode & 0o170000;
    if (
      fileType === 0o120000 ||
      (fileType !== 0 &&
        fileType !== (isDirectory ? 0o040000 : 0o100000))
    ) {
      throw new Error("ZIP archive contains a non-regular entry");
    }
    if (
      localOffset + 30 > directoryOffset ||
      bytes.readUInt32LE(localOffset) !== ZIP_LOCAL_SIGNATURE
    ) {
      throw new Error("ZIP local entry is invalid");
    }
    const localFlags = bytes.readUInt16LE(localOffset + 6);
    const localMethod = bytes.readUInt16LE(localOffset + 8);
    const localNameLength = bytes.readUInt16LE(localOffset + 26);
    const localExtraLength = bytes.readUInt16LE(localOffset + 28);
    const localName = bytes.subarray(
      localOffset + 30,
      localOffset + 30 + localNameLength,
    );
    const dataOffset = localOffset + 30 + localNameLength + localExtraLength;
    if (
      localFlags !== flags ||
      localMethod !== method ||
      !localName.equals(nameBytes) ||
      dataOffset + compressedSize > directoryOffset
    ) {
      throw new Error("ZIP local entry does not match its directory record");
    }
    const compressed = bytes.subarray(dataOffset, dataOffset + compressedSize);
    const data = isDirectory
      ? Buffer.alloc(0)
      : method === 0
        ? Buffer.from(compressed)
        : inflateRawSync(compressed, { maxOutputLength: uncompressedSize });
    if (
      data.length !== uncompressedSize ||
      crc32(data) !== expectedCRC ||
      (isDirectory && (compressedSize !== 0 || uncompressedSize !== 0))
    ) {
      throw new Error("ZIP entry content is invalid");
    }
    totalBytes += data.length;
    if (totalBytes > MAX_ARCHIVE_BYTES) {
      throw new Error("ZIP archive content is too large");
    }
    entries.push({
      path: name,
      data,
      isDirectory,
      executable: !isDirectory && (unixMode & 0o111) !== 0,
    });
    offset = next;
  }
  if (offset !== endOffset) {
    throw new Error("ZIP central directory or root is incomplete");
  }
  return entries;
}

function writeTarString(header, offset, length, value) {
  const bytes = Buffer.from(value, "utf8");
  if (bytes.length > length) throw new Error("tar field is too long");
  bytes.copy(header, offset);
}

function writeTarOctal(header, offset, length, value) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new Error("tar numeric field is invalid");
  }
  const octal = value.toString(8).padStart(length - 1, "0");
  if (octal.length > length - 1) throw new Error("tar numeric field is too large");
  writeTarString(header, offset, length, `${octal}\0`);
}

function tarNameFields(value) {
  const raw = Buffer.from(value, "utf8");
  if (raw.length <= 100) return { name: value, prefix: "" };
  for (let index = value.lastIndexOf("/"); index > 0; index = value.lastIndexOf("/", index - 1)) {
    const prefix = value.slice(0, index);
    const name = value.slice(index + 1);
    if (Buffer.byteLength(prefix) <= 155 && Buffer.byteLength(name) <= 100) {
      return { name, prefix };
    }
  }
  throw new Error("tar path is too long");
}

function deterministicTar(inputEntries) {
  if (
    !Array.isArray(inputEntries) ||
    inputEntries.length < 1 ||
    inputEntries.length > MAX_ARCHIVE_ENTRIES
  ) {
    throw new Error("deterministic tar entry count is invalid");
  }
  const entries = inputEntries.map((entry) => {
    const isDirectory = entry.type === "directory";
    if (
      !validArchivePath(entry.path, isDirectory) ||
      ![0o444, 0o555].includes(entry.mode) ||
      (!isDirectory && !Buffer.isBuffer(entry.data)) ||
      (isDirectory && entry.data !== undefined)
    ) {
      throw new Error("deterministic tar entry is invalid");
    }
    return {
      path: entry.path,
      type: entry.type,
      mode: entry.mode,
      data: isDirectory ? Buffer.alloc(0) : entry.data,
    };
  });
  entries.sort((left, right) =>
    Buffer.compare(Buffer.from(left.path), Buffer.from(right.path)),
  );
  if (new Set(entries.map((entry) => entry.path)).size !== entries.length) {
    throw new Error("deterministic tar contains a duplicate path");
  }
  const parts = [];
  let totalBytes = 0;
  for (const entry of entries) {
    totalBytes += entry.data.length;
    if (totalBytes > MAX_ARCHIVE_BYTES) {
      throw new Error("deterministic tar content is too large");
    }
    const header = Buffer.alloc(512);
    const names = tarNameFields(entry.path);
    writeTarString(header, 0, 100, names.name);
    writeTarOctal(header, 100, 8, entry.mode);
    writeTarOctal(header, 108, 8, 0);
    writeTarOctal(header, 116, 8, 0);
    writeTarOctal(header, 124, 12, entry.data.length);
    writeTarOctal(header, 136, 12, 0);
    header.fill(0x20, 148, 156);
    header[156] = entry.type === "directory" ? 0x35 : 0x30;
    writeTarString(header, 257, 6, "ustar\0");
    writeTarString(header, 263, 2, "00");
    writeTarString(header, 345, 155, names.prefix);
    const checksum = [...header].reduce((sum, byte) => sum + byte, 0);
    const checksumText = checksum.toString(8).padStart(6, "0");
    writeTarString(header, 148, 8, `${checksumText}\0 `);
    parts.push(header);
    if (entry.data.length > 0) {
      parts.push(entry.data);
      const remainder = entry.data.length % 512;
      if (remainder !== 0) parts.push(Buffer.alloc(512 - remainder));
    }
  }
  parts.push(Buffer.alloc(1024));
  return Buffer.concat(parts);
}

export {
  MAX_ARCHIVE_BYTES,
  MAX_ARCHIVE_ENTRIES,
  crc32,
  deterministicTar,
  deterministicZip,
  readZipEntries,
};
