import { createHash } from "node:crypto";
import path from "node:path";

import {
  chromium,
  errors as playwrightErrors,
  type BrowserContext,
  type ElementHandle,
  type Frame,
  type Page,
  type Request as PlaywrightRequest,
  type WebSocketRoute,
} from "playwright-core";

import {
  gatewayBlockedResponse,
  isTunnelConnectionFailure,
  probeEgressGateway,
} from "./egress-policy.js";
import {
  failure,
  type BrowserAction,
  type BrowserErrorCode,
  type ClickEffect,
  type EngineRequest,
  type EngineResponse,
  type EngineViewerRequest,
  type EngineViewerResponse,
  type EngineOpsObserverRequest,
  type EngineOpsObserverResponse,
  type EngineFailure,
  type EnvironmentEvidence,
  type Observation,
  type ObservationMode,
  type TargetCategory,
  type Controller,
  canonicalHTTPSOrigin,
  isPublicHTTPURL,
  success,
  truncateUTF8,
  viewerFailure,
  viewerSuccess,
  opsObserverFailure,
  opsObserverSuccess,
} from "./protocol.js";
import {
  allowsPageRequestMethod,
  blocksHighImpactActivation,
  blocksKeypress,
  blocksNonSecretTyping,
  type InteractiveMetadata,
} from "./policy.js";
import {
  PageContinuationCorruptError,
  PageContinuationStore,
  pageContinuationSessionKey,
} from "./page-continuation.js";
import {
  environmentEvidence,
  parseBrowserEnvironment,
} from "./environment.js";
import {
  BROWSER_VIEWPORT_HEIGHT,
  BROWSER_VIEWPORT_WIDTH,
} from "./browser-contract.generated.js";
import { OriginBudgetStore } from "./origin-budget.js";
import {
  CLASSIFIER_RULES_VERSION,
  classifyChallenge,
  parseRetryAfter,
} from "./site-classifier.js";
import { DocumentGenerationTracker } from "./document-generation.js";
import { NativeChromeGate } from "./native-chrome-gate.js";

const MAX_SCREENSHOT_BYTES = 4 * 1024 * 1024;
const MAX_VIEWER_FRAME_BYTES = 1024 * 1024;
const MAX_STATE_TEXT_BYTES = 256 * 1024;
const MAX_TYPE_VALUE_BYTES = 16 * 1024;
const MAX_PAGES = 4;
const MAX_TRACKED_MUTATION_REQUESTS_PER_ACTION = 32;
const MUTATION_RESPONSE_SETTLE_MS = 2_000;
const VIEWPORT = {
  width: BROWSER_VIEWPORT_WIDTH,
  height: BROWSER_VIEWPORT_HEIGHT,
};
type GatewayHealthProbe = (timeout: number) => Promise<boolean>;
type DocumentTrackerFactory = (
  page: Page,
) => Promise<DocumentGenerationTracker>;
type TrackedMutationRequest = {
  sequence: number;
  outcome: "pending" | "finished" | "failed";
  settled: Promise<void>;
  resolve: () => void;
};
export type ControlState = {
  controller: Controller;
  attachmentKey: string;
  pageWebSockets: Set<WebSocketRoute>;
  interactionPolicy: "restricted" | "full";
  mutationOrigins: Set<string>;
  mutationBlockSerial: number;
  blockedMutationRequests: number;
  actionInFlight: boolean;
  frameOrigins: () => string[];
};

const MAX_HUMAN_PAGE_WEBSOCKETS = 64;

const CHROMIUM_FLAGS = [
  "--disable-background-networking",
  "--disable-component-update",
  "--disable-default-apps",
  "--disable-domain-reliability",
  "--disable-features=DnsOverHttpsUpgrade",
  "--disable-quic",
  "--disable-sync",
  "--dns-over-https-mode=off",
  "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
  "--metrics-recording-only",
  "--no-first-run",
  "--no-pings",
  "--webrtc-ip-handling-policy=disable_non_proxied_udp",
] as const;

export class BrowserEngine {
  private readonly context: BrowserContext;
  private readonly continuation: PageContinuationStore;
  private page: Page;
  private boundSessionKey = "";
  private boundAttachmentKey = "";
  private boundRunID = "";
  private lastBodyText = "";
  private lastSettledPageTitle = "";
  private navigationGeneration = 1;
  private readonly blockedNavigationPages = new WeakSet<Page>();
  private readonly configuredPages = new WeakSet<Page>();
  private readonly gatewayHealthProbe: GatewayHealthProbe;
  private readonly environment: EnvironmentEvidence | undefined;
  private readonly originBudget: OriginBudgetStore | undefined;
  private readonly mainDocumentResponses = new WeakMap<
    Page,
    MainDocumentResponse
  >();
  private readonly consecutiveAccessDenials = new Map<string, number>();
  private readonly documentTrackerFactory: DocumentTrackerFactory;
  private documentTracker: DocumentGenerationTracker | undefined;
  private classifiedDocumentGeneration: number | undefined;
  private suspectedDocumentGeneration: number | undefined;
  private suspectedSticky = false;
  private readonly controlState: ControlState;
  private mutationRequestSequence = 0;
  private mutationCaptureStart: number | undefined;
  private mutationTrackingOverflow = false;
  private freshObservationRequired = false;
  private readonly trackedMutationRequests = new Map<
    PlaywrightRequest,
    TrackedMutationRequest
  >();

  private constructor(
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: EnvironmentEvidence | undefined,
    originBudget: OriginBudgetStore | undefined,
    documentTrackerFactory: DocumentTrackerFactory,
    controlState: ControlState,
    private readonly nativeChromeGate?: NativeChromeGate,
  ) {
    this.context = context;
    this.page = page;
    this.continuation = continuation;
    this.gatewayHealthProbe = gatewayHealthProbe;
    this.environment = environment;
    this.originBudget = originBudget;
    this.documentTrackerFactory = documentTrackerFactory;
    this.controlState = controlState;
  }

  static async create(environment: NodeJS.ProcessEnv): Promise<BrowserEngine> {
    const browserEnvironment = parseBrowserEnvironment(environment);
    const proxy = requireProxy(environment.OPENLINKER_BROWSER_EGRESS_PROXY);
    const profileDirectory = requireProfileDirectory(
      environment.OPENLINKER_BROWSER_PROFILE_DIR,
    );
    const nativeChromeGate = NativeChromeGate.fromEnvironment(environment);
    const launchOptions: Parameters<typeof chromium.launchPersistentContext>[1] = {
      acceptDownloads: false,
      args:
        nativeChromeGate?.launchArguments(CHROMIUM_FLAGS) ?? [...CHROMIUM_FLAGS],
      headless: nativeChromeGate === undefined,
      // Playwright disables BFCache by default to make request interception
      // deterministic. The Runtime's challenge-release contract needs real
      // BFCache restores, while the Egress Gateway remains the authoritative
      // network boundary for any navigation that does issue a request.
      ignoreDefaultArgs: [
        "--disable-back-forward-cache",
        ...(nativeChromeGate?.ignoredDefaultArguments() ?? []),
      ],
      locale: browserEnvironment.locale,
      proxy: { server: proxy },
      serviceWorkers: "block",
      timezoneId: browserEnvironment.timezone,
      viewport: VIEWPORT,
    };
    if (browserEnvironment.executablePath !== undefined) {
      launchOptions.executablePath = browserEnvironment.executablePath;
    } else {
      launchOptions.channel =
        browserEnvironment.engine === "chrome" ? "chrome" : "chromium";
    }
    const context = await chromium.launchPersistentContext(
      profileDirectory,
      launchOptions,
    );
    const browserVersion = context.browser()?.version();
    if (browserVersion === undefined) {
      await context.close();
      throw new Error("Browser version is unavailable");
    }
    let lockedEnvironment: EnvironmentEvidence;
    try {
      lockedEnvironment = environmentEvidence(
        browserEnvironment,
        browserVersion,
      );
      await nativeChromeGate?.activate(context);
    } catch (error) {
      await context.close().catch(() => undefined);
      throw error;
    }
    const controlState: ControlState = {
      controller: "agent",
      attachmentKey: "",
      pageWebSockets: new Set(),
      interactionPolicy: "restricted",
      mutationOrigins: new Set(),
      mutationBlockSerial: 0,
      blockedMutationRequests: 0,
      actionInFlight: false,
      frameOrigins: () =>
        context
          .pages()
          .flatMap((candidatePage) =>
            candidatePage.frames().map((frame) => frame.url()),
          ),
    };
    await context.route("**/*", async (route) => {
      await routePageRequest(controlState, route.request(), {
        abort: () => route.abort("blockedbyclient"),
        continue: () => route.continue(),
      });
    });
    await context.routeWebSocket("**/*", async (webSocket) => {
      await routePageWebSocket(controlState, webSocket);
    });
    await context.clearPermissions();
    const pages = context
      .pages()
      .filter((candidate) => !nativeChromeGate?.isInternalPage(candidate));
    const page = pages[0] ?? (await context.newPage());
    const originBudget = new OriginBudgetStore({
      profileDirectory,
      profileGeneration: browserEnvironment.profileGeneration,
      maxActionsPerMinute: browserEnvironment.maxActionsPerOriginMinute,
      maxNavigationsPerMinute:
        browserEnvironment.maxNavigationsPerOriginMinute,
    });
    await originBudget.load();
    const engine = new BrowserEngine(
      context,
      page,
      new PageContinuationStore(profileDirectory),
      (timeout) => probeEgressGateway(proxy, timeout),
      lockedEnvironment,
      originBudget,
      (trackedPage) => DocumentGenerationTracker.create(context, trackedPage),
      controlState,
      nativeChromeGate,
    );
    for (const existingPage of pages) {
      engine.configurePage(existingPage);
    }
    context.on("page", (newPage) => {
      engine.configurePage(newPage);
      if (nativeChromeGate?.isInternalPage(newPage)) {
        return;
      }
      const selectModelPage = () => {
        if (nativeChromeGate?.isInternalPage(newPage)) {
          return;
        }
        if (engine.modelPages().length > MAX_PAGES) {
          void newPage.close();
          return;
        }
        engine.page = newPage;
      };
      if (nativeChromeGate !== undefined && newPage.url() === "about:blank") {
        newPage.on("framenavigated", (frame) => {
          if (frame === newPage.mainFrame()) selectModelPage();
        });
        return;
      }
      if (engine.modelPages().length > MAX_PAGES) {
        void newPage.close();
        return;
      }
      engine.page = newPage;
    });
    return engine;
  }

  async execute(request: EngineRequest): Promise<EngineResponse> {
    try {
      const timeout = remainingTimeout(request.deadline);
      await this.nativeChromeGate?.authorize(request);
      await this.bindSession(request, timeout);
      this.admitMutationRecoveryAction(request.action);
      this.refreshSuspectedSticky();
      this.admitOriginBudget(request);
      const mutationSerial = this.controlState.mutationBlockSerial;
      const mutationCaptureStart = this.beginMutationCapture(request);
      this.controlState.actionInFlight = true;
      let effect: ActionEffect | undefined;
      try {
        effect = await this.executeAction(request.action, timeout, request);
        if (this.controlState.mutationBlockSerial !== mutationSerial) {
          throw this.mutationOriginBlocked(request, true);
        }
        await this.awaitMutationOutcomes(
          mutationCaptureStart,
          request.deadline,
        );
      } catch (error) {
        if (
          error instanceof EngineActionError &&
          mutationCaptureStart !== undefined &&
          this.mutationRequestSequence > mutationCaptureStart
        ) {
          error = new EngineActionError(
            error.code,
            error.message,
            error.recoverable,
            error.actionIndex,
            {
              ...error.details,
              mutation_requests_observed: Math.min(
                MAX_TRACKED_MUTATION_REQUESTS_PER_ACTION,
                this.mutationRequestSequence - mutationCaptureStart,
              ),
            },
          );
        }
        if (
          this.controlState.mutationBlockSerial !== mutationSerial &&
          !(
            error instanceof EngineActionError &&
            error.code === "BROWSER_MUTATION_ORIGIN_BLOCKED"
          )
        ) {
          throw this.mutationOriginBlocked(request, true);
        }
        throw error;
      } finally {
        this.controlState.actionInFlight = false;
        this.endMutationCapture(mutationCaptureStart);
      }
      if (this.controlState.mutationBlockSerial !== mutationSerial) {
        throw this.mutationOriginBlocked(request, true);
      }
      const mutationRequestsObserved =
        mutationCaptureStart === undefined
          ? 0
          : Math.min(
              MAX_TRACKED_MUTATION_REQUESTS_PER_ACTION,
              this.mutationRequestSequence - mutationCaptureStart,
            );
      this.assertGatewayAllowed(this.page);
      assertAllowedPageURL(this.page.url());
      await this.classifyActionOutcome(request);
      const observation = await this.observe(
        request.action.observation ?? "semantic",
        timeout,
      );
      if (request.action.kind === "preflight") {
        if (this.environment !== undefined) {
          observation.environment = this.environment;
        }
      }
      if (effect !== undefined) {
        observation.click_effect = effect.clickEffect;
        observation.target_category = effect.targetCategory;
      }
      if (this.suspectedSticky) {
        observation.site_outcome = "BROWSER_CHALLENGE_SUSPECTED";
        observation.classifier_rules_version = CLASSIFIER_RULES_VERSION;
        observation.challenge_release_unavailable = true;
      }
      if (this.controlState.blockedMutationRequests > 0) {
        observation.blocked_mutation_requests = Math.min(
          1024,
          this.controlState.blockedMutationRequests,
        );
        this.controlState.blockedMutationRequests = 0;
      }
      if (mutationRequestsObserved > 0) {
        observation.mutation_requests_observed = mutationRequestsObserved;
      }
      this.assertGatewayAllowed(this.page);
      assertAllowedPageURL(this.page.url());
      if (request.action.kind !== "preflight") {
        await this.continuation.record(
          request.identity,
          this.page.url(),
          Date.now(),
          request.action.kind === "checkpoint",
        );
      }
      if (request.action.kind === "checkpoint") {
        await this.originBudget?.checkpoint();
      }
      if (request.action.kind === "screenshot") {
        this.freshObservationRequired = false;
      }
      return success(request.action_id, observation);
    } catch (error) {
      if (error instanceof PageContinuationCorruptError) {
        return failure(
          request.action_id,
          "BROWSER_PROFILE_CORRUPT",
          "Browser Profile page state is invalid",
          false,
        );
      }
      if (error instanceof EngineActionError) {
        if (error.code === "BROWSER_MUTATION_OUTCOME_UNKNOWN") {
          this.freshObservationRequired = true;
        }
        this.refreshSuspectedSticky();
        return failure(
          request.action_id,
          error.code,
          error.message,
          error.recoverable,
          error.actionIndex,
          this.suspectedSticky
            ? {
                ...error.details,
                challenge_release_unavailable: true,
              }
            : error.details,
        );
      }
      const message = error instanceof Error ? error.message : "browser action failed";
      if (Date.parse(request.deadline) <= Date.now()) {
        return failure(
          request.action_id,
          "BROWSER_CANCELED",
          "browser action deadline elapsed",
          true,
        );
      }
      const tunnelFailure = await this.classifyTunnelFailure(
        error,
        Date.parse(request.deadline) - Date.now(),
      );
      if (tunnelFailure !== undefined) {
        return failure(
          request.action_id,
          tunnelFailure.code,
          tunnelFailure.message,
          tunnelFailure.recoverable,
        );
      }
      return failure(
        request.action_id,
        request.action.kind === "navigate"
          ? "BROWSER_EGRESS_UNAVAILABLE"
          : "BROWSER_RUNTIME_UNAVAILABLE",
        safeRuntimeMessage(message),
        true,
      );
    }
  }

  async executeViewer(
    request: EngineViewerRequest,
  ): Promise<EngineViewerResponse> {
    try {
      const timeout = remainingTimeout(request.deadline);
    await this.nativeChromeGate?.authorizeViewer(request);
      const sessionKey = pageContinuationSessionKey(request.identity);
      const attachmentKey = viewerAttachmentKey(request.identity);
      if (request.operation === "enter") {
        if (
          this.boundSessionKey === "" ||
          this.boundSessionKey !== sessionKey ||
          this.controlState.controller === "human"
        ) {
          return viewerFailure(
            request.action_id,
            "BROWSER_ACTION_REJECTED",
            "browser viewer cannot claim this Session",
            false,
          );
        }
        this.boundAttachmentKey = attachmentKey;
        this.boundRunID = request.identity.run_id;
        this.controlState.attachmentKey = attachmentKey;
        this.controlState.controller = "human";
        return viewerSuccess(request.action_id);
      }
      if (
        this.controlState.controller !== "human" ||
        this.controlState.attachmentKey !== attachmentKey ||
        this.boundSessionKey !== sessionKey
      ) {
        return viewerFailure(
          request.action_id,
          "BROWSER_ACTION_REJECTED",
          "browser viewer control is stale",
          false,
        );
      }
      if (request.operation === "exit") {
        // Reinstate the restrictive page policy before acknowledging release.
        await restoreAgentPagePolicy(this.controlState);
        return viewerSuccess(request.action_id);
      }
      if (request.operation === "frame") {
        const frame = await this.page.screenshot({
          animations: "disabled",
          caret: "initial",
          quality: 55,
          timeout,
          type: "jpeg",
        });
        if (frame.byteLength > MAX_VIEWER_FRAME_BYTES) {
          return viewerFailure(
            request.action_id,
            "BROWSER_OUTPUT_TOO_LARGE",
            "browser viewer frame exceeds the output limit",
            false,
          );
        }
        return viewerSuccess(request.action_id, {
          mime_type: "image/jpeg",
          data: frame.toString("base64"),
          width: BROWSER_VIEWPORT_WIDTH,
          height: BROWSER_VIEWPORT_HEIGHT,
        });
      }
      const input = request.input;
      if (input === undefined) {
        throw new Error("browser viewer input is missing");
      }
      switch (input.kind) {
        case "pointer":
          if (input.pointer_action === "move") {
            await this.page.mouse.move(input.x, input.y);
          } else {
            await this.page.mouse.click(input.x, input.y, {
              button: input.button ?? "left",
              clickCount: input.click_count ?? 1,
            });
          }
          break;
        case "keyboard":
          if (input.keyboard_action === "press") {
            await this.page.keyboard.press(input.key ?? "");
          } else {
            await this.page.keyboard.insertText(input.text ?? "");
          }
          break;
        case "scroll":
          await this.page.mouse.wheel(input.delta_x, input.delta_y);
          break;
      }
      return viewerSuccess(request.action_id);
    } catch (error) {
      return viewerFailure(
        request.action_id,
        Date.parse(request.deadline) <= Date.now()
          ? "BROWSER_CANCELED"
          : "BROWSER_RUNTIME_UNAVAILABLE",
        Date.parse(request.deadline) <= Date.now()
          ? "browser viewer deadline elapsed"
          : safeRuntimeMessage(
              error instanceof Error
                ? error.message
                : "browser viewer operation failed",
            ),
        true,
      );
    }
  }

  async executeOpsObserver(
    request: EngineOpsObserverRequest,
  ): Promise<EngineOpsObserverResponse> {
    const sessionKey = pageContinuationSessionKey(request.identity);
    const attachmentKey =
      request.identity.controller === "human"
        ? viewerAttachmentKey(request.identity)
        : agentAttachmentKey(request.identity);
    if (
      request.identity.controller === "none" ||
      this.boundRunID !== request.identity.run_id ||
      this.boundSessionKey === "" ||
      this.boundSessionKey !== sessionKey ||
      this.boundAttachmentKey !== attachmentKey ||
      this.controlState.controller !== request.identity.controller
    ) {
      return opsObserverFailure(
        request.action_id,
        "RUN_NOT_ACTIVE",
        "requested Run is not active in this Browser Engine",
      );
    }
    try {
      const timeout = Math.min(500, remainingTimeout(request.deadline));
      await withOpsObserverTimeout(
        this.nativeChromeGate?.authorizeObserver(request) ?? Promise.resolve(),
        timeout,
      );
      const pageURL = opsObserverPageURL(this.page.url());
      // Agent actions already obtain the page title while settling their
      // observation. Reuse that bounded value here: asking Playwright for a
      // second title concurrently with an in-flight action can fail fast with
      // TimeoutError and make every Ops observation look busy before the JPEG
      // capture is even attempted.
      const pageTitle = truncateUTF8(this.lastSettledPageTitle, 512);
      if (request.operation === "observe_status") {
        return opsObserverSuccess(request.action_id, pageURL, pageTitle);
      }
      const frame = await this.page.screenshot({
        animations: "disabled",
        caret: "initial",
        quality: 55,
        timeout: Math.max(1, Math.min(timeout, remainingTimeout(request.deadline))),
        type: "jpeg",
      });
      if (frame.byteLength > MAX_VIEWER_FRAME_BYTES) {
        throw new Error("Ops Observer frame exceeds the output limit");
      }
      return opsObserverSuccess(request.action_id, pageURL, pageTitle, {
        mime_type: "image/jpeg",
        data: frame.toString("base64"),
        width: BROWSER_VIEWPORT_WIDTH,
        height: BROWSER_VIEWPORT_HEIGHT,
      });
    } catch (error) {
      const busy =
        Date.parse(request.deadline) <= Date.now() ||
        error instanceof playwrightErrors.TimeoutError ||
        (error instanceof Error &&
          error.message === "Ops Observer capture timed out");
      return opsObserverFailure(
        request.action_id,
        busy ? "OPS_VIEWER_BUSY" : "OPS_VIEWER_INTERNAL",
        busy
          ? "Browser Engine is busy"
          : "Browser Engine could not capture the read-only observation",
      );
    }
  }

  private admitOriginBudget(request: EngineRequest): void {
    if (
      request.action.kind === "preflight" ||
      request.action.kind === "checkpoint"
    ) {
      return;
    }
    const origin =
      request.action.kind === "navigate"
        ? safeOrigin(request.action.url ?? "")
        : safeOrigin(this.page.url());
    if (origin === "") {
      return;
    }
    if (this.originBudget !== undefined) {
      if (
        request.action.kind === "batch" &&
        request.action.actions?.some(isTraversalAction)
      ) {
        // A traversing batch changes its effective origin while it runs. Each
        // nested action is charged immediately before dispatch instead of
        // attributing the entire batch to a stale or empty starting origin.
        return;
      }
      const actionCount =
        request.action.kind === "batch"
          ? (request.action.actions?.length ?? 1)
          : 1;
      const actionDelay = this.originBudget.chargeAction(
        request.identity,
        origin,
        actionCount,
      );
      if (actionDelay !== undefined) {
        throw originRateLimited(actionDelay);
      }
      const retryDelay = this.originBudget.retryDelay(request.identity, origin);
      if (retryDelay !== undefined) {
        throw originRateLimited(retryDelay);
      }
    }
    if (
      request.action.kind !== "navigate" &&
      request.action.kind !== "back" &&
      request.action.kind !== "forward"
    ) {
      return;
    }
    this.admitNavigation(
      request,
      origin,
      request.action.kind === "navigate",
    );
  }

  private admitMutationRecoveryAction(action: BrowserAction): void {
    if (
      !this.freshObservationRequired ||
      action.kind === "screenshot" ||
      action.kind === "checkpoint"
    ) {
      return;
    }
    throw new EngineActionError(
      "BROWSER_ACTION_REJECTED",
      "A fresh Browser observation is required after an uncertain mutation",
      false,
    );
  }

  private admitNavigation(
    request: EngineRequest,
    origin: string,
    enforceAccessDenial: boolean,
  ): void {
    if (origin === "") {
      return;
    }
    if (enforceAccessDenial) {
      const denials = this.consecutiveAccessDenials.get(
        accessDenialKey(request, origin),
      );
      if (denials !== undefined && denials >= 3) {
        throw accessDenied(3, true);
      }
    }
    const retryDelay = this.originBudget?.retryDelay(
      request.identity,
      origin,
    );
    if (retryDelay !== undefined) {
      throw originRateLimited(retryDelay);
    }
    const navigationDelay = this.originBudget?.chargeNavigation(
      request.identity,
      origin,
    );
    if (navigationDelay !== undefined) {
      throw originRateLimited(navigationDelay);
    }
  }

  private admitNestedOriginBudget(
    request: EngineRequest,
    action: BrowserAction,
    traversingBatch: boolean,
  ): void {
    const origin =
      action.kind === "navigate"
        ? safeOrigin(action.url ?? "")
        : safeOrigin(this.page.url());
    if (origin === "") {
      return;
    }
    if (traversingBatch && this.originBudget !== undefined) {
      const actionDelay = this.originBudget.chargeAction(
        request.identity,
        origin,
      );
      if (actionDelay !== undefined) {
        throw originRateLimited(actionDelay);
      }
      const retryDelay = this.originBudget.retryDelay(
        request.identity,
        origin,
      );
      if (retryDelay !== undefined) {
        throw originRateLimited(retryDelay);
      }
    }
    if (!isTraversalAction(action)) {
      return;
    }
    // Each nested traversal also consumes the independent navigation window.
    // Non-traversing batches were charged atomically at admission; traversing
    // batches were charged per nested effective origin above.
    this.admitNavigation(request, origin, action.kind === "navigate");
  }

  private async classifyActionOutcome(request: EngineRequest): Promise<void> {
    if (
      this.suspectedDocumentGeneration !== undefined &&
      this.documentTracker?.healthy === false
    ) {
      this.suspectedSticky = true;
    }
    const response = this.mainDocumentResponses.get(this.page);
    const currentDocumentGeneration = this.documentTracker?.generation;
    if (
      response === undefined &&
      (currentDocumentGeneration === undefined ||
        currentDocumentGeneration === this.classifiedDocumentGeneration)
    ) {
      return;
    }
    if (response !== undefined) {
      this.mainDocumentResponses.delete(this.page);
    }
    const challenge = await classifyChallenge(this.page);
    this.classifiedDocumentGeneration = currentDocumentGeneration;
    if (challenge === "required") {
      throw new EngineActionError(
        "BROWSER_CHALLENGE_REQUIRED",
        "Interactive website challenge requires human control",
        false,
        undefined,
        {
          site_outcome: "BROWSER_CHALLENGE_REQUIRED",
          classifier_rules_version: CLASSIFIER_RULES_VERSION,
        },
      );
    }
    if (challenge === "suspected") {
      this.suspectedDocumentGeneration =
        this.documentTracker?.generation ?? 1;
      this.suspectedSticky ||= this.documentTracker?.healthy === false;
      throw this.suspectedChallengeError();
    }
    if (this.suspectedDocumentGeneration !== undefined) {
      if (this.documentTracker?.healthy === false) {
        this.suspectedSticky = true;
      } else if (
        !this.suspectedSticky &&
        this.documentTracker !== undefined &&
        this.documentTracker.generation > this.suspectedDocumentGeneration
      ) {
        this.suspectedDocumentGeneration = undefined;
      }
    }
    if (response === undefined || response.origin === "") {
      return;
    }
    const denialKey = accessDenialKey(request, response.origin);
    if (response.status === 403) {
      const denials = Math.min(
        3,
        (this.consecutiveAccessDenials.get(denialKey) ?? 0) + 1,
      );
      this.consecutiveAccessDenials.set(denialKey, denials);
      throw accessDenied(denials, denials === 3);
    }
    this.consecutiveAccessDenials.delete(denialKey);
    if (response.status !== 429) {
      return;
    }
    const retryAfter = parseRetryAfter(response.retryAfter, Date.now());
    if (retryAfter !== undefined) {
      this.originBudget?.setRetryAfter(
        request.identity,
        response.origin,
        retryAfter,
      );
    }
    throw new EngineActionError(
      "BROWSER_RATE_LIMITED",
      "Website rate limit was reached",
      true,
      undefined,
      {
        site_outcome: "BROWSER_RATE_LIMITED",
        ...(retryAfter === undefined ? {} : { retry_after_ms: retryAfter }),
      },
    );
  }

  private suspectedChallengeError(): EngineActionError {
    return new EngineActionError(
      "BROWSER_CHALLENGE_SUSPECTED",
      "Interactive website challenge may be present",
      true,
      undefined,
      {
        site_outcome: "BROWSER_CHALLENGE_SUSPECTED",
        classifier_rules_version: CLASSIFIER_RULES_VERSION,
        ...(this.suspectedSticky
          ? { challenge_release_unavailable: true }
          : {}),
      },
    );
  }

  private refreshSuspectedSticky(): void {
    if (
      this.suspectedDocumentGeneration !== undefined &&
      this.documentTracker?.healthy === false
    ) {
      this.suspectedSticky = true;
    }
  }

  private denySuspectedMutation(): void {
    if (this.suspectedDocumentGeneration !== undefined) {
      if (this.documentTracker?.healthy === false) {
        this.suspectedSticky = true;
      }
      throw this.suspectedChallengeError();
    }
  }

  private mutationOriginBlocked(
    request: EngineRequest,
    freshObservationRequired: boolean,
    observedOrigin = canonicalDocumentOrigin(this.page.mainFrame()),
  ): EngineActionError {
    return new EngineActionError(
      "BROWSER_MUTATION_ORIGIN_BLOCKED",
      "Browser mutation origin is outside the Owner-authorized scope",
      false,
      undefined,
      {
        retry_same_action: false,
        attachment_usable: true,
        fresh_observation_required: freshObservationRequired,
        ...(observedOrigin === "" ? {} : { observed_origin: observedOrigin }),
        browser_mutation_origins: [
          ...request.identity.browser_mutation_origins,
        ],
      },
    );
  }

  private requireMutationOrigin(
    request: EngineRequest,
    origin: string,
  ): void {
    if (
      request.identity.browser_interaction_policy !== "full" ||
      origin === "" ||
      !request.identity.browser_mutation_origins.includes(origin)
    ) {
      throw this.mutationOriginBlocked(request, false, origin);
    }
  }

  private async establishDocumentTracker(page: Page): Promise<void> {
    await this.documentTracker?.detach();
    this.documentTracker = undefined;
    this.classifiedDocumentGeneration = undefined;
    try {
      if (typeof this.documentTrackerFactory !== "function") {
        throw new Error("document tracker factory is missing");
      }
      this.documentTracker = await this.documentTrackerFactory(page);
    } catch {
      throw new EngineActionError(
        "BROWSER_RUNTIME_UNAVAILABLE",
        "Browser document identity tracking is unavailable",
        false,
      );
    }
  }

  async close(): Promise<void> {
    await this.context.close();
  }

  private configurePage(page: Page): void {
    if (this.configuredPages.has(page)) {
      return;
    }
    this.configuredPages.add(page);
    page.on("dialog", (dialog) => {
      void dialog.dismiss();
    });
    page.on("download", (download) => {
      void download.cancel();
    });
    page.on("request", (request) => {
      this.trackMutationRequest(request);
    });
    page.on("requestfinished", (request) => {
      this.finishMutationRequest(request, "finished");
    });
    page.on("requestfailed", (request) => {
      this.finishMutationRequest(request, "failed");
    });
    page.on("response", (response) => {
      if (response.status() >= 500) {
        this.failMutationRequest(response.request());
      } else {
        // Receiving a non-server-error response establishes the transport
        // outcome. Do not wait for requestfinished: that event also waits for
        // the response body to drain and may be delayed by an intermediary
        // after the server has already acknowledged the mutation.
        this.finishMutationRequest(response.request(), "finished");
      }
      if (
        response.request().isNavigationRequest() &&
        response.frame() === page.mainFrame()
      ) {
        const headers = response.headers();
        if (gatewayBlockedResponse(headers)) {
          this.blockedNavigationPages.add(page);
        }
        this.mainDocumentResponses.set(page, {
          status: response.status(),
          origin: safeOrigin(response.url()),
          ...(headers["retry-after"] === undefined
            ? {}
            : { retryAfter: headers["retry-after"] }),
        });
      }
    });
    page.on("framenavigated", (frame) => {
      if (page === this.page && frame === page.mainFrame()) {
        this.navigationGeneration++;
      }
    });
  }

  private beginMutationCapture(request: EngineRequest): number | undefined {
    if (request.identity.browser_interaction_policy !== "full") {
      return undefined;
    }
    const start = this.mutationRequestSequence;
    this.mutationCaptureStart = start;
    this.mutationTrackingOverflow = false;
    return start;
  }

  private trackMutationRequest(request: PlaywrightRequest): void {
    if (
      !this.controlState.actionInFlight ||
      this.controlState.interactionPolicy !== "full" ||
      this.mutationCaptureStart === undefined ||
      !["POST", "PUT", "PATCH", "DELETE"].includes(
        request.method().toUpperCase(),
      )
    ) {
      return;
    }
    const sequence = ++this.mutationRequestSequence;
    if (
      sequence - this.mutationCaptureStart >
      MAX_TRACKED_MUTATION_REQUESTS_PER_ACTION
    ) {
      this.mutationTrackingOverflow = true;
      return;
    }
    let resolve: () => void = () => {};
    const settled = new Promise<void>((done) => {
      resolve = done;
    });
    this.trackedMutationRequests.set(request, {
      sequence,
      outcome: "pending",
      settled,
      resolve,
    });
  }

  private finishMutationRequest(
    request: PlaywrightRequest,
    outcome: "finished" | "failed",
  ): void {
    const tracked = this.trackedMutationRequests.get(request);
    if (tracked === undefined || tracked.outcome !== "pending") {
      return;
    }
    tracked.outcome = outcome;
    tracked.resolve();
  }

  private failMutationRequest(request: PlaywrightRequest): void {
    this.finishMutationRequest(request, "failed");
  }

  private async awaitMutationOutcomes(
    captureStart: number | undefined,
    deadline: string,
  ): Promise<void> {
    if (captureStart === undefined) {
      return;
    }
    if (this.mutationTrackingOverflow) {
      throw mutationOutcomeUnknown("page_mutation_tracking_overflow");
    }
    const tracked = [...this.trackedMutationRequests.values()].filter(
      (request) => request.sequence > captureStart,
    );
    if (tracked.length === 0) {
      return;
    }
    const settleDeadline = Math.min(
      Date.parse(deadline),
      Date.now() + MUTATION_RESPONSE_SETTLE_MS,
    );
    while (tracked.some((request) => request.outcome === "pending")) {
      const remaining = settleDeadline - Date.now();
      if (remaining <= 0) {
        throw mutationOutcomeUnknown("page_mutation_response_unobserved");
      }
      await Promise.race([
        ...tracked
          .filter((request) => request.outcome === "pending")
          .map((request) => request.settled),
        delay(Math.min(remaining, 25)),
      ]);
      if (this.mutationTrackingOverflow) {
        throw mutationOutcomeUnknown("page_mutation_tracking_overflow");
      }
    }
    if (tracked.some((request) => request.outcome === "failed")) {
      throw mutationOutcomeUnknown("page_mutation_request_failed");
    }
  }

  private endMutationCapture(captureStart: number | undefined): void {
    if (captureStart === undefined) {
      return;
    }
    this.discardTrackedMutationRequestsAfter(captureStart);
    if (this.mutationCaptureStart === captureStart) {
      this.mutationCaptureStart = undefined;
      this.mutationTrackingOverflow = false;
    }
  }

  private discardTrackedMutationRequestsAfter(captureStart: number): void {
    for (const [request, tracked] of this.trackedMutationRequests) {
      if (tracked.sequence > captureStart) {
        this.trackedMutationRequests.delete(request);
      }
    }
  }

  private assertGatewayAllowed(page: Page): void {
    if (!this.blockedNavigationPages.has(page)) {
      return;
    }
    this.blockedNavigationPages.delete(page);
    throw new EngineActionError(
      "BROWSER_TARGET_BLOCKED",
      "Browser egress gateway blocked the navigation target",
      false,
    );
  }

  private async bindSession(
    request: EngineRequest,
    timeout: number,
  ): Promise<void> {
    const sessionKey = pageContinuationSessionKey(request.identity);
    const attachmentKey = agentAttachmentKey(request.identity);
    this.controlState.interactionPolicy =
      request.identity.browser_interaction_policy;
    this.controlState.mutationOrigins = new Set(
      request.identity.browser_mutation_origins,
    );
    this.controlState.frameOrigins = () =>
      this.page.frames().map((frame) => frame.url());
    if (
      this.boundSessionKey === sessionKey &&
      this.boundAttachmentKey === attachmentKey
    ) {
      this.boundRunID = request.identity.run_id;
      return;
    }
    if (this.boundSessionKey === sessionKey) {
      this.suspectedDocumentGeneration = undefined;
      this.suspectedSticky = false;
      await this.establishDocumentTracker(this.page);
      this.boundAttachmentKey = attachmentKey;
      this.boundRunID = request.identity.run_id;
      return;
    }
    this.freshObservationRequired = false;
    this.boundSessionKey = "";
    this.boundAttachmentKey = "";
    this.boundRunID = "";
    const pages = this.modelPages();
    for (const extra of pages.slice(1)) {
      await extra.close();
    }
    const page = await this.context.newPage();
    this.configurePage(page);
    this.page = page;
    if (pages[0] !== undefined) {
      await pages[0].close();
    }
    this.lastBodyText = "";
    this.lastSettledPageTitle = "";
    this.blockedNavigationPages.delete(page);
    page.setDefaultTimeout(timeout);
    page.setDefaultNavigationTimeout(timeout);
    await page.goto("about:blank", {
      timeout,
      waitUntil: "domcontentloaded",
    });
    await this.establishDocumentTracker(page);
    this.suspectedDocumentGeneration = undefined;
    this.suspectedSticky = false;
    this.boundAttachmentKey = attachmentKey;
    this.boundRunID = request.identity.run_id;
    if (request.action.kind === "navigate") {
      this.boundSessionKey = sessionKey;
      return;
    }
    if (request.action.kind === "preflight") {
      // Preflight proves that the isolated engine can start without making a
      // saved public page part of readiness. It must not bind the Session:
      // the first real action still needs to restore that continuation.
      return;
    }
    const savedURL = await this.continuation.lookup(request.identity);
    if (savedURL !== undefined) {
      try {
        await page.goto(savedURL, {
          timeout,
          waitUntil: "domcontentloaded",
        });
      } catch (error) {
        const tunnelFailure = await this.classifyTunnelFailure(error, timeout);
        if (tunnelFailure !== undefined) {
          throw tunnelFailure;
        }
        throw new EngineActionError(
          "BROWSER_EGRESS_UNAVAILABLE",
          "saved Browser page could not be restored through the egress gateway",
          true,
        );
      }
    }
    this.boundSessionKey = sessionKey;
  }

  private modelPages(): Page[] {
    return this.context
      .pages()
      .filter(
        (page) =>
          !page.isClosed() && !this.nativeChromeGate?.isInternalPage(page),
      );
  }

  private async classifyTunnelFailure(
    error: unknown,
    _timeout: number,
  ): Promise<EngineActionError | undefined> {
    if (!isTunnelConnectionFailure(error)) {
      return undefined;
    }
    // Chromium does not expose the CONNECT response headers to the page. A
    // healthy Gateway therefore cannot distinguish a policy denial from a
    // public DNS/dial/upstream failure. Unknown tunnel failures must stay
    // recoverable instead of being guessed into a permanent security block.
    return new EngineActionError(
      "BROWSER_EGRESS_UNAVAILABLE",
      "Browser HTTPS tunnel is unavailable",
      true,
    );
  }

  private async executeAction(
    action: BrowserAction,
    timeout: number,
    request: EngineRequest,
  ): Promise<ActionEffect | undefined> {
    const page = this.page;
    page.setDefaultTimeout(timeout);
    page.setDefaultNavigationTimeout(timeout);
    switch (action.kind) {
      case "navigate":
        this.blockedNavigationPages.delete(page);
        await page.goto(action.url ?? "", {
          timeout,
          waitUntil: "domcontentloaded",
        });
        return;
      case "click": {
        const x = action.x ?? -1;
        const y = action.y ?? -1;
        assertViewportPoint(x, y);
        const target = await interactiveHandleAt(page, x, y);
        try {
          if (target.handle !== undefined && !blocksNonSecretTyping(target.metadata)) {
            this.denySuspectedMutation();
            await target.handle.focus();
            return {
              clickEffect: "focused",
              targetCategory: "text_input",
            };
          }
          const full = request.identity.browser_interaction_policy === "full";
          const topOrigin = canonicalDocumentOrigin(page.mainFrame());
          const targetOrigin = target.origin;
          if (
            target.handle === undefined ||
            (!full && blocksHighImpactActivation(target.metadata))
          ) {
            const blocked = await this.observe("semantic", timeout);
            throw new EngineActionError(
              "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
              "Phase 1 blocks this high-impact browser action",
              false,
              undefined,
              {
                target_category: targetCategory(target.metadata),
                page_state_id: blocked.page_state_id,
                navigation_generation: blocked.navigation_generation,
              },
            );
          }
          if (
            full &&
            (topOrigin === "" || targetOrigin === "" ||
              !request.identity.browser_mutation_origins.includes(topOrigin) ||
              !request.identity.browser_mutation_origins.includes(targetOrigin))
          ) {
            if (blocksHighImpactActivation(target.metadata)) {
              throw this.mutationOriginBlocked(request, false, targetOrigin);
            }
          }
          this.denySuspectedMutation();
          this.admitNavigation(
            request,
            safeOrigin(target.metadata.href),
            true,
          );
          if (full) {
            await target.handle.click({ timeout, trial: true });
            try {
              await this.actAndAwaitNavigation(
                () => target.handle!.click({ timeout }),
                timeout,
              );
            } catch {
              throw mutationOutcomeUnknown("click_dispatch_uncertain");
            }
          } else {
            await this.actAndAwaitNavigation(
              () => target.handle!.click({ timeout }),
              timeout,
            );
          }
          return {
            clickEffect: "activated",
            targetCategory: activationTargetCategory(target.metadata),
          };
        } finally {
          await target.handle?.dispose();
        }
      }
      case "type_non_secret": {
        const target = await activeInteractiveHandle(page);
        try {
          if (target.handle === undefined) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "active text control is no longer actionable",
              false,
            );
          }
          const full = request.identity.browser_interaction_policy === "full";
          if (full) {
            this.requireMutationOrigin(
              request,
              canonicalDocumentOrigin(page.mainFrame()),
            );
            this.requireMutationOrigin(request, target.origin);
          }
          if (blocksNonSecretTyping(target.metadata)) {
            throw new EngineActionError(
              "BROWSER_USER_ACTION_REQUIRED",
              "credential fields require a later human-control phase",
              false,
            );
          }
          this.denySuspectedMutation();
          const current = await target.handle.evaluate((element) => {
            if (
              !(element instanceof HTMLInputElement) &&
              !(element instanceof HTMLTextAreaElement)
            ) {
              return undefined;
            }
            return {
              value: element.value,
              selectionStart: element.selectionStart,
              selectionEnd: element.selectionEnd,
            };
          });
          if (current === undefined) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "active text control is no longer actionable",
              false,
            );
          }
          const start = current.selectionStart ?? current.value.length;
          const end = current.selectionEnd ?? start;
          const result =
            current.value.slice(0, start) +
            (action.text ?? "") +
            current.value.slice(end);
          if (
            Buffer.byteLength(current.value, "utf8") > MAX_TYPE_VALUE_BYTES ||
            Buffer.byteLength(result, "utf8") > MAX_TYPE_VALUE_BYTES
          ) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "text control value exceeds the Phase 1 limit",
              false,
            );
          }
          if (!full) {
            await target.handle.fill(result, { timeout });
            return;
          }
          if (Buffer.byteLength(action.text ?? "", "utf8") > 2 * 1024) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "sequential text exceeds the full-policy input limit",
              false,
            );
          }
          await sequentialTypeIntoPinnedHandle(
            page,
            target.handle,
            action.text ?? "",
          );
          return;
        } finally {
          await target.handle?.dispose();
        }
      }
      case "scroll":
        await page.mouse.wheel(action.delta_x ?? 0, action.delta_y ?? 0);
        return;
      case "keypress":
        {
          const target = await activeInteractiveHandle(page);
          try {
            const blockedByRestricted = blocksKeypress(
              target.metadata,
              action.key ?? "",
            );
            const full = request.identity.browser_interaction_policy === "full";
            if (!full && blockedByRestricted) {
              throw new EngineActionError(
                "BROWSER_HIGH_IMPACT_ACTION_BLOCKED",
                "Phase 1 blocks this browser key action",
                false,
              );
            }
            if (full && blockedByRestricted) {
              this.requireMutationOrigin(
                request,
                canonicalDocumentOrigin(page.mainFrame()),
              );
              this.requireMutationOrigin(request, target.origin);
            }
            this.denySuspectedMutation();
            if (action.key === "Enter") {
              this.admitNavigation(
                request,
                safeOrigin(target.metadata.formAction),
                true,
              );
            }
            if (full && blockedByRestricted && target.handle !== undefined) {
              if (!(await pinnedHandleOwnsFocus(target.handle))) {
                throw new EngineActionError(
                  "BROWSER_ACTION_REJECTED",
                  "active control changed before key dispatch",
                  false,
                );
              }
              try {
                await this.actAndAwaitNavigation(
                  () => page.keyboard.press(action.key ?? ""),
                  timeout,
                );
              } catch {
                throw mutationOutcomeUnknown("keypress_dispatch_uncertain");
              }
              if (!(await pinnedHandleOwnsFocus(target.handle))) {
                throw mutationOutcomeUnknown(
                  "keypress_target_changed_after_dispatch",
                );
              }
              return;
            }
          } finally {
            await target.handle?.dispose();
          }
        }
        await this.actAndAwaitNavigation(
          () => page.keyboard.press(action.key ?? ""),
          timeout,
        );
        return;
      case "select": {
        if (request.identity.browser_interaction_policy !== "full") {
            throw new EngineActionError(
              "BROWSER_USER_ACTION_REQUIRED",
              "Phase 1 does not allow select controls",
              false,
            );
        }
        const target = await activeInteractiveHandle(page);
        try {
          if (
            target.handle === undefined ||
            target.metadata.tagName.toLowerCase() !== "select"
          ) {
            throw new EngineActionError(
              "BROWSER_ACTION_REJECTED",
              "active control is not a supported select",
              false,
            );
          }
          this.requireMutationOrigin(
            request,
            canonicalDocumentOrigin(page.mainFrame()),
          );
          this.requireMutationOrigin(request, target.origin);
          this.denySuspectedMutation();
          try {
            await target.handle.selectOption(action.value ?? "", { timeout });
          } catch {
            throw mutationOutcomeUnknown("select_dispatch_uncertain");
          }
          return;
        } finally {
          await target.handle?.dispose();
        }
      }
      case "wait":
        await page.waitForTimeout(action.duration_ms ?? 1);
        return;
      case "back":
        await this.traverseHistory(page, "back", timeout);
        return;
      case "forward":
        await this.traverseHistory(page, "forward", timeout);
        return;
      case "screenshot":
      case "checkpoint":
        return;
      case "preflight":
        if (!(await this.gatewayHealthProbe(Math.min(timeout, 1_000)))) {
          throw new EngineActionError(
            "BROWSER_EGRESS_UNAVAILABLE",
            "Browser egress gateway preflight failed",
            true,
          );
        }
        await page.goto("about:blank", {
          timeout,
          waitUntil: "domcontentloaded",
        });
        return;
      case "batch": {
        const traversingBatch = (action.actions ?? []).some(
          isTraversalAction,
        );
        for (const [index, nested] of (action.actions ?? []).entries()) {
          const mutationSerial = this.controlState.mutationBlockSerial;
          const nestedMutationStart = this.mutationRequestSequence;
          try {
            this.admitNestedOriginBudget(request, nested, traversingBatch);
            await this.executeAction(nested, timeout, request);
            if (this.controlState.mutationBlockSerial !== mutationSerial) {
              const blocked = this.mutationOriginBlocked(request, true);
              throw new EngineActionError(
                blocked.code,
                blocked.message,
                blocked.recoverable,
                index,
                { ...blocked.details, completed_actions: index },
              );
            }
            await this.awaitMutationOutcomes(
              nestedMutationStart,
              request.deadline,
            );
            // A batch must not defer navigation/challenge classification until
            // its final action. Otherwise a 403, 429, or challenge reached by
            // one nested action could still be followed by later dispatches.
            await this.classifyActionOutcome(request);
          } catch (error) {
            if (error instanceof EngineActionError) {
              throw new EngineActionError(
                error.code,
                error.message,
                error.recoverable,
                index,
                { ...error.details, completed_actions: index },
              );
            }
            throw new EngineActionError(
              "BROWSER_RUNTIME_UNAVAILABLE",
              safeRuntimeMessage(
                error instanceof Error ? error.message : "browser batch action failed",
              ),
              true,
              index,
              { completed_actions: index },
            );
          } finally {
            this.discardTrackedMutationRequestsAfter(nestedMutationStart);
          }
        }
        return;
      }
    }
  }

  private async traverseHistory(
    page: Page,
    direction: "back" | "forward",
    timeout: number,
  ): Promise<void> {
    const startedAt = Date.now();
    if (direction === "back") {
      await page.goBack({ timeout, waitUntil: "commit" });
    } else {
      await page.goForward({ timeout, waitUntil: "commit" });
    }
    const remaining = Math.max(1, timeout - (Date.now() - startedAt));
    await page.waitForFunction(
      () => document.readyState !== "loading",
      undefined,
      { timeout: remaining },
    );
  }

  private async actAndAwaitNavigation(
    action: () => Promise<void>,
    timeout: number,
  ): Promise<void> {
    const page = this.page;
    const mainFrame = page.mainFrame();
    const navigationSettled = page
      .waitForEvent("request", {
        predicate: (request) =>
          request.isNavigationRequest() && request.frame() === mainFrame,
        timeout: Math.min(timeout, 750),
      })
      .catch(() => undefined)
      .then(async (request) => {
        if (request === undefined) {
          return;
        }
        await page.waitForEvent("framenavigated", {
          predicate: (frame) => frame === mainFrame,
          timeout,
        });
        await page.waitForLoadState("domcontentloaded", { timeout });
      });
    await action();
    await navigationSettled;
  }

  private async observe(
    mode: ObservationMode,
    timeout: number,
  ): Promise<Observation> {
    const page = this.page;
    let screenshot: Buffer | undefined;
    if (mode === "screenshot" || mode === "both") {
      screenshot = await page.screenshot({
        animations: "disabled",
        caret: "hide",
        quality: 65,
        timeout,
        type: "jpeg",
      });
      if (screenshot.byteLength > MAX_SCREENSHOT_BYTES) {
        throw new EngineActionError(
          "BROWSER_OUTPUT_TOO_LARGE",
          "browser screenshot exceeds the output limit",
          false,
        );
      }
    }
    const includeSemantic = mode === "semantic" || mode === "both";
    const body = page.locator("body");
    const [title, ariaSnapshot, bodyText] = await Promise.all([
      page.title(),
      includeSemantic
        ? extractBounded(
            () => body.ariaSnapshot({ timeout: Math.min(timeout, 2000) }),
            MAX_STATE_TEXT_BYTES,
          )
        : Promise.resolve<ExtractionResult>({ value: "", timedOut: false }),
      includeSemantic
        ? extractBounded(
            () => body.innerText({ timeout: Math.min(timeout, 2000) }),
            MAX_STATE_TEXT_BYTES,
          )
        : Promise.resolve<ExtractionResult>({ value: "", timedOut: false }),
    ]);
    let domDiff: ReturnType<typeof textDiff> | undefined;
    if (includeSemantic && !bodyText.failed) {
      domDiff = textDiff(this.lastBodyText, bodyText.value);
      this.lastBodyText = bodyText.value;
    }
    const boundedTitle = truncateUTF8(title, 2048);
    this.lastSettledPageTitle = boundedTitle;
    const pageStateID = createHash("sha256")
      .update("openlinker.browser.page-state.v2\u0000")
      .update(truncateUTF8(page.url(), 4096))
      .update("\u0000")
      .update(boundedTitle)
      .update("\u0000")
      .update(ariaSnapshot.value)
      .update("\u0000")
      .update(bodyText.value)
      .digest("hex");
    const observation: Observation = {
      page_state_id: pageStateID,
      viewport: {
        width: VIEWPORT.width,
        height: VIEWPORT.height,
      },
      navigation_generation: this.navigationGeneration,
      origin: safeOrigin(page.url()),
      title: boundedTitle,
    };
    if (screenshot !== undefined) {
      observation.screenshot = {
        mime_type: "image/jpeg",
        data: screenshot.toString("base64"),
        width: VIEWPORT.width,
        height: VIEWPORT.height,
      };
    }
    if (includeSemantic && !ariaSnapshot.failed) {
      observation.ax_tree = { aria_snapshot: ariaSnapshot.value };
    }
    if (domDiff !== undefined) {
      observation.dom_diff = domDiff;
    }
    if (ariaSnapshot.timedOut) {
      observation.ax_tree_timed_out = true;
    }
    if (bodyText.timedOut) {
      observation.dom_diff_timed_out = true;
    }
    return observation;
  }
}

export async function routePageWebSocket(
  controlState: ControlState,
  webSocket: WebSocketRoute,
): Promise<void> {
  if (
    (controlState.controller === "human" ||
      (controlState.controller === "agent" &&
        controlState.interactionPolicy === "full" &&
        webSocketMutationAllowed(controlState, webSocket.url()))) &&
    controlState.pageWebSockets.size < MAX_HUMAN_PAGE_WEBSOCKETS
  ) {
    controlState.pageWebSockets.add(webSocket);
    webSocket.connectToServer();
    return;
  }
  if (controlState.controller === "agent" &&
      controlState.interactionPolicy === "full") {
    recordMutationBlock(controlState);
  }
  await webSocket.close({
    code: 1008,
    reason: "Agent control blocks page WebSocket traffic",
  });
}

export async function routePageRequest(
  controlState: ControlState,
  request: PlaywrightRequest,
  operations: {
    abort: () => Promise<unknown>;
    continue: () => Promise<unknown>;
  },
): Promise<void> {
  const method = request.method().toUpperCase();
  if (controlState.controller === "human" || allowsPageRequestMethod(method)) {
    await operations.continue();
    return;
  }
  if (
    controlState.controller === "agent" &&
    controlState.interactionPolicy === "full" &&
    ["POST", "PUT", "PATCH", "DELETE"].includes(method) &&
    pageRequestMutationAllowed(controlState, request)
  ) {
    await operations.continue();
    return;
  }
  if (
    controlState.controller === "agent" &&
    controlState.interactionPolicy === "full"
  ) {
    recordMutationBlock(controlState);
  }
  await operations.abort();
}

function pageRequestMutationAllowed(
  controlState: ControlState,
  request: PlaywrightRequest,
): boolean {
  try {
    const frame = request.frame();
    const source = canonicalDocumentOrigin(frame);
    const top = canonicalDocumentOrigin(frame.page().mainFrame());
    const destination = canonicalHTTPSOrigin(new URL(request.url()).origin);
    return [top, source, destination].every(
      (origin) => origin !== "" && controlState.mutationOrigins.has(origin),
    );
  } catch {
    return false;
  }
}

function webSocketMutationAllowed(
  controlState: ControlState,
  rawURL: string,
): boolean {
  try {
    const url = new URL(rawURL);
    if (url.protocol !== "wss:") {
      return false;
    }
    const destination = canonicalHTTPSOrigin(`https://${url.host}`);
    const frameOrigins = controlState.frameOrigins().map((origin) =>
      canonicalHTTPSOrigin(safeOrigin(origin)),
    );
    return (
      destination !== "" &&
      controlState.mutationOrigins.has(destination) &&
      frameOrigins.length > 0 &&
      frameOrigins.every(
        (origin) => origin !== "" && controlState.mutationOrigins.has(origin),
      )
    );
  } catch {
    return false;
  }
}

function recordMutationBlock(controlState: ControlState): void {
  controlState.mutationBlockSerial++;
  if (!controlState.actionInFlight) {
    controlState.blockedMutationRequests = Math.min(
      1024,
      controlState.blockedMutationRequests + 1,
    );
  }
}

function isTraversalAction(action: BrowserAction): boolean {
  return (
    action.kind === "navigate" ||
    action.kind === "back" ||
    action.kind === "forward"
  );
}

export async function restoreAgentPagePolicy(
  controlState: ControlState,
): Promise<void> {
  const sockets = [...controlState.pageWebSockets];
  await Promise.all(
    sockets.map((webSocket) =>
      webSocket.close({
        code: 1008,
        reason: "Human controller epoch ended",
      }),
    ),
  );
  controlState.pageWebSockets.clear();
  controlState.controller = "agent";
  controlState.attachmentKey = "";
}

interface ExtractionResult {
  value: string;
  timedOut: boolean;
  failed?: boolean;
}

interface ActionEffect {
  clickEffect: ClickEffect;
  targetCategory: "link" | "text_input" | "button" | "custom" | "other";
}

interface MainDocumentResponse {
  status: number;
  origin: string;
  retryAfter?: string;
}

async function extractBounded(
  extract: () => Promise<string>,
  maximumBytes: number,
): Promise<ExtractionResult> {
  try {
    return {
      value: truncateUTF8(await extract(), maximumBytes),
      timedOut: false,
    };
  } catch (error) {
    return {
      value: "",
      timedOut: error instanceof playwrightErrors.TimeoutError,
      failed: true,
    };
  }
}

class EngineActionError extends Error {
  readonly code: BrowserErrorCode;
  readonly recoverable: boolean;
  readonly actionIndex: number | undefined;
  readonly details: EngineErrorDetails;

  constructor(
    code: BrowserErrorCode,
    message: string,
    recoverable: boolean,
    actionIndex?: number,
    details: EngineErrorDetails = {},
  ) {
    super(message);
    this.code = code;
    this.recoverable = recoverable;
    this.actionIndex = actionIndex;
    this.details = details;
  }
}

function mutationOutcomeUnknown(
  reason: string,
  details: Pick<
    EngineFailure,
    "attempted_units" | "undispatched_units"
  > = {},
): EngineActionError {
  return new EngineActionError(
    "BROWSER_MUTATION_OUTCOME_UNKNOWN",
    "Browser mutation outcome could not be established",
    false,
    undefined,
    {
      retry_same_action: false,
      attachment_usable: true,
      fresh_observation_required: true,
      mutation_outcome_reason: reason,
      ...details,
    },
  );
}

function delay(durationMS: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, Math.max(1, durationMS)));
}

async function pinnedHandleOwnsFocus(
  handle: ElementHandle<HTMLElement>,
): Promise<boolean> {
  try {
    return await handle.evaluate((element) => {
      const formControl = element as HTMLInputElement | HTMLTextAreaElement;
      return (
        element.isConnected &&
        element.ownerDocument.activeElement === element &&
        !("disabled" in formControl && formControl.disabled) &&
        !("readOnly" in formControl && formControl.readOnly)
      );
    });
  } catch {
    return false;
  }
}

async function sequentialTypeIntoPinnedHandle(
  page: Page,
  handle: ElementHandle<HTMLElement>,
  text: string,
): Promise<void> {
  const units = Array.from(text);
  let attempted = 0;
  for (const unit of units) {
    if (!(await pinnedHandleOwnsFocus(handle))) {
      if (attempted === 0) {
        throw new EngineActionError(
          "BROWSER_ACTION_REJECTED",
          "active text control changed before text dispatch",
          false,
        );
      }
      throw mutationOutcomeUnknown("text_entry_target_changed_before_unit", {
        attempted_units: attempted,
        undispatched_units: units.length - attempted,
      });
    }
    try {
      await page.keyboard.type(unit);
    } catch {
      throw mutationOutcomeUnknown("text_entry_dispatch_uncertain", {
        attempted_units: attempted + 1,
        undispatched_units: units.length - attempted - 1,
      });
    }
    attempted++;
    if (!(await pinnedHandleOwnsFocus(handle))) {
      throw mutationOutcomeUnknown("text_entry_target_changed_after_unit", {
        attempted_units: attempted,
        undispatched_units: units.length - attempted,
      });
    }
  }
}

type EngineErrorDetails = Partial<
  Pick<
    EngineFailure,
    | "target_category"
    | "page_state_id"
    | "navigation_generation"
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
>;

function originRateLimited(retryAfterMS: number): EngineActionError {
  return new EngineActionError(
    "BROWSER_ORIGIN_RATE_LIMITED",
    "Browser origin budget is temporarily exhausted",
    true,
    undefined,
    {
      site_outcome: "BROWSER_ORIGIN_RATE_LIMITED",
      retry_after_ms: Math.max(1, Math.min(15 * 60 * 1000, retryAfterMS)),
    },
  );
}

function accessDenied(
  denials: number,
  blockedForAttachment: boolean,
): EngineActionError {
  return new EngineActionError(
    "BROWSER_ACCESS_DENIED",
    "Website denied access to the requested document",
    true,
    undefined,
    {
      site_outcome: "BROWSER_ACCESS_DENIED",
      consecutive_access_denials: Math.max(1, Math.min(3, denials)),
      ...(blockedForAttachment
        ? { origin_blocked_for_attachment: true }
        : {}),
    },
  );
}

function accessDenialKey(request: EngineRequest, origin: string): string {
  return [
    request.identity.attachment_id,
    request.identity.control_epoch,
    origin,
  ].join("\u0000");
}

function viewerAttachmentKey(
  identity: EngineViewerRequest["identity"],
): string {
  return [
    identity.attachment_id,
    identity.control_epoch,
    identity.controller,
  ].join("\u0000");
}

function agentAttachmentKey(identity: EngineRequest["identity"]): string {
  return [
    identity.attachment_id,
    identity.control_epoch,
    identity.browser_interaction_policy,
    identity.browser_interaction_policy_generation,
    identity.browser_mutation_origins_sha256,
  ].join("\u0000");
}

function opsObserverPageURL(raw: string): string {
  if (raw === "about:blank") return raw;
  const parsed = new URL(raw);
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username !== "" ||
    parsed.password !== ""
  ) {
    throw new Error("Ops Observer page URL is not public HTTP(S)");
  }
  parsed.search = "";
  parsed.hash = "";
  const redacted = parsed.toString();
  if (Buffer.byteLength(redacted, "utf8") <= 4096) return redacted;
  parsed.pathname = "/";
  const originOnly = parsed.toString();
  if (Buffer.byteLength(originOnly, "utf8") > 4096) {
    throw new Error("Ops Observer page URL exceeds the output limit");
  }
  return originOnly;
}

async function withOpsObserverTimeout<T>(
  operation: Promise<T>,
  timeoutMS: number,
): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      operation,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(
          () => reject(new Error("Ops Observer capture timed out")),
          timeoutMS,
        );
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

function requireProxy(raw: string | undefined): string {
  if (raw === undefined || raw.trim() !== raw) {
    throw new Error("OPENLINKER_BROWSER_EGRESS_PROXY is required");
  }
  const url = new URL(raw);
  if (
    url.protocol !== "http:" ||
    url.username !== "" ||
    url.password !== "" ||
    url.pathname !== "/" ||
    url.search !== "" ||
    url.hash !== ""
  ) {
    throw new Error("Browser egress proxy must be an HTTP origin without credentials");
  }
  return url.origin;
}

function requireProfileDirectory(raw: string | undefined): string {
  if (
    raw === undefined ||
    raw.trim() !== raw ||
    !path.isAbsolute(raw) ||
    path.normalize(raw) !== raw
  ) {
    throw new Error("OPENLINKER_BROWSER_PROFILE_DIR must be an absolute clean path");
  }
  return raw;
}

function remainingTimeout(deadline: string): number {
  const remaining = Date.parse(deadline) - Date.now();
  if (!Number.isFinite(remaining) || remaining <= 0) {
    throw new EngineActionError(
      "BROWSER_CANCELED",
      "browser action deadline elapsed",
      true,
    );
  }
  return Math.max(1, Math.min(remaining, 60_000));
}

function assertViewportPoint(x: number, y: number): void {
  if (x < 0 || y < 0 || x >= VIEWPORT.width || y >= VIEWPORT.height) {
    throw new EngineActionError(
      "BROWSER_OUTPUT_INVALID",
      "browser action coordinates are outside the viewport",
      false,
    );
  }
}

function assertAllowedPageURL(raw: string): void {
  if (raw === "about:blank" || isPublicHTTPURL(raw)) {
    return;
  }
  throw new EngineActionError(
    "BROWSER_TARGET_BLOCKED",
    "browser navigation reached a forbidden destination",
    false,
  );
}

interface InteractiveTarget {
  handle: ElementHandle<HTMLElement> | undefined;
  metadata: InteractiveMetadata;
  origin: string;
}

async function interactiveHandleAt(
  page: Page,
  x: number,
  y: number,
): Promise<InteractiveTarget> {
  let frame = page.mainFrame();
  let pointX = x;
  let pointY = y;
  for (let depth = 0; depth < 8; depth++) {
    const hitTest = ({ localX, localY }: { localX: number; localY: number }) => {
      const hit = document.elementFromPoint(localX, localY);
      const interactive = hit?.closest(
        "a,button,input,select,textarea,[role=button],[role=link]",
      );
      if (interactive instanceof HTMLElement) return interactive;
      return hit instanceof HTMLIFrameElement ? hit : null;
    };
    const point = { localX: pointX, localY: pointY };
    const candidate =
      frame === page.mainFrame()
        ? await page.evaluateHandle(hitTest, point)
        : await frame.evaluateHandle(hitTest, point);
    const element = candidate.asElement() as ElementHandle<HTMLElement> | null;
    if (element === null) {
      await candidate.dispose();
      break;
    }
    const metadata = await metadataForHandle(element);
    if (metadata.tagName.toLowerCase() !== "iframe") {
      return {
        handle: element,
        metadata,
        origin: canonicalDocumentOrigin(frame),
      };
    }
    const bounds = await element.evaluate((iframe) => {
      const rect = iframe.getBoundingClientRect();
      return { left: rect.left, top: rect.top };
    });
    const child = await element.contentFrame();
    await element.dispose();
    if (child === null) break;
    pointX -= bounds.left;
    pointY -= bounds.top;
    frame = child;
  }
  return {
    handle: undefined,
    metadata: emptyInteractiveMetadata(),
    origin: "",
  };
}

async function activeInteractiveHandle(page: Page): Promise<InteractiveTarget> {
  let frame = page.mainFrame();
  for (let depth = 0; depth < 8; depth++) {
    const activeElement = () => {
      const active = document.activeElement;
      return active instanceof HTMLElement ? active : null;
    };
    const candidate =
      frame === page.mainFrame()
        ? await page.evaluateHandle(activeElement)
        : await frame.evaluateHandle(activeElement);
    const element = candidate.asElement() as ElementHandle<HTMLElement> | null;
    if (element === null) {
      await candidate.dispose();
      break;
    }
    const metadata = await metadataForHandle(element);
    if (metadata.tagName.toLowerCase() !== "iframe") {
      return {
        handle: element,
        metadata,
        origin: canonicalDocumentOrigin(frame),
      };
    }
    const child = await element.contentFrame();
    await element.dispose();
    if (child === null) break;
    frame = child;
  }
  return {
    handle: undefined,
    metadata: emptyInteractiveMetadata(),
    origin: "",
  };
}

async function metadataForHandle(
  handle: ElementHandle<HTMLElement>,
): Promise<InteractiveMetadata> {
  return handle.evaluate((interactive) => {
    const input = interactive instanceof HTMLInputElement ? interactive : undefined;
    const anchor = interactive instanceof HTMLAnchorElement ? interactive : undefined;
    const textarea =
      interactive instanceof HTMLTextAreaElement ? interactive : undefined;
    const form = input?.form ?? textarea?.form;
    return {
      tagName: interactive.tagName.toLowerCase(),
      type: input?.type ?? "",
      autocomplete: input?.autocomplete ?? "",
      label: (
        interactive.getAttribute("aria-label") ??
        input?.labels?.[0]?.innerText ??
        textarea?.labels?.[0]?.innerText ??
        input?.placeholder ??
        textarea?.placeholder ??
        interactive.innerText ??
        ""
      ).slice(0, 512),
      href: (anchor?.href ?? "").slice(0, 1024),
      role: (interactive.getAttribute("role") ?? "").slice(0, 64),
      name: (input?.name ?? textarea?.name ?? "").slice(0, 256),
      placeholder: (input?.placeholder ?? textarea?.placeholder ?? "").slice(
        0,
        512,
      ),
      formMethod: (form?.method ?? "").slice(0, 16),
      formAction: (form?.action ?? "").slice(0, 1024),
      formRole: (form?.getAttribute("role") ?? "").slice(0, 64),
      download: anchor?.hasAttribute("download") ?? false,
    };
  });
}

function emptyInteractiveMetadata(): InteractiveMetadata {
  return {
    tagName: "",
    type: "",
    autocomplete: "",
    label: "",
    href: "",
    role: "",
    name: "",
    placeholder: "",
    formMethod: "",
    formAction: "",
    formRole: "",
    download: false,
  };
}

function targetCategory(metadata: InteractiveMetadata): TargetCategory {
  const tagName = metadata.tagName.toLowerCase();
  const role = metadata.role.toLowerCase();
  if (tagName === "") {
    return "none";
  }
  if (tagName === "a" || role === "link") {
    return "link";
  }
  if (
    (tagName === "input" &&
      ["text", "search"].includes(metadata.type.toLowerCase())) ||
    tagName === "textarea"
  ) {
    return "text_input";
  }
  if (
    tagName === "button" ||
    role === "button" ||
    (tagName === "input" &&
      ["button", "submit", "reset", "image"].includes(
        metadata.type.toLowerCase(),
      ))
  ) {
    return "button";
  }
  if (role !== "" || ["input", "select"].includes(tagName)) {
    return "custom";
  }
  return "other";
}

function activationTargetCategory(
  metadata: InteractiveMetadata,
): "link" | "text_input" | "button" | "custom" | "other" {
  const category = targetCategory(metadata);
  return category === "none" ? "other" : category;
}

function textDiff(
  previous: string,
  current: string,
): {
  kind: "baseline" | "diff";
  common_prefix: number;
  removed_characters: number;
  inserted: string;
  truncated: boolean;
} {
  if (previous === "") {
    return {
      kind: "baseline",
      common_prefix: 0,
      removed_characters: 0,
      inserted: current,
      truncated: Buffer.byteLength(current, "utf8") >= MAX_STATE_TEXT_BYTES,
    };
  }
  let commonPrefix = 0;
  const maximumPrefix = Math.min(previous.length, current.length);
  while (
    commonPrefix < maximumPrefix &&
    previous[commonPrefix] === current[commonPrefix]
  ) {
    commonPrefix++;
  }
  let commonSuffix = 0;
  while (
    commonSuffix < previous.length - commonPrefix &&
    commonSuffix < current.length - commonPrefix &&
    previous[previous.length - 1 - commonSuffix] ===
      current[current.length - 1 - commonSuffix]
  ) {
    commonSuffix++;
  }
  const inserted = current.slice(
    commonPrefix,
    commonSuffix === 0 ? current.length : current.length - commonSuffix,
  );
  return {
    kind: "diff",
    common_prefix: commonPrefix,
    removed_characters: previous.length - commonPrefix - commonSuffix,
    inserted: truncateUTF8(inserted, MAX_STATE_TEXT_BYTES),
    truncated: Buffer.byteLength(inserted, "utf8") > MAX_STATE_TEXT_BYTES,
  };
}

function safeOrigin(raw: string): string {
  try {
    const url = new URL(raw);
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return "";
    }
    return truncateUTF8(url.origin, 512);
  } catch {
    return "";
  }
}

function canonicalDocumentOrigin(frame: Frame): string {
  if (typeof frame.url !== "function") {
    return "";
  }
  return canonicalHTTPSOrigin(safeOrigin(frame.url()));
}

function safeRuntimeMessage(message: string): string {
  const normalized = message.toLowerCase();
  if (normalized.includes("proxy") || normalized.includes("net::")) {
    return "Browser network path is unavailable";
  }
  return "Browser engine is unavailable";
}
