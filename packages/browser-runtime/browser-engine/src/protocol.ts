import { isIP } from "node:net";
import { createHash } from "node:crypto";

import {
  BROWSER_VIEWPORT_HEIGHT,
  BROWSER_VIEWPORT_WIDTH,
} from "./browser-contract.generated.js";

export const ENGINE_CONTRACT_ID = "openlinker.browser.engine.v2";
export const ENGINE_VIEWER_CONTRACT_ID =
  "openlinker.browser.engine.viewer.v1";
export const ENGINE_OPS_OBSERVER_CONTRACT_ID =
  "openlinker.browser.engine.ops-observer.v1";

export type Controller = "agent" | "none" | "human";

export const ACTION_KINDS = [
  "navigate",
  "click",
  "type_non_secret",
  "scroll",
  "keypress",
  "select",
  "wait",
  "back",
  "forward",
  "screenshot",
  "checkpoint",
  "preflight",
  "batch",
] as const;

export type ActionKind = (typeof ACTION_KINDS)[number];

export const OBSERVATION_MODES = [
  "semantic",
  "screenshot",
  "both",
  "none",
] as const;

export type ObservationMode = (typeof OBSERVATION_MODES)[number];

export interface Identity {
  run_id: string;
  agent_id: string;
  principal_scope_id: string;
  browser_session_id: string;
  session_epoch: number;
  attachment_id: string;
  control_epoch: number;
  controller: Controller;
  browser_interaction_policy: "restricted" | "full";
  browser_interaction_policy_generation: number;
  browser_mutation_origins: string[];
  browser_mutation_origins_sha256: string;
}

export interface BrowserAction {
  kind: ActionKind;
  observation?: ObservationMode;
  url?: string;
  x?: number;
  y?: number;
  delta_x?: number;
  delta_y?: number;
  text?: string;
  key?: string;
  value?: string;
  duration_ms?: number;
  actions?: BrowserAction[];
}

export interface EngineRequest {
  contract_id: typeof ENGINE_CONTRACT_ID;
  action_id: string;
  deadline: string;
  identity: Identity;
  action: BrowserAction;
}

export type ViewerOperation = "enter" | "exit" | "frame" | "input";

export type ViewerInput =
  | {
      kind: "pointer";
      pointer_action: "move" | "click";
      x: number;
      y: number;
      button?: "left" | "middle" | "right";
      click_count?: number;
    }
  | {
      kind: "keyboard";
      keyboard_action: "press" | "text";
      key?: string;
      text?: string;
    }
  | {
      kind: "scroll";
      delta_x: number;
      delta_y: number;
    };

export interface EngineViewerRequest {
  contract_id: typeof ENGINE_VIEWER_CONTRACT_ID;
  action_id: string;
  deadline: string;
  identity: Identity;
  operation: ViewerOperation;
  input?: ViewerInput;
}

export type OpsObserverOperation = "observe_status" | "observe_frame";

export interface EngineOpsObserverRequest {
  contract_id: typeof ENGINE_OPS_OBSERVER_CONTRACT_ID;
  action_id: string;
  deadline: string;
  identity: Identity;
  operation: OpsObserverOperation;
}

export type BrowserErrorCode =
  | "BROWSER_OUTPUT_TOO_LARGE"
  | "BROWSER_RUNTIME_UNAVAILABLE"
  | "BROWSER_ENGINE_UNAVAILABLE"
  | "BROWSER_EGRESS_UNAVAILABLE"
  | "BROWSER_TARGET_BLOCKED"
  | "BROWSER_PROFILE_LOCKED"
  | "BROWSER_PROFILE_CORRUPT"
  | "BROWSER_PROFILE_ENVIRONMENT_MISMATCH"
  | "BROWSER_PROFILE_ENGINE_DOWNGRADE_UNSUPPORTED"
  | "BROWSER_PROFILE_ENGINE_UPGRADE_FAILED"
  | "BROWSER_USER_ACTION_REQUIRED"
  | "BROWSER_HIGH_IMPACT_ACTION_BLOCKED"
  | "BROWSER_MUTATION_ORIGIN_BLOCKED"
  | "BROWSER_MUTATION_OUTCOME_UNKNOWN"
  | "BROWSER_ACCESS_DENIED"
  | "BROWSER_RATE_LIMITED"
  | "BROWSER_CHALLENGE_SUSPECTED"
  | "BROWSER_CHALLENGE_REQUIRED"
  | "BROWSER_ORIGIN_RATE_LIMITED"
  | "BROWSER_ACTION_LIMIT_EXCEEDED"
  | "BROWSER_CLICK_RETRY_EXHAUSTED"
  | "BROWSER_CLOSE_RETRY_EXHAUSTED"
  | "BROWSER_CANCELED"
  | "BROWSER_ACTION_REJECTED"
  | "BROWSER_OUTPUT_INVALID"
  | "BROWSER_INTERNAL";

export type ClickEffect = "activated" | "focused";

export type TargetCategory =
  | "link"
  | "text_input"
  | "button"
  | "custom"
  | "none"
  | "other";

export interface EngineFailure {
  code: BrowserErrorCode;
  message: string;
  recoverable: boolean;
  action_index?: number;
  target_category?: TargetCategory;
  page_state_id?: string;
  navigation_generation?: number;
  blocked_click_navigation_attempts_remaining?: number;
  blocked_click_run_attempts_remaining?: number;
  site_outcome?: SiteOutcome;
  retry_after_ms?: number;
  classifier_rules_version?: string;
  consecutive_access_denials?: number;
  origin_blocked_for_attachment?: boolean;
  challenge_release_unavailable?: boolean;
  retry_same_action?: boolean;
  attachment_usable?: boolean;
  fresh_observation_required?: boolean;
  mutation_outcome_reason?: string;
  attempted_units?: number;
  undispatched_units?: number;
  completed_actions?: number;
  mutation_requests_observed?: number;
  observed_origin?: string;
  browser_mutation_origins?: string[];
}

export type SiteOutcome =
  | "BROWSER_ACCESS_DENIED"
  | "BROWSER_RATE_LIMITED"
  | "BROWSER_CHALLENGE_SUSPECTED"
  | "BROWSER_CHALLENGE_REQUIRED"
  | "BROWSER_ORIGIN_RATE_LIMITED";

export interface EnvironmentEvidence {
  browser_engine: "chromium" | "chrome";
  browser_distribution:
    | "playwright_chromium"
    | "google_chrome"
    | "chrome_for_testing";
  browser_version: string;
  browser_major_version: number;
  browser_locale: string;
  browser_timezone: string;
  font_contract_version: string;
  font_manifest_sha256: string;
}

export interface Observation {
  page_state_id: string;
  viewport: {
    width: number;
    height: number;
  };
  navigation_generation: number;
  screenshot?: {
    mime_type: "image/jpeg";
    data: string;
    width: number;
    height: number;
  };
  ax_tree?: unknown;
  dom_diff?: unknown;
  ax_tree_timed_out?: boolean;
  dom_diff_timed_out?: boolean;
  origin?: string;
  title?: string;
  click_effect?: ClickEffect;
  target_category?: "link" | "text_input" | "button" | "custom" | "other";
  environment?: EnvironmentEvidence;
  site_outcome?: SiteOutcome;
  classifier_rules_version?: string;
  challenge_release_unavailable?: boolean;
  blocked_mutation_requests?: number;
  mutation_requests_observed?: number;
}

export type EngineResponse =
  | {
      contract_id: typeof ENGINE_CONTRACT_ID;
      action_id: string;
      status: "ok";
      observation: Observation;
    }
  | {
      contract_id: typeof ENGINE_CONTRACT_ID;
      action_id: string;
      status: "error";
      error: EngineFailure;
    };

export type EngineViewerResponse =
  | {
      contract_id: typeof ENGINE_VIEWER_CONTRACT_ID;
      action_id: string;
      status: "ok";
      frame?: {
        mime_type: "image/jpeg";
        data: string;
        width: number;
        height: number;
      };
    }
  | {
      contract_id: typeof ENGINE_VIEWER_CONTRACT_ID;
      action_id: string;
      status: "error";
      error: EngineFailure;
    };

export type EngineOpsObserverResponse =
  | {
      contract_id: typeof ENGINE_OPS_OBSERVER_CONTRACT_ID;
      action_id: string;
      status: "ok";
      page_url: string;
      page_title: string;
      frame?: {
        mime_type: "image/jpeg";
        data: string;
        width: number;
        height: number;
      };
    }
  | {
      contract_id: typeof ENGINE_OPS_OBSERVER_CONTRACT_ID;
      action_id: string;
      status: "error";
      error: {
        code: "RUN_NOT_ACTIVE" | "OPS_VIEWER_BUSY" | "OPS_VIEWER_INTERNAL";
        message: string;
      };
    };

const REQUEST_FIELDS = new Set([
  "contract_id",
  "action_id",
  "deadline",
  "identity",
  "action",
]);
const IDENTITY_FIELDS = new Set([
  "run_id",
  "agent_id",
  "principal_scope_id",
  "browser_session_id",
  "session_epoch",
  "attachment_id",
  "control_epoch",
  "controller",
  "browser_interaction_policy",
  "browser_interaction_policy_generation",
  "browser_mutation_origins",
  "browser_mutation_origins_sha256",
]);
const VIEWER_REQUEST_FIELDS = new Set([
  "contract_id",
  "action_id",
  "deadline",
  "identity",
  "operation",
  "input",
]);
const OPS_OBSERVER_REQUEST_FIELDS = new Set([
  "contract_id",
  "action_id",
  "deadline",
  "identity",
  "operation",
]);
const VIEWER_INPUT_FIELDS = new Set([
  "kind",
  "pointer_action",
  "keyboard_action",
  "x",
  "y",
  "button",
  "click_count",
  "key",
  "text",
  "delta_x",
  "delta_y",
]);
const ACTION_FIELDS = new Set([
  "kind",
  "observation",
  "url",
  "x",
  "y",
  "delta_x",
  "delta_y",
  "text",
  "key",
  "value",
  "duration_ms",
  "actions",
]);

export function parseRequest(line: string, now = Date.now()): EngineRequest {
  if (Buffer.byteLength(line, "utf8") > 256 * 1024) {
    throw new Error("engine request exceeds input limit");
  }
  const value: unknown = JSON.parse(line);
  const request = requireRecord(value, "request");
  requireExactFields(request, REQUEST_FIELDS, "request");
  if (request.contract_id !== ENGINE_CONTRACT_ID) {
    throw new Error("unsupported engine contract");
  }
  const actionID = requireString(request.action_id, "action_id", 1, 32);
  if (!/^[1-9][0-9]*$/.test(actionID)) {
    throw new Error("action_id is invalid");
  }
  const deadline = parseDeadline(request.deadline, now);
  const identity = parseIdentity(request.identity);
  if (identity.controller !== "agent") {
    throw new Error("browser action does not hold agent control");
  }
  const action = parseAction(request.action);
  validateActionPolicy(action, identity.browser_interaction_policy);
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline,
    identity,
    action,
  };
}

export function parseViewerRequest(
  line: string,
  now = Date.now(),
): EngineViewerRequest {
  if (Buffer.byteLength(line, "utf8") > 256 * 1024) {
    throw new Error("engine viewer request exceeds input limit");
  }
  const value: unknown = JSON.parse(line);
  const request = requireRecord(value, "request");
  requireKnownFields(request, VIEWER_REQUEST_FIELDS, "request");
  for (const field of [
    "contract_id",
    "action_id",
    "deadline",
    "identity",
    "operation",
  ]) {
    if (!(field in request)) {
      throw new Error("request is missing a field");
    }
  }
  if (request.contract_id !== ENGINE_VIEWER_CONTRACT_ID) {
    throw new Error("unsupported engine viewer contract");
  }
  const actionID = requireString(request.action_id, "action_id", 1, 32);
  if (!/^[1-9][0-9]*$/.test(actionID)) {
    throw new Error("action_id is invalid");
  }
  const identity = parseIdentity(request.identity);
  if (identity.controller !== "human") {
    throw new Error("browser viewer request does not hold human control");
  }
  const operation = requireString(request.operation, "operation", 4, 16);
  if (!["enter", "exit", "frame", "input"].includes(operation)) {
    throw new Error("browser viewer operation is invalid");
  }
  const input =
    request.input === undefined
      ? undefined
      : parseViewerInput(request.input);
  if ((operation === "input") !== (input !== undefined)) {
    throw new Error("browser viewer input shape is invalid");
  }
  return {
    contract_id: ENGINE_VIEWER_CONTRACT_ID,
    action_id: actionID,
    deadline: parseDeadline(request.deadline, now),
    identity,
    operation: operation as ViewerOperation,
    ...(input === undefined ? {} : { input }),
  };
}

export function parseOpsObserverRequest(
  line: string,
  now = Date.now(),
): EngineOpsObserverRequest {
  if (Buffer.byteLength(line, "utf8") > 256 * 1024) {
    throw new Error("engine Ops Observer request exceeds input limit");
  }
  const value: unknown = JSON.parse(line);
  const request = requireRecord(value, "request");
  requireExactFields(request, OPS_OBSERVER_REQUEST_FIELDS, "request");
  if (request.contract_id !== ENGINE_OPS_OBSERVER_CONTRACT_ID) {
    throw new Error("unsupported engine Ops Observer contract");
  }
  const actionID = requireString(request.action_id, "action_id", 1, 32);
  if (!/^[1-9][0-9]*$/.test(actionID)) {
    throw new Error("action_id is invalid");
  }
  const operation = requireString(request.operation, "operation", 13, 14);
  if (operation !== "observe_status" && operation !== "observe_frame") {
    throw new Error("engine Ops Observer operation is invalid");
  }
  return {
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: actionID,
    deadline: parseDeadline(request.deadline, now),
    identity: parseIdentity(request.identity),
    operation,
  };
}

function parseIdentity(value: unknown): Identity {
  const identity = requireRecord(value, "identity");
  requireExactFields(identity, IDENTITY_FIELDS, "identity");
  const parsed: Identity = {
    run_id: requireUUID(identity.run_id, "run_id"),
    agent_id: requireUUID(identity.agent_id, "agent_id"),
    principal_scope_id: requireOpaque(identity.principal_scope_id, "principal_scope_id", 256),
    browser_session_id: requireUUID(identity.browser_session_id, "browser_session_id"),
    session_epoch: requirePositiveInteger(identity.session_epoch, "session_epoch"),
    attachment_id: requireUUID(identity.attachment_id, "attachment_id"),
    control_epoch: requirePositiveInteger(identity.control_epoch, "control_epoch"),
    controller: requireController(identity.controller),
    browser_interaction_policy: requireInteractionPolicy(
      identity.browser_interaction_policy,
    ),
    browser_interaction_policy_generation: requirePositiveInteger(
      identity.browser_interaction_policy_generation,
      "browser_interaction_policy_generation",
    ),
    browser_mutation_origins: requireMutationOrigins(
      identity.browser_mutation_origins,
      identity.browser_mutation_origins_sha256,
      identity.browser_interaction_policy,
    ),
    browser_mutation_origins_sha256: requireSHA256(
      identity.browser_mutation_origins_sha256,
      "browser_mutation_origins_sha256",
    ),
  };
  return parsed;
}

function requireInteractionPolicy(value: unknown): "restricted" | "full" {
  if (value !== "restricted" && value !== "full") {
    throw new Error("browser_interaction_policy is invalid");
  }
  return value;
}

function requireMutationOrigins(
  value: unknown,
  digestValue: unknown,
  policyValue: unknown,
): string[] {
  if (!Array.isArray(value) || value.length > 32) {
    throw new Error("browser_mutation_origins is invalid");
  }
  const origins = value.map((entry) =>
    requireString(entry, "browser_mutation_origins entry", 9, 512),
  );
  if (
    (policyValue === "restricted" && origins.length !== 0) ||
    (policyValue === "full" && origins.length === 0)
  ) {
    throw new Error("browser_mutation_origins does not match the policy");
  }
  let previous = "";
  for (const origin of origins) {
    if (canonicalHTTPSOrigin(origin) !== origin || origin <= previous) {
      throw new Error("browser_mutation_origins is not canonical");
    }
    previous = origin;
  }
  const digest = createHash("sha256")
    .update(JSON.stringify(origins), "utf8")
    .digest("hex");
  if (digestValue !== digest) {
    throw new Error("browser_mutation_origins_sha256 does not match");
  }
  return origins;
}

export function canonicalHTTPSOrigin(raw: string): string {
  if (
    raw.trim() !== raw ||
    raw.includes("%") ||
    !/^https:\/\/[^/?#]+$/u.test(raw)
  ) {
    return "";
  }
  try {
    const url = new URL(raw);
    if (
      url.protocol !== "https:" ||
      url.username !== "" ||
      url.password !== "" ||
      url.hostname.endsWith(".") ||
      url.pathname !== "/" ||
      url.search !== "" ||
      url.hash !== ""
    ) {
      return "";
    }
    if (!canonicalURLHostnameAllowed(url.hostname)) {
      return "";
    }
    return url.origin;
  } catch {
    return "";
  }
}

function canonicalURLHostnameAllowed(hostname: string): boolean {
  if (hostname.startsWith("[") && hostname.endsWith("]")) {
    // WHATWG rewrites every IPv4-mapped IPv6 spelling to this form, while
    // Go's netip canonical form retains dotted IPv4 bytes. Reject the
    // ambiguous family instead of binding different strings to one endpoint.
    return !/^\[::ffff:[0-9a-f]{1,4}:[0-9a-f]{1,4}\]$/u.test(hostname);
  }
  if (/^[0-9]+(?:\.[0-9]+){3}$/u.test(hostname)) {
    return true;
  }
  if (hostname.length === 0 || hostname.length > 253) {
    return false;
  }
  return hostname.split(".").every(
    (label) =>
      label.length > 0 &&
      label.length <= 63 &&
      /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/u.test(label),
  );
}

function parseViewerInput(value: unknown): ViewerInput {
  const input = requireRecord(value, "viewer input");
  requireKnownFields(input, VIEWER_INPUT_FIELDS, "viewer input");
  const kind = requireString(input.kind, "kind", 6, 16);
  switch (kind) {
    case "pointer": {
      const pointerAction = requireString(
        input.pointer_action,
        "pointer_action",
        4,
        8,
      );
      const x = requireInteger(input.x, "x");
      const y = requireInteger(input.y, "y");
      validateCoordinates({ kind: "click", x, y });
      if (pointerAction === "move") {
        requireOnlyViewerInputFields(input, [
          "kind",
          "pointer_action",
          "x",
          "y",
        ]);
        return { kind, pointer_action: pointerAction, x, y };
      }
      if (pointerAction !== "click") {
        throw new Error("browser viewer pointer action is invalid");
      }
      requireOnlyViewerInputFields(input, [
        "kind",
        "pointer_action",
        "x",
        "y",
        "button",
        "click_count",
      ]);
      const button = requireString(input.button, "button", 4, 6);
      if (!["left", "middle", "right"].includes(button)) {
        throw new Error("browser viewer pointer button is invalid");
      }
      const clickCount = requireInteger(input.click_count, "click_count");
      if (clickCount < 1 || clickCount > 3) {
        throw new Error("browser viewer click count is invalid");
      }
      return {
        kind,
        pointer_action: pointerAction,
        x,
        y,
        button: button as "left" | "middle" | "right",
        click_count: clickCount,
      };
    }
    case "keyboard": {
      const keyboardAction = requireString(
        input.keyboard_action,
        "keyboard_action",
        4,
        8,
      );
      if (keyboardAction === "press") {
        requireOnlyViewerInputFields(input, [
          "kind",
          "keyboard_action",
          "key",
        ]);
        return {
          kind,
          keyboard_action: keyboardAction,
          key: requireString(input.key, "key", 1, 64),
        };
      }
      if (keyboardAction !== "text") {
        throw new Error("browser viewer keyboard action is invalid");
      }
      requireOnlyViewerInputFields(input, [
        "kind",
        "keyboard_action",
        "text",
      ]);
      const text = requireString(input.text, "text", 1, 4096);
      if (text.includes("\u0000")) {
        throw new Error("browser viewer text input is invalid");
      }
      return { kind, keyboard_action: keyboardAction, text };
    }
    case "scroll": {
      requireOnlyViewerInputFields(input, ["kind", "delta_x", "delta_y"]);
      const deltaX = requireFiniteNumber(input.delta_x, "delta_x");
      const deltaY = requireFiniteNumber(input.delta_y, "delta_y");
      if (
        (deltaX === 0 && deltaY === 0) ||
        Math.abs(deltaX) > 4096 ||
        Math.abs(deltaY) > 4096
      ) {
        throw new Error("browser viewer scroll input is invalid");
      }
      return { kind, delta_x: deltaX, delta_y: deltaY };
    }
    default:
      throw new Error("browser viewer input kind is invalid");
  }
}

function requireOnlyViewerInputFields(
  input: Record<string, unknown>,
  fields: string[],
): void {
  const allowed = new Set(fields);
  requireExactFields(input, allowed, "viewer input");
}

function parseDeadline(value: unknown, now: number): string {
  const deadline = requireString(value, "deadline", 20, 64);
  const deadlineMillis = Date.parse(deadline);
  if (
    !Number.isFinite(deadlineMillis) ||
    deadlineMillis <= now ||
    deadlineMillis > now + 60_000
  ) {
    throw new Error("deadline has elapsed");
  }
  return deadline;
}

function requireController(value: unknown): Controller {
  const controller = requireString(value, "controller", 4, 5);
  if (!["agent", "none", "human"].includes(controller)) {
    throw new Error("controller is invalid");
  }
  return controller as Controller;
}

function requireFiniteNumber(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${label} must be finite`);
  }
  return value;
}

function parseAction(value: unknown): BrowserAction {
  const action = requireRecord(value, "action");
  requireKnownFields(action, ACTION_FIELDS, "action");
  const kind = requireString(action.kind, "kind", 1, 64);
  if (!ACTION_KINDS.includes(kind as ActionKind)) {
    throw new Error("action kind is not allowed");
  }
  const parsed: BrowserAction = { kind: kind as ActionKind };
  if (action.observation !== undefined) {
    const observation = requireString(
      action.observation,
      "observation",
      1,
      32,
    );
    if (!OBSERVATION_MODES.includes(observation as ObservationMode)) {
      throw new Error("observation mode is invalid");
    }
    parsed.observation = observation as ObservationMode;
  }
  for (const field of ["url", "text", "key", "value"] as const) {
    if (action[field] !== undefined) {
      const maximum =
        field === "text" ? 16 * 1024 : field === "url" ? 4096 : 2048;
      parsed[field] = requireString(action[field], field, 1, maximum);
    }
  }
  for (const field of ["x", "y", "delta_x", "delta_y", "duration_ms"] as const) {
    if (action[field] !== undefined) {
      parsed[field] = requireInteger(action[field], field);
    }
  }
  if (action.actions !== undefined) {
    if (!Array.isArray(action.actions)) {
      throw new Error("actions must be an array");
    }
    parsed.actions = action.actions.map((nested) => parseAction(nested));
  }
  validateActionShape(parsed);
  return parsed;
}

function validateActionShape(action: BrowserAction): void {
  const present = new Set(
    Object.entries(action)
      .filter(
        ([field, value]) =>
          field !== "kind" && field !== "observation" && value !== undefined,
      )
      .map(([field]) => field),
  );
  const requireFields = (...fields: string[]): void => {
    if (present.size !== fields.length || fields.some((field) => !present.has(field))) {
      throw new Error(`invalid ${action.kind} action shape`);
    }
  };
  switch (action.kind) {
    case "navigate":
      requireFields("url");
      if (!isPublicHTTPURL(action.url ?? "")) {
        throw new Error("navigate URL is invalid");
      }
      return;
    case "click":
      requireFields("x", "y");
      validateCoordinates(action);
      return;
    case "type_non_secret":
      requireFields("text");
      return;
    case "scroll":
      if (
        present.size === 0 ||
        [...present].some((field) => field !== "delta_x" && field !== "delta_y") ||
        ((action.delta_x ?? 0) === 0 && (action.delta_y ?? 0) === 0)
      ) {
        throw new Error("invalid scroll action shape");
      }
      for (const delta of [action.delta_x ?? 0, action.delta_y ?? 0]) {
        if (Math.abs(delta) > 32768) {
          throw new Error("scroll delta is out of range");
        }
      }
      return;
    case "keypress":
      requireFields("key");
      if (
        ![
          "Enter",
          "Space",
          "Tab",
          "Escape",
          "ArrowUp",
          "ArrowDown",
          "ArrowLeft",
          "ArrowRight",
          "PageUp",
          "PageDown",
          "Home",
          "End",
          "Backspace",
          "Delete",
        ].includes(action.key ?? "")
      ) {
        throw new Error("keypress key is not allowed");
      }
      return;
    case "select":
      requireFields("value");
      return;
    case "wait":
      requireFields("duration_ms");
      if ((action.duration_ms ?? 0) < 1 || (action.duration_ms ?? 0) > 5000) {
        throw new Error("wait duration is out of range");
      }
      return;
    case "back":
    case "forward":
    case "screenshot":
    case "checkpoint":
    case "preflight":
      requireFields();
      return;
    case "batch":
      requireFields("actions");
      if ((action.actions?.length ?? 0) < 2 || (action.actions?.length ?? 0) > 8) {
        throw new Error("browser batch requires two to eight actions");
      }
      for (const nested of action.actions ?? []) {
        if (
          nested.observation !== undefined ||
          ["batch", "checkpoint", "preflight"].includes(nested.kind)
        ) {
          throw new Error("browser batch contains a disallowed action");
        }
      }
      return;
  }
}

function validateActionPolicy(
  action: BrowserAction,
  policy: "restricted" | "full",
): void {
  if (policy === "full") {
    return;
  }
  if (action.kind === "select") {
    throw new Error("Phase 1 does not allow select controls");
  }
  if (action.kind === "keypress" && action.key === "Space") {
    throw new Error("restricted policy does not allow Space");
  }
  if (
    action.kind === "batch" &&
    action.actions?.some(
      (nested) => !["scroll", "wait", "screenshot"].includes(nested.kind),
    )
  ) {
    throw new Error("browser batch contains a disallowed action");
  }
}

function validateCoordinates(action: BrowserAction): void {
  if (
    action.x === undefined ||
    action.y === undefined ||
    action.x < 0 ||
    action.y < 0 ||
    action.x >= BROWSER_VIEWPORT_WIDTH ||
    action.y >= BROWSER_VIEWPORT_HEIGHT
  ) {
    throw new Error("coordinates are out of range");
  }
}

export function isPublicHTTPURL(raw: string): boolean {
  try {
    const url = new URL(raw);
    return (
      (url.protocol === "http:" || url.protocol === "https:") &&
      url.username === "" &&
      url.password === "" &&
      isIP(url.hostname) === 0 &&
      url.hostname.includes(".") &&
      !url.hostname.endsWith(".local") &&
      !url.hostname.endsWith(".internal")
    );
  } catch {
    return false;
  }
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function requireExactFields(
  value: Record<string, unknown>,
  fields: ReadonlySet<string>,
  label: string,
): void {
  requireKnownFields(value, fields, label);
  if ([...fields].some((field) => !(field in value))) {
    throw new Error(`${label} is missing a field`);
  }
}

function requireKnownFields(
  value: Record<string, unknown>,
  fields: ReadonlySet<string>,
  label: string,
): void {
  if (Object.keys(value).some((field) => !fields.has(field))) {
    throw new Error(`${label} contains an unknown field`);
  }
}

function requireString(
  value: unknown,
  label: string,
  minimum: number,
  maximum: number,
): string {
  if (
    typeof value !== "string" ||
    Buffer.byteLength(value, "utf8") < minimum ||
    Buffer.byteLength(value, "utf8") > maximum
  ) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function requireUUID(value: unknown, label: string): string {
  const parsed = requireString(value, label, 36, 36);
  if (
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(parsed) ||
    parsed === "00000000-0000-0000-0000-000000000000"
  ) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

function requireOpaque(value: unknown, label: string, maximum: number): string {
  const parsed = requireString(value, label, 1, maximum);
  if (!/^[A-Za-z0-9._:-]+$/.test(parsed)) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

function requireSHA256(value: unknown, label: string): string {
  const parsed = requireString(value, label, 64, 64);
  if (!/^[0-9a-f]{64}$/u.test(parsed)) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

function requirePositiveInteger(value: unknown, label: string): number {
  const parsed = requireInteger(value, label);
  if (parsed < 1) {
    throw new Error(`${label} must be positive`);
  }
  return parsed;
}

function requireInteger(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    throw new Error(`${label} must be an integer`);
  }
  return value;
}

export function success(actionID: string, observation: Observation): EngineResponse {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    status: "ok",
    observation,
  };
}

export function failure(
  actionID: string,
  code: BrowserErrorCode,
  message: string,
  recoverable: boolean,
  actionIndex?: number,
  details: Partial<
    Pick<
      EngineFailure,
      | "target_category"
      | "page_state_id"
      | "navigation_generation"
      | "blocked_click_navigation_attempts_remaining"
      | "blocked_click_run_attempts_remaining"
      | "site_outcome"
      | "retry_after_ms"
      | "classifier_rules_version"
      | "consecutive_access_denials"
      | "origin_blocked_for_attachment"
      | "challenge_release_unavailable"
      | "retry_same_action"
      | "attachment_usable"
      | "fresh_observation_required"
      | "mutation_outcome_reason"
      | "attempted_units"
      | "undispatched_units"
      | "completed_actions"
      | "mutation_requests_observed"
      | "observed_origin"
      | "browser_mutation_origins"
    >
  > = {},
): Extract<EngineResponse, { status: "error" }> {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    status: "error",
    error: {
      code,
      message: truncateUTF8(message.trim(), 500),
      recoverable,
      ...(actionIndex === undefined ? {} : { action_index: actionIndex }),
      ...details,
    },
  };
}

export function viewerSuccess(
  actionID: string,
  frame?: {
    mime_type: "image/jpeg";
    data: string;
    width: number;
    height: number;
  },
): EngineViewerResponse {
  return {
    contract_id: ENGINE_VIEWER_CONTRACT_ID,
    action_id: actionID,
    status: "ok",
    ...(frame === undefined ? {} : { frame }),
  };
}

export function viewerFailure(
  actionID: string,
  code: BrowserErrorCode,
  message: string,
  recoverable: boolean,
): EngineViewerResponse {
  return {
    contract_id: ENGINE_VIEWER_CONTRACT_ID,
    action_id: actionID,
    status: "error",
    error: {
      code,
      message: truncateUTF8(message.trim(), 500),
      recoverable,
    },
  };
}

export function opsObserverSuccess(
  actionID: string,
  pageURL: string,
  pageTitle: string,
  frame?: {
    mime_type: "image/jpeg";
    data: string;
    width: number;
    height: number;
  },
): EngineOpsObserverResponse {
  return {
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: actionID,
    status: "ok",
    page_url: pageURL,
    page_title: truncateUTF8(pageTitle, 512),
    ...(frame === undefined ? {} : { frame }),
  };
}

export function opsObserverFailure(
  actionID: string,
  code: "RUN_NOT_ACTIVE" | "OPS_VIEWER_BUSY" | "OPS_VIEWER_INTERNAL",
  message: string,
): EngineOpsObserverResponse {
  return {
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: actionID,
    status: "error",
    error: {
      code,
      message: truncateUTF8(message.trim(), 300),
    },
  };
}

export function truncateUTF8(value: string, maximumBytes: number): string {
  if (Buffer.byteLength(value, "utf8") <= maximumBytes) {
    return value;
  }
  let low = 0;
  let high = value.length;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (Buffer.byteLength(value.slice(0, middle), "utf8") <= maximumBytes) {
      low = middle;
    } else {
      high = middle - 1;
    }
  }
  return value.slice(0, low);
}
