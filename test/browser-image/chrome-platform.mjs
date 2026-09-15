import { open } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Docker/Go architecture names deliberately map to upstream CfT names here.
// Never infer an architecture from a caller-supplied archive filename.
const PLATFORMS = Object.freeze({
  amd64: Object.freeze({ platform: "linux64", prefix: "chrome-linux64/",
    upstreamFilename: "chrome-linux64.zip", filename: "chrome-linux-amd64.tar", machine: 62 }),
  arm64: Object.freeze({ platform: "linux-arm64", prefix: "chrome-linux-arm64/",
    upstreamFilename: "chrome-linux-arm64.zip", filename: "chrome-linux-arm64.tar", machine: 183 }),
});

function chromePlatform(architecture = "amd64") {
  if (!Object.hasOwn(PLATFORMS, architecture)) {
    throw new Error("unsupported native Chrome architecture");
  }
  return PLATFORMS[architecture];
}

function chromeSourceURL(version, architecture = "amd64") {
  if (!/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){3}$/.test(version ?? "")) {
    throw new Error("Chrome for Testing version must contain four numeric parts");
  }
  const platform = chromePlatform(architecture);
  return `https://storage.googleapis.com/chrome-for-testing-public/${version}/${platform.platform}/${platform.upstreamFilename}`;
}

function verifyChromeELF(bytes, architecture) {
  const { machine } = chromePlatform(architecture);
  if (!Buffer.isBuffer(bytes) || bytes.length < 64 ||
      !bytes.subarray(0, 7).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1])) ||
      ![2, 3].includes(bytes.readUInt16LE(16)) || bytes.readUInt16LE(18) !== machine ||
      bytes.readUInt32LE(20) !== 1 || bytes.readUInt16LE(52) !== 64) {
    throw new Error("Chrome ELF executable does not match the locked architecture");
  }
}

// Recheck the extracted files before executing Chrome or publishing its asset lock.
// Reading only headers avoids loading the installed executable into memory twice.
async function verifyInstalledChrome(architecture, files) {
  chromePlatform(architecture);
  if (files.length !== 2 || files.some((file) => !path.isAbsolute(file))) {
    throw new Error("Chrome architecture verification requires absolute Chrome and sandbox paths");
  }
  for (const file of files) {
    const handle = await open(file, "r");
    try {
      const header = Buffer.alloc(64);
      const { bytesRead } = await handle.read(header, 0, header.length, 0);
      verifyChromeELF(header.subarray(0, bytesRead), architecture);
    } finally {
      await handle.close();
    }
  }
}

if (path.resolve(process.argv[1] ?? "") === path.resolve(fileURLToPath(import.meta.url))) {
  const [architecture, ...files] = process.argv.slice(2);
  await verifyInstalledChrome(architecture, files);
}

export { chromePlatform, chromeSourceURL, verifyChromeELF, verifyInstalledChrome };
