import { createHash, randomBytes } from "node:crypto";
import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  rm,
} from "node:fs/promises";
import path from "node:path";

import { BROWSER_STATE_RETENTION_MS } from "./browser-contract.generated.js";
import { isPublicHTTPURL, type Identity } from "./protocol.js";

export const PAGE_CONTINUATION_CONTRACT_ID =
  "openlinker.browser.page-continuation.v1";
export const PAGE_CONTINUATION_DIRECTORY = ".openlinker";
export const PAGE_CONTINUATION_FILE = "page-continuations.v1.json";

const MAX_ENTRIES = 32;
const MAX_FILE_BYTES = 512 * 1024;
const MAX_URL_BYTES = 8192;
const STABLE_SESSION_KEY_LABEL =
  "openlinker/browser/page-continuation/stable-session/v1\u0000";

interface PageContinuationEntry {
  session_key: string;
  url: string;
  updated_at: string;
}

interface PageContinuationIndex {
  contract_id: typeof PAGE_CONTINUATION_CONTRACT_ID;
  entries: PageContinuationEntry[];
}

export class PageContinuationCorruptError extends Error {
  constructor() {
    super("Browser page continuation state is invalid");
  }
}

export class PageContinuationStore {
  private readonly directory: string;
  private readonly file: string;
  private entries: PageContinuationEntry[] | undefined;

  constructor(profileDirectory: string) {
    this.directory = path.join(profileDirectory, PAGE_CONTINUATION_DIRECTORY);
    this.file = path.join(this.directory, PAGE_CONTINUATION_FILE);
  }

  async lookup(identity: Identity, now = Date.now()): Promise<string | undefined> {
    await this.load(now);
    const key = pageContinuationSessionKey(identity);
    return this.entries?.find((entry) => entry.session_key === key)?.url;
  }

  async record(
    identity: Identity,
    rawURL: string,
    now = Date.now(),
    force = false,
  ): Promise<void> {
    await this.load(now);
    const key = pageContinuationSessionKey(identity);
    const current = (this.entries ?? []).find(
      (entry) => entry.session_key === key,
    );
    const url = normalizeContinuationURL(rawURL);
    if (!force && current?.url === url) {
      return;
    }
    if (!force && current === undefined && url === undefined) {
      return;
    }
    const retained = (this.entries ?? []).filter(
      (entry) => entry.session_key !== key,
    );
    if (url !== undefined) {
      retained.push({
        session_key: key,
        url,
        updated_at: new Date(now).toISOString(),
      });
    }
    retained.sort(
      (left, right) =>
        Date.parse(right.updated_at) - Date.parse(left.updated_at),
    );
    this.entries = retained.slice(0, MAX_ENTRIES);
    await this.persist();
  }

  private async load(now: number): Promise<void> {
    if (this.entries !== undefined) {
      return;
    }
    let info;
    try {
      info = await lstat(this.file);
    } catch (error) {
      if (isMissing(error)) {
        this.entries = [];
        return;
      }
      throw error;
    }
    if (
      !info.isFile() ||
      info.isSymbolicLink() ||
      (info.mode & 0o077) !== 0 ||
      info.size <= 0 ||
      info.size > MAX_FILE_BYTES
    ) {
      throw new PageContinuationCorruptError();
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(await readFile(this.file, "utf8")) as unknown;
    } catch {
      throw new PageContinuationCorruptError();
    }
    const entries = parseIndex(parsed);
    const oldestAllowed = now - BROWSER_STATE_RETENTION_MS;
    this.entries = entries.filter(
      (entry) => Date.parse(entry.updated_at) >= oldestAllowed,
    );
    if (this.entries.length !== entries.length) {
      await this.persist();
    }
  }

  private async persist(): Promise<void> {
    const entries = this.entries ?? [];
    if (entries.length === 0) {
      await rm(this.file, { force: true });
      return;
    }
    await ensurePrivateDirectory(this.directory);
    const value: PageContinuationIndex = {
      contract_id: PAGE_CONTINUATION_CONTRACT_ID,
      entries,
    };
    const raw = `${JSON.stringify(value)}\n`;
    if (Buffer.byteLength(raw, "utf8") > MAX_FILE_BYTES) {
      throw new PageContinuationCorruptError();
    }
    const temporary = path.join(
      this.directory,
      `.${PAGE_CONTINUATION_FILE}.${randomBytes(12).toString("hex")}.tmp`,
    );
    const handle = await open(temporary, "wx", 0o600);
    try {
      await handle.writeFile(raw, "utf8");
      await handle.sync();
      await handle.close();
      await rename(temporary, this.file);
      await chmod(this.file, 0o600);
      const directoryHandle = await open(this.directory, "r");
      try {
        await directoryHandle.sync();
      } finally {
        await directoryHandle.close();
      }
    } catch (error) {
      await handle.close().catch(() => undefined);
      await rm(temporary, { force: true });
      throw error;
    }
  }
}

export function pageContinuationSessionKey(identity: Identity): string {
  // BrowserSessionID is the stable, Provider-owned Conversation mapping.
  // Session/control epochs fence live actions in the Runtime lease; including
  // them here would make a correctly fenced Worker reattachment lose its
  // durable last-page checkpoint.
  return createHash("sha256")
    .update(STABLE_SESSION_KEY_LABEL)
    .update(identity.browser_session_id)
    .digest("hex");
}

export function pageContinuationPath(profileDirectory: string): string {
  return path.join(
    profileDirectory,
    PAGE_CONTINUATION_DIRECTORY,
    PAGE_CONTINUATION_FILE,
  );
}

function parseIndex(value: unknown): PageContinuationEntry[] {
  const index = requireRecord(value);
  requireExactFields(index, new Set(["contract_id", "entries"]));
  if (
    index.contract_id !== PAGE_CONTINUATION_CONTRACT_ID ||
    !Array.isArray(index.entries) ||
    index.entries.length > MAX_ENTRIES
  ) {
    throw new PageContinuationCorruptError();
  }
  const seen = new Set<string>();
  return index.entries.map((rawEntry) => {
    const entry = requireRecord(rawEntry);
    requireExactFields(entry, new Set(["session_key", "url", "updated_at"]));
    if (
      typeof entry.session_key !== "string" ||
      !/^[a-f0-9]{64}$/.test(entry.session_key) ||
      seen.has(entry.session_key) ||
      typeof entry.url !== "string" ||
      normalizeContinuationURL(entry.url) !== entry.url ||
      typeof entry.updated_at !== "string" ||
      !isCanonicalTimestamp(entry.updated_at)
    ) {
      throw new PageContinuationCorruptError();
    }
    seen.add(entry.session_key);
    return {
      session_key: entry.session_key,
      url: entry.url,
      updated_at: entry.updated_at,
    };
  });
}

export function normalizeContinuationURL(raw: string): string | undefined {
  if (
    Buffer.byteLength(raw, "utf8") > MAX_URL_BYTES ||
    !isPublicHTTPURL(raw)
  ) {
    return undefined;
  }
  const url = new URL(raw);
  url.hash = "";
  const normalized = url.toString();
  if (Buffer.byteLength(normalized, "utf8") > MAX_URL_BYTES) {
    return undefined;
  }
  return normalized;
}

function isCanonicalTimestamp(value: string): boolean {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) && new Date(parsed).toISOString() === value;
}

function requireRecord(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new PageContinuationCorruptError();
  }
  return value as Record<string, unknown>;
}

function requireExactFields(
  value: Record<string, unknown>,
  fields: ReadonlySet<string>,
): void {
  const keys = Object.keys(value);
  if (keys.length !== fields.size || keys.some((key) => !fields.has(key))) {
    throw new PageContinuationCorruptError();
  }
}

async function ensurePrivateDirectory(directory: string): Promise<void> {
  try {
    await mkdir(directory, { mode: 0o700 });
  } catch (error) {
    if (!isAlreadyExists(error)) {
      throw error;
    }
  }
  const info = await lstat(directory);
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new PageContinuationCorruptError();
  }
  if ((info.mode & 0o077) !== 0) {
    await chmod(directory, 0o700);
  }
}

function isMissing(error: unknown): boolean {
  return isNodeError(error) && error.code === "ENOENT";
}

function isAlreadyExists(error: unknown): boolean {
  return isNodeError(error) && error.code === "EEXIST";
}

function isNodeError(error: unknown): error is NodeJS.ErrnoException {
  return error instanceof Error && "code" in error;
}
