import { createHash, randomBytes } from "node:crypto";
import {
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  rm,
} from "node:fs/promises";
import path from "node:path";

import type { Identity } from "./protocol.js";

const CONTRACT_ID = "openlinker.browser.origin-budgets.v1";
const STATE_RELATIVE_PATH = ".openlinker/origin-budgets.v1.json";
const MAX_STATE_BYTES = 256 * 1024;
const MAX_ORIGINS = 256;
const RETENTION_MS = 10 * 60 * 1000;

export interface OriginBudgetOptions {
  profileDirectory: string;
  profileGeneration: number;
  maxActionsPerMinute: number;
  maxNavigationsPerMinute: number;
  now?: () => number;
}

interface BudgetEntry {
  key: string;
  action_ms: number[];
  navigation_ms: number[];
  retry_after_ms?: number;
  updated_at_ms: number;
}

interface BudgetDocument {
  contract_id: typeof CONTRACT_ID;
  entries: BudgetEntry[];
}

export class OriginBudgetStore {
  private readonly path: string;
  private readonly profileGeneration: number;
  private readonly maxActions: number;
  private readonly maxNavigations: number;
  private readonly now: () => number;
  private readonly entries = new Map<string, BudgetEntry>();
  private loaded = false;
  private lastPrune = 0;

  constructor(options: OriginBudgetOptions) {
    this.path = path.join(options.profileDirectory, STATE_RELATIVE_PATH);
    this.profileGeneration = options.profileGeneration;
    this.maxActions = boundedInteger(
      options.maxActionsPerMinute,
      1,
      600,
      "action budget",
    );
    this.maxNavigations = boundedInteger(
      options.maxNavigationsPerMinute,
      1,
      60,
      "navigation budget",
    );
    this.now = options.now ?? Date.now;
  }

  async load(): Promise<void> {
    if (this.loaded) {
      return;
    }
    let raw: Buffer;
    try {
      const info = await lstat(this.path);
      if (
        !info.isFile() ||
        info.isSymbolicLink() ||
        (info.mode & 0o077) !== 0 ||
        info.size <= 0 ||
        info.size > MAX_STATE_BYTES
      ) {
        throw new Error("invalid state file");
      }
      raw = await readFile(this.path);
    } catch (error) {
      if (isMissing(error)) {
        this.loaded = true;
        return;
      }
      throw new OriginBudgetCorruptError();
    }
    try {
      const document = parseDocument(raw);
      for (const entry of document.entries) {
        this.entries.set(entry.key, entry);
      }
      this.prune(this.now(), true);
      this.loaded = true;
    } catch {
      throw new OriginBudgetCorruptError();
    }
  }

  chargeAction(
    identity: Identity,
    origin: string,
    count = 1,
  ): number | undefined {
    return this.charge(identity, origin, "action", count);
  }

  chargeNavigation(identity: Identity, origin: string): number | undefined {
    return this.charge(identity, origin, "navigation");
  }

  retryDelay(identity: Identity, origin: string): number | undefined {
    const now = this.now();
    this.prune(now);
    const entry = this.entries.get(this.key(identity, origin));
    const deadline = entry?.retry_after_ms;
    if (deadline === undefined || deadline <= now) {
      return undefined;
    }
    return Math.max(1, Math.min(15 * 60 * 1000, deadline - now));
  }

  setRetryAfter(identity: Identity, origin: string, delayMS: number): void {
    const now = this.now();
    this.prune(now);
    const entry = this.entry(identity, origin, now);
    entry.retry_after_ms =
      now + boundedInteger(delayMS, 1, 15 * 60 * 1000, "Retry-After");
    entry.updated_at_ms = now;
  }

  async checkpoint(): Promise<void> {
    const now = this.now();
    this.prune(now, true);
    let raw = this.serialized();
    while (raw.byteLength > MAX_STATE_BYTES && this.entries.size > 1) {
      const oldest = [...this.entries.values()].sort(compareEntries)[0];
      if (oldest === undefined) {
        break;
      }
      this.entries.delete(oldest.key);
      raw = this.serialized();
    }
    if (raw.byteLength > MAX_STATE_BYTES) {
      throw new Error("Browser origin-budget state exceeds its size limit");
    }
    const directory = path.dirname(this.path);
    await mkdir(directory, { recursive: true, mode: 0o700 });
    const directoryInfo = await lstat(directory);
    if (!directoryInfo.isDirectory() || directoryInfo.isSymbolicLink()) {
      throw new Error("Browser origin-budget directory is invalid");
    }
    const temporary = `${this.path}.${randomBytes(8).toString("hex")}.tmp`;
    const handle = await open(temporary, "wx", 0o600);
    try {
      await handle.writeFile(raw);
      await handle.sync();
      await handle.close();
      await rename(temporary, this.path);
    } catch (error) {
      await handle.close().catch(() => undefined);
      await rm(temporary, { force: true }).catch(() => undefined);
      throw error;
    }
  }

  private serialized(): Buffer {
    const document: BudgetDocument = {
      contract_id: CONTRACT_ID,
      entries: [...this.entries.values()]
        .sort(compareEntries)
        .map(canonicalEntry),
    };
    return Buffer.from(JSON.stringify(document), "utf8");
  }

  private charge(
    identity: Identity,
    origin: string,
    kind: "action" | "navigation",
    count = 1,
  ): number | undefined {
    boundedInteger(count, 1, 8, `${kind} charge`);
    const now = this.now();
    this.prune(now);
    const entry = this.entry(identity, origin, now);
    const window = kind === "action" ? entry.action_ms : entry.navigation_ms;
    const maximum =
      kind === "action" ? this.maxActions : this.maxNavigations;
    discardBefore(window, now - 60_000);
    if (window.length + count > maximum) {
      return Math.max(
        1,
        Math.min(60_000, (window[0] ?? now) + 60_000 - now),
      );
    }
    for (let index = 0; index < count; index++) {
      window.push(now);
    }
    entry.updated_at_ms = now;
    return undefined;
  }

  private entry(identity: Identity, origin: string, now: number): BudgetEntry {
    const key = this.key(identity, origin);
    let entry = this.entries.get(key);
    if (entry === undefined) {
      if (this.entries.size >= MAX_ORIGINS) {
        const oldest = [...this.entries.values()].sort(compareEntries)[0];
        if (oldest !== undefined) {
          this.entries.delete(oldest.key);
        }
      }
      entry = {
        key,
        action_ms: [],
        navigation_ms: [],
        updated_at_ms: now,
      };
      this.entries.set(key, entry);
    }
    return entry;
  }

  private key(identity: Identity, origin: string): string {
    return createHash("sha256")
      .update("openlinker.browser.origin-budget.v1\u0000")
      .update(identity.agent_id)
      .update("\u0000")
      .update(identity.principal_scope_id)
      .update("\u0000")
      .update(String(this.profileGeneration))
      .update("\u0000")
      .update(origin)
      .digest("hex");
  }

  private prune(now: number, force = false): void {
    if (!force && now - this.lastPrune < 60_000) {
      return;
    }
    this.lastPrune = now;
    for (const [key, entry] of this.entries) {
      discardBefore(entry.action_ms, now - 60_000);
      discardBefore(entry.navigation_ms, now - 60_000);
      if (entry.retry_after_ms !== undefined && entry.retry_after_ms <= now) {
        delete entry.retry_after_ms;
      }
      if (entry.updated_at_ms < now - RETENTION_MS) {
        this.entries.delete(key);
      }
    }
    if (this.entries.size <= MAX_ORIGINS) {
      return;
    }
    for (const entry of [...this.entries.values()]
      .sort(compareEntries)
      .slice(0, this.entries.size - MAX_ORIGINS)) {
      this.entries.delete(entry.key);
    }
  }
}

export class OriginBudgetCorruptError extends Error {
  constructor() {
    super("Browser origin-budget state is invalid");
    this.name = "OriginBudgetCorruptError";
  }
}

export function validateOriginBudgetDocument(raw: Buffer): void {
  parseDocument(raw);
}

function parseDocument(raw: Buffer): BudgetDocument {
  const text = raw.toString("utf8");
  const value: unknown = JSON.parse(text);
  if (!isRecord(value) || !hasExactKeys(value, ["contract_id", "entries"])) {
    throw new Error("invalid budget document");
  }
  if (value.contract_id !== CONTRACT_ID || !Array.isArray(value.entries)) {
    throw new Error("unsupported budget document");
  }
  if (value.entries.length > MAX_ORIGINS) {
    throw new Error("too many budget entries");
  }
  const document: BudgetDocument = {
    contract_id: CONTRACT_ID,
    entries: value.entries.map(parseEntry),
  };
  const seen = new Set<string>();
  for (let index = 0; index < document.entries.length; index++) {
    const entry = document.entries[index]!;
    if (seen.has(entry.key)) {
      throw new Error("duplicate budget entry");
    }
    seen.add(entry.key);
    if (
      index > 0 &&
      compareEntries(document.entries[index - 1]!, entry) >= 0
    ) {
      throw new Error("budget entries are not canonically ordered");
    }
  }
  // The checkpoint writer emits one canonical compact representation.
  // Re-encoding the validated closed shape rejects duplicate object keys,
  // unknown key ordering, insignificant whitespace and non-canonical numbers.
  if (JSON.stringify(document) !== text) {
    throw new Error("non-canonical budget document");
  }
  return document;
}

function parseEntry(value: unknown): BudgetEntry {
  if (
    !isRecord(value) ||
    !hasOnlyKeys(value, [
      "key",
      "action_ms",
      "navigation_ms",
      "retry_after_ms",
      "updated_at_ms",
    ]) ||
    typeof value.key !== "string" ||
    !/^[0-9a-f]{64}$/.test(value.key) ||
    !validTimes(value.action_ms, 600) ||
    !validTimes(value.navigation_ms, 60) ||
    !nonNegativeSafeInteger(value.updated_at_ms) ||
    (value.retry_after_ms !== undefined &&
      !nonNegativeSafeInteger(value.retry_after_ms))
  ) {
    throw new Error("invalid budget entry");
  }
  const entry: BudgetEntry = {
    key: value.key,
    action_ms: value.action_ms,
    navigation_ms: value.navigation_ms,
    ...(value.retry_after_ms === undefined
      ? {}
      : { retry_after_ms: value.retry_after_ms as number }),
    updated_at_ms: value.updated_at_ms as number,
  };
  if (
    (entry.action_ms.at(-1) ?? 0) > entry.updated_at_ms ||
    (entry.navigation_ms.at(-1) ?? 0) > entry.updated_at_ms
  ) {
    throw new Error("budget entry update time is invalid");
  }
  return entry;
}

function validTimes(value: unknown, maximum: number): value is number[] {
  return (
    Array.isArray(value) &&
    value.length <= maximum &&
    value.every(
      (item, index) =>
        nonNegativeSafeInteger(item) &&
        (index === 0 || item >= (value[index - 1] as number)),
    )
  );
}

function nonNegativeSafeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

function discardBefore(values: number[], minimum: number): void {
  let index = 0;
  while (index < values.length && values[index]! <= minimum) {
    index++;
  }
  if (index > 0) {
    values.splice(0, index);
  }
}

function compareEntries(left: BudgetEntry, right: BudgetEntry): number {
  if (left.updated_at_ms !== right.updated_at_ms) {
    return left.updated_at_ms - right.updated_at_ms;
  }
  return left.key < right.key ? -1 : left.key > right.key ? 1 : 0;
}

function canonicalEntry(entry: BudgetEntry): BudgetEntry {
  return {
    key: entry.key,
    action_ms: entry.action_ms,
    navigation_ms: entry.navigation_ms,
    ...(entry.retry_after_ms === undefined
      ? {}
      : { retry_after_ms: entry.retry_after_ms }),
    updated_at_ms: entry.updated_at_ms,
  };
}

function boundedInteger(
  value: number,
  minimum: number,
  maximum: number,
  label: string,
): number {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function isMissing(error: unknown): boolean {
  return (
    error !== null &&
    typeof error === "object" &&
    "code" in error &&
    error.code === "ENOENT"
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function hasExactKeys(
  value: Record<string, unknown>,
  keys: string[],
): boolean {
  return (
    Object.keys(value).length === keys.length &&
    hasOnlyKeys(value, keys)
  );
}

function hasOnlyKeys(
  value: Record<string, unknown>,
  keys: string[],
): boolean {
  const allowed = new Set(keys);
  return Object.keys(value).every((key) => allowed.has(key));
}
