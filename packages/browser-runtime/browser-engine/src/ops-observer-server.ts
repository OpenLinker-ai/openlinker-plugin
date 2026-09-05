import { lstat, chmod, unlink } from "node:fs/promises";
import {
  createConnection,
  createServer,
  type Server,
  type Socket,
} from "node:net";
import path from "node:path";

import { BrowserEngine } from "./engine.js";
import {
  ENGINE_OPS_OBSERVER_CONTRACT_ID,
  opsObserverFailure,
  parseOpsObserverRequest,
} from "./protocol.js";

const MAX_INPUT_BYTES = 256 * 1024;

export class OpsObserverServer {
  private readonly connections = new Set<Socket>();
  private server: Server | undefined;
  private socketIdentity: { dev: number; ino: number } | undefined;

  private constructor(
    private readonly engine: BrowserEngine,
    private readonly socketPath: string,
  ) {}

  static async start(
    engine: BrowserEngine,
    environment: NodeJS.ProcessEnv,
  ): Promise<OpsObserverServer | undefined> {
    const enabled = environment.OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED;
    if (enabled === undefined || enabled === "false") return undefined;
    if (enabled !== "true") {
      throw new Error(
        "OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED must be true or false",
      );
    }
    const socketPath = requireAbsoluteSocketPath(
      environment.OPENLINKER_BROWSER_ENGINE_OPS_SOCKET,
    );
    const observer = new OpsObserverServer(engine, socketPath);
    await observer.listen();
    return observer;
  }

  async close(): Promise<void> {
    const server = this.server;
    this.server = undefined;
    for (const connection of this.connections) connection.destroy();
    this.connections.clear();
    if (server !== undefined) {
      await new Promise<void>((resolve, reject) => {
        server.close((error) => (error === undefined ? resolve() : reject(error)));
      }).catch(() => undefined);
    }
    await this.removeOwnedSocket();
  }

  private async listen(): Promise<void> {
    await removeStaleSocket(this.socketPath);
    // The Go Runtime sends exactly one newline-framed request and then closes
    // only its write side. Keep the response side open until the asynchronous
    // observation has been encoded instead of letting Node's default
    // allowHalfOpen=false turn that FIN into an immediate empty EOF.
    const server = createServer(
      { allowHalfOpen: true },
      (connection) => this.handle(connection),
    );
    this.server = server;
    await new Promise<void>((resolve, reject) => {
      const onError = (error: Error) => {
        server.off("listening", onListening);
        reject(error);
      };
      const onListening = () => {
        server.off("error", onError);
        resolve();
      };
      server.once("error", onError);
      server.once("listening", onListening);
      server.listen(this.socketPath);
    });
    await chmod(this.socketPath, 0o600);
    const status = await lstat(this.socketPath);
    if (!status.isSocket() || status.uid !== process.getuid?.()) {
      await this.close();
      throw new Error("Browser Engine Ops Observer socket identity is invalid");
    }
    this.socketIdentity = { dev: status.dev, ino: status.ino };
  }

  private handle(connection: Socket): void {
    this.connections.add(connection);
    connection.setTimeout(750, () => connection.destroy());
    let buffered = Buffer.alloc(0);
    let handled = false;
    connection.on("close", () => this.connections.delete(connection));
    connection.on("error", () => undefined);
    connection.on("data", (chunk: Buffer) => {
      if (handled) return;
      buffered = Buffer.concat([buffered, chunk]);
      if (buffered.byteLength > MAX_INPUT_BYTES + 1) {
        handled = true;
        connection.end(
          `${JSON.stringify(
            opsObserverFailure(
              "0",
              "OPS_VIEWER_INTERNAL",
              "Browser Engine Ops Observer request is too large",
            ),
          )}\n`,
        );
        return;
      }
      const newline = buffered.indexOf(0x0a);
      if (newline < 0) return;
      handled = true;
      const line = buffered.subarray(0, newline).toString("utf8");
      let actionID = "0";
      try {
        const parsed: unknown = JSON.parse(line);
        if (
          parsed !== null &&
          typeof parsed === "object" &&
          !Array.isArray(parsed) &&
          "action_id" in parsed &&
          typeof parsed.action_id === "string" &&
          /^[1-9][0-9]*$/.test(parsed.action_id)
        ) {
          actionID = parsed.action_id;
        }
      } catch {
        // Closed parsing below returns a generic response without echoing input.
      }
      void (async () => {
        try {
          const request = parseOpsObserverRequest(line);
          const response = await this.engine.executeOpsObserver(request);
          connection.end(`${JSON.stringify(response)}\n`);
        } catch {
          connection.end(
            `${JSON.stringify(
              opsObserverFailure(
                actionID,
                "OPS_VIEWER_INTERNAL",
                "Browser Engine Ops Observer request is invalid",
              ),
            )}\n`,
          );
        }
      })();
    });
  }

  private async removeOwnedSocket(): Promise<void> {
    if (this.socketIdentity === undefined) return;
    try {
      const status = await lstat(this.socketPath);
      if (
        status.isSocket() &&
        status.dev === this.socketIdentity.dev &&
        status.ino === this.socketIdentity.ino
      ) {
        await unlink(this.socketPath);
      }
    } catch (error) {
      if (!isCode(error, "ENOENT")) throw error;
    } finally {
      this.socketIdentity = undefined;
    }
  }
}

async function removeStaleSocket(socketPath: string): Promise<void> {
  let status;
  try {
    status = await lstat(socketPath);
  } catch (error) {
    if (isCode(error, "ENOENT")) return;
    throw error;
  }
  if (!status.isSocket() || status.uid !== process.getuid?.()) {
    throw new Error("refusing to replace an unowned Browser Engine Ops socket");
  }
  if (await socketAcceptsConnections(socketPath)) {
    throw new Error("Browser Engine Ops Observer socket is already active");
  }
  await unlink(socketPath);
}

function socketAcceptsConnections(socketPath: string): Promise<boolean> {
  return new Promise((resolve) => {
    const connection = createConnection(socketPath);
    const finish = (active: boolean) => {
      connection.destroy();
      resolve(active);
    };
    connection.setTimeout(100, () => finish(true));
    connection.once("connect", () => finish(true));
    connection.once("error", () => finish(false));
  });
}

function requireAbsoluteSocketPath(value: string | undefined): string {
  if (
    value === undefined ||
    !path.isAbsolute(value) ||
    path.normalize(value) !== value ||
    value.includes("\u0000")
  ) {
    throw new Error(
      "OPENLINKER_BROWSER_ENGINE_OPS_SOCKET must be an absolute normalized path",
    );
  }
  return value;
}

function isCode(error: unknown, code: string): boolean {
  return (
    error !== null &&
    typeof error === "object" &&
    "code" in error &&
    error.code === code
  );
}

export { ENGINE_OPS_OBSERVER_CONTRACT_ID };
