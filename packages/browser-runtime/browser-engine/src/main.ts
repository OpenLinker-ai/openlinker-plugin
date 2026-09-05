import { once } from "node:events";

import { BrowserEngine } from "./engine.js";
import { OpsObserverServer } from "./ops-observer-server.js";
import {
  ENGINE_VIEWER_CONTRACT_ID,
  failure,
  parseRequest,
  parseViewerRequest,
  type EngineResponse,
  type EngineViewerResponse,
  viewerFailure,
} from "./protocol.js";

const MAX_INPUT_BYTES = 256 * 1024;

async function main(): Promise<void> {
  const engine = await BrowserEngine.create(process.env);
  const opsObserver = await OpsObserverServer.start(engine, process.env);
  const shutdown = async (): Promise<void> => {
    await opsObserver?.close().catch(() => undefined);
    await engine.close().catch(() => undefined);
    process.exit(0);
  };
  process.once("SIGINT", () => {
    void shutdown();
  });
  process.once("SIGTERM", () => {
    void shutdown();
  });

  for await (const line of boundedLines(process.stdin, MAX_INPUT_BYTES)) {
    let response: EngineResponse | EngineViewerResponse;
    try {
      if (extractContractID(line) === ENGINE_VIEWER_CONTRACT_ID) {
        response = await engine.executeViewer(parseViewerRequest(line));
      } else {
        response = await engine.execute(parseRequest(line));
      }
    } catch (error) {
      response =
        extractContractID(line) === ENGINE_VIEWER_CONTRACT_ID
          ? viewerFailure(
              extractActionID(line),
              "BROWSER_OUTPUT_INVALID",
              "browser engine viewer request is invalid",
              false,
            )
          : failure(
              extractActionID(line),
              "BROWSER_OUTPUT_INVALID",
              "browser engine request is invalid",
              false,
            );
    }
    await writeResponse(response);
  }
  await opsObserver?.close();
  await engine.close();
}

async function* boundedLines(
  input: NodeJS.ReadableStream,
  maximumBytes: number,
): AsyncGenerator<string> {
  let buffered = Buffer.alloc(0);
  for await (const rawChunk of input) {
    const chunk = Buffer.isBuffer(rawChunk)
      ? rawChunk
      : Buffer.from(rawChunk as string);
    buffered = Buffer.concat([buffered, chunk]);
    for (;;) {
      const newline = buffered.indexOf(0x0a);
      if (newline < 0) {
        if (buffered.byteLength > maximumBytes) {
          throw new Error("browser engine input exceeds the line limit");
        }
        break;
      }
      if (newline > maximumBytes) {
        throw new Error("browser engine input exceeds the line limit");
      }
      const line = buffered.subarray(0, newline).toString("utf8");
      buffered = buffered.subarray(newline + 1);
      yield line;
    }
  }
  if (buffered.byteLength !== 0) {
    throw new Error("browser engine input ended without a newline");
  }
}

function extractActionID(line: string): string {
  try {
    const value: unknown = JSON.parse(line);
    if (
      value !== null &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      "action_id" in value &&
      typeof value.action_id === "string" &&
      /^[1-9][0-9]*$/.test(value.action_id) &&
      value.action_id.length <= 32
    ) {
      return value.action_id;
    }
  } catch {
    // The Go supervisor rejects action_id 0 and restarts the engine.
  }
  return "0";
}

function extractContractID(line: string): string {
  try {
    const value: unknown = JSON.parse(line);
    if (
      value !== null &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      "contract_id" in value &&
      typeof value.contract_id === "string"
    ) {
      return value.contract_id;
    }
  } catch {
    // Invalid JSON is handled by the closed engine parser.
  }
  return "";
}

async function writeResponse(
  response: EngineResponse | EngineViewerResponse,
): Promise<void> {
  const raw = `${JSON.stringify(response)}\n`;
  if (!process.stdout.write(raw)) {
    await once(process.stdout, "drain");
  }
}

void main().catch((error: unknown) => {
  const detail =
    error instanceof Error && error.message.trim() !== ""
      ? error.message.trim()
      : "unknown startup failure";
  process.stderr.write(
    `browser engine startup or stream loop failed: ${detail}\n`,
  );
  process.exit(1);
});
