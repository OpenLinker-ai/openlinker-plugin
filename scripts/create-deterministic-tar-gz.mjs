#!/usr/bin/env node

import { readdir, readFile, writeFile } from "node:fs/promises";
import { basename, join, relative, resolve } from "node:path";

function argumentValue(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 && index + 1 < process.argv.length
    ? process.argv[index + 1]
    : "";
}

async function entriesBelow(root, prefix, current = root) {
  const entries = await readdir(current, { withFileTypes: true });
  const result = [];
  for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
    const path = join(current, entry.name);
    const relativeName = relative(root, path).replaceAll("\\", "/");
    const name = prefix
      ? `${prefix.replace(/\/+$/u, "")}/${relativeName}`
      : relativeName;
    if (entry.isDirectory()) {
      result.push({ name: `${name}/`, type: "directory", content: Buffer.alloc(0) });
      result.push(...await entriesBelow(root, prefix, path));
    } else if (entry.isFile()) {
      result.push({ name, type: "file", content: await readFile(path) });
    } else {
      throw new Error(`unsupported archive entry: ${path}`);
    }
  }
  return result;
}

function writeString(buffer, offset, length, value) {
  const encoded = Buffer.from(value, "utf8");
  if (encoded.length > length) {
    throw new Error(`tar field exceeds ${length} bytes: ${value}`);
  }
  encoded.copy(buffer, offset);
}

function writeOctal(buffer, offset, length, value) {
  const encoded = value.toString(8).padStart(length - 1, "0");
  writeString(buffer, offset, length, `${encoded}\0`);
}

function tarHeader(entry) {
  const header = Buffer.alloc(512);
  writeString(header, 0, 100, entry.name);
  writeOctal(header, 100, 8, entry.type === "directory" ? 0o555 : 0o444);
  writeOctal(header, 108, 8, 0);
  writeOctal(header, 116, 8, 0);
  writeOctal(header, 124, 12, entry.content.length);
  writeOctal(header, 136, 12, 0);
  header.fill(0x20, 148, 156);
  header[156] = entry.type === "directory" ? 0x35 : 0x30;
  writeString(header, 257, 6, "ustar\0");
  writeString(header, 263, 2, "00");
  writeString(header, 265, 32, "root");
  writeString(header, 297, 32, "root");
  let checksum = 0;
  for (const byte of header) checksum += byte;
  const encodedChecksum = checksum.toString(8).padStart(6, "0");
  writeString(header, 148, 8, `${encodedChecksum}\0 `);
  return header;
}

function tarArchive(entries) {
  const chunks = [];
  for (const entry of entries) {
    chunks.push(tarHeader(entry), entry.content);
    const remainder = entry.content.length % 512;
    if (remainder !== 0) chunks.push(Buffer.alloc(512 - remainder));
  }
  chunks.push(Buffer.alloc(1024));
  return Buffer.concat(chunks);
}

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0);
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function deterministicGzip(buffer) {
  const chunks = [Buffer.from([
    0x1f, 0x8b, 0x08, 0x00,
    0x00, 0x00, 0x00, 0x00,
    0x00, 0xff,
  ])];
  for (let offset = 0; offset < buffer.length;) {
    const length = Math.min(0xffff, buffer.length - offset);
    const final = offset + length === buffer.length;
    const block = Buffer.alloc(5 + length);
    block[0] = final ? 0x01 : 0x00;
    block.writeUInt16LE(length, 1);
    block.writeUInt16LE((~length) & 0xffff, 3);
    buffer.copy(block, 5, offset, offset + length);
    chunks.push(block);
    offset += length;
  }
  const trailer = Buffer.alloc(8);
  trailer.writeUInt32LE(crc32(buffer), 0);
  trailer.writeUInt32LE(buffer.length >>> 0, 4);
  chunks.push(trailer);
  return Buffer.concat(chunks);
}

export async function createDeterministicTarGz(root, output, prefix = "") {
  const entries = await entriesBelow(resolve(root), prefix);
  if (entries.length === 0) {
    throw new Error("refusing to create an empty Agent Runtime archive");
  }
  await writeFile(resolve(output), deterministicGzip(tarArchive(entries)));
}

if (process.argv[1] && basename(process.argv[1]) === basename(import.meta.filename)) {
  const root = argumentValue("--root");
  const output = argumentValue("--out");
  const prefix = argumentValue("--prefix");
  if (!root || !output) {
    throw new Error(
      "usage: create-deterministic-tar-gz.mjs --root <dir> --out <archive> [--prefix <dir>]",
    );
  }
  await createDeterministicTarGz(root, output, prefix);
}
