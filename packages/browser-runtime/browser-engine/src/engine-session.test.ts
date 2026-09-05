import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import {
  errors as playwrightErrors,
  type BrowserContext,
  type Page,
} from "playwright-core";

import { BrowserEngine, type ControlState } from "./engine.js";
import { OriginBudgetStore } from "./origin-budget.js";
import { PageContinuationStore } from "./page-continuation.js";
import {
  type BrowserAction,
  ENGINE_CONTRACT_ID,
  ENGINE_OPS_OBSERVER_CONTRACT_ID,
  type EngineRequest,
  type Identity,
  type ObservationMode,
} from "./protocol.js";

test("retries Session restoration instead of succeeding on about:blank", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const identity = fixtureIdentity(1);
  const continuation = new PageContinuationStore(root);
  await continuation.record(identity, "https://example.com/saved");

  let restoreAttempts = 0;
  const context = new FakeContext(() =>
    new FakePage(async (url) => {
      if (url === "https://example.com/saved") {
        restoreAttempts++;
        if (restoreAttempts === 1) {
          throw new Error("temporary network failure");
        }
      }
    }),
  );
  const engine = createEngine(context, continuation, async () => true);

  const first = await engine.execute(request(identity, "1", "screenshot"));
  assert.equal(first.status, "error");
  if (first.status === "error") {
    assert.equal(first.error.code, "BROWSER_EGRESS_UNAVAILABLE");
    assert.equal(first.error.recoverable, true);
  }
  assert.equal(
    await continuation.lookup(identity),
    "https://example.com/saved",
    "failed restoration must not delete the continuation",
  );

  const second = await engine.execute(request(identity, "2", "screenshot"));
  assert.equal(second.status, "ok");
  if (second.status === "ok") {
    assert.equal(second.observation.origin, "https://example.com");
  }
  assert.equal(restoreAttempts, 2, "retry must attempt restoration again");
  assert.equal(context.createdPages.length, 2);
  assert.equal(context.createdPages[0]?.isClosed(), true);
  assert.equal(context.createdPages[1]?.url(), "https://example.com/saved");
});

test("uses a new Page when the Browser Session changes", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);

  const first = await engine.execute(
    request(fixtureIdentity(1), "1", "screenshot"),
  );
  assert.equal(first.status, "ok");
  const firstSessionPage = context.createdPages[0];
  assert.ok(firstSessionPage);
  firstSessionPage.sessionStorage.set("wizard-step", "session-a");

  const second = await engine.execute(
    request(fixtureIdentity(2), "2", "screenshot"),
  );
  assert.equal(second.status, "ok");
  const secondSessionPage = context.createdPages[1];
  assert.ok(secondSessionPage);
  assert.notEqual(firstSessionPage, secondSessionPage);
  assert.equal(firstSessionPage.isClosed(), true);
  assert.equal(secondSessionPage.sessionStorage.has("wizard-step"), false);
});

test("keeps uncorrelated HTTPS tunnel failures recoverable", async () => {
  for (const healthy of [true, false]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() =>
      new FakePage(async (url) => {
        if (url === "https://public.example/redirect") {
          throw new Error(
            "page.goto: net::ERR_TUNNEL_CONNECTION_FAILED at " + url,
          );
        }
      }),
    );
    let probes = 0;
    const engine = createEngine(context, continuation, async () => {
      probes++;
      return healthy;
    });

    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, "BROWSER_EGRESS_UNAVAILABLE");
      assert.equal(response.error.recoverable, true);
    }
    assert.equal(probes, 0);
  }
});

test("defaults to semantic output and never hashes screenshot bytes", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);

  const semantic = await engine.execute(
    request(identity, "1", "screenshot"),
  );
  assert.equal(semantic.status, "ok");
  if (semantic.status !== "ok") {
    return;
  }
  assert.equal(semantic.observation.screenshot, undefined);
  assert.notEqual(semantic.observation.ax_tree, undefined);
  const page = context.createdPages[0];
  assert.ok(page);
  assert.equal(page.screenshotCalls, 0);

  const firstScreenshot = await engine.execute(
    request(identity, "2", "screenshot", "screenshot"),
  );
  const secondScreenshot = await engine.execute(
    request(identity, "3", "screenshot", "screenshot"),
  );
  assert.equal(firstScreenshot.status, "ok");
  assert.equal(secondScreenshot.status, "ok");
  if (firstScreenshot.status === "ok" && secondScreenshot.status === "ok") {
    assert.notEqual(firstScreenshot.observation.screenshot?.data, undefined);
    assert.notEqual(secondScreenshot.observation.screenshot?.data, undefined);
    assert.notEqual(
      firstScreenshot.observation.screenshot?.data,
      secondScreenshot.observation.screenshot?.data,
    );
    assert.equal(
      firstScreenshot.observation.page_state_id,
      secondScreenshot.observation.page_state_id,
      "page_state_id must be a semantic digest, not a screenshot hash",
    );
  }
});

test("reports semantic extraction timeouts without failing the action", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.ariaError = new playwrightErrors.TimeoutError("aria timeout");
    page.bodyError = new playwrightErrors.TimeoutError("body timeout");
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute(
    request(fixtureIdentity(1), "1", "screenshot"),
  );
  assert.equal(response.status, "ok");
  if (response.status === "ok") {
    assert.equal(response.observation.ax_tree, undefined);
    assert.equal(response.observation.dom_diff, undefined);
    assert.equal(response.observation.ax_tree_timed_out, true);
    assert.equal(response.observation.dom_diff_timed_out, true);
  }
});

test("preflight requires Gateway health and produces a bounded blank observation", async () => {
  for (const healthy of [false, true]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() => new FakePage());
    let probes = 0;
    const engine = createEngine(context, continuation, async () => {
      probes++;
      return healthy;
    });
    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "preflight", "semantic"),
    );
    assert.equal(probes, 1);
    if (!healthy) {
      assert.equal(response.status, "error");
      if (response.status === "error") {
        assert.equal(response.error.code, "BROWSER_EGRESS_UNAVAILABLE");
      }
      continue;
    }
    assert.equal(response.status, "ok");
    if (response.status === "ok") {
      assert.equal(response.observation.origin, "");
      assert.equal(response.observation.screenshot, undefined);
      assert.ok(response.observation.page_state_id.length <= 256);
    }
  }
});

test("preflight does not suppress restoration for the first real Session action", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const identity = fixtureIdentity(1);
  const continuation = new PageContinuationStore(root);
  await continuation.record(identity, "https://example.com/saved");
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);

  const preflight = await engine.execute(
    request(identity, "1", "preflight", "semantic"),
  );
  assert.equal(preflight.status, "ok");
  if (preflight.status === "ok") {
    assert.equal(preflight.observation.origin, "");
  }

  const firstAction = await engine.execute(
    request(identity, "2", "screenshot", "semantic"),
  );
  assert.equal(firstAction.status, "ok");
  if (firstAction.status === "ok") {
    assert.equal(firstAction.observation.origin, "https://example.com");
  }
  assert.equal(
    context.createdPages.at(-1)?.url(),
    "https://example.com/saved",
  );
  assert.equal(
    await continuation.lookup(identity),
    "https://example.com/saved",
    "preflight must not replace the saved continuation with about:blank",
  );
});

test("history traversal supports BFCache without classifying a loading document", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);

  for (const [index, kind] of (["back", "forward"] as const).entries()) {
    const response = await engine.execute(
      request(identity, String(index + 1), kind),
    );
    assert.equal(response.status, "ok");
  }
  const page = context.createdPages[0];
  assert.ok(page);
  assert.deepEqual(page.historyWaitUntil, ["commit", "commit"]);
  assert.equal(page.documentReadyWaits, 2);
});

test("executes a safe batch with exactly one final observation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute({
    ...request(fixtureIdentity(1), "1", "screenshot"),
    action: {
      kind: "batch",
      observation: "screenshot",
      actions: [{ kind: "screenshot" }, { kind: "screenshot" }],
    },
  });
  assert.equal(response.status, "ok");
  assert.equal(context.createdPages[0]?.screenshotCalls, 1);
});

test("Ops Observer reuses the last settled title without a concurrent title request", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);

  const action = await engine.execute(request(identity, "1", "navigate"));
  assert.equal(action.status, "ok");
  const page = context.createdPages[0];
  assert.ok(page);
  page.titleError = new playwrightErrors.TimeoutError(
    "concurrent title request must not run",
  );

  const observed = await engine.executeOpsObserver({
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: "2",
    deadline: new Date(Date.now() + 500).toISOString(),
    identity,
    operation: "observe_status",
  });
  assert.equal(observed.status, "ok");
  if (observed.status === "ok") {
    assert.equal(observed.page_title, "Fixture page");
    assert.equal(observed.page_url, "https://public.example/redirect");
  }
  const frame = await engine.executeOpsObserver({
    contract_id: ENGINE_OPS_OBSERVER_CONTRACT_ID,
    action_id: "3",
    deadline: new Date(Date.now() + 500).toISOString(),
    identity,
    operation: "observe_frame",
  });
  assert.equal(frame.status, "ok");
  if (frame.status === "ok") {
    assert.equal(frame.page_title, "Fixture page");
    assert.equal(frame.frame?.mime_type, "image/jpeg");
  }
  assert.equal(page.titleCalls, 1);
  assert.equal(page.screenshotCalls, 1);
});

test("reports the failed action index for any batch execution failure", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.waitError = new Error("fixture wait failure");
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const response = await engine.execute({
    ...request(fixtureIdentity(1), "1", "screenshot"),
    action: {
      kind: "batch",
      actions: [
        { kind: "screenshot" },
        { kind: "wait", duration_ms: 1 },
        { kind: "screenshot" },
      ],
    },
  });
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_RUNTIME_UNAVAILABLE");
    assert.equal(response.error.recoverable, true);
    assert.equal(response.error.action_index, 1);
  }
});

test("charges each nested batch navigation before dispatch", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(41, true);
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 8,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await budget.load();
  (
    engine as unknown as {
      originBudget: OriginBudgetStore | undefined;
    }
  ).originBudget = budget;

  const response = await engine.execute(
    actionRequest(identity, "batch-navigation", {
      kind: "batch",
      actions: [
        { kind: "navigate", url: "https://public.example/first" },
        { kind: "navigate", url: "https://public.example/second" },
      ],
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_ORIGIN_RATE_LIMITED");
    assert.equal(response.error.action_index, 1);
    assert.equal(response.error.completed_actions, 1);
  }
  assert.equal(page?.publicNavigationCalls, 1);
});

test("does not let an about:blank traversal batch bypass action budgets", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(42, true);
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 1,
    maxNavigationsPerMinute: 4,
    now: () => 1_000_000,
  });
  await budget.load();
  (
    engine as unknown as {
      originBudget: OriginBudgetStore | undefined;
    }
  ).originBudget = budget;

  const response = await engine.execute(
    actionRequest(identity, "blank-batch", {
      kind: "batch",
      actions: [
        { kind: "navigate", url: "https://public.example/first" },
        { kind: "screenshot" },
      ],
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_ORIGIN_RATE_LIMITED");
    assert.equal(response.error.action_index, 1);
    assert.equal(response.error.completed_actions, 1);
  }
  assert.equal(page?.publicNavigationCalls, 1);
});

test("stops a batch immediately after a nested access denial", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.responseStatus = 403;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(43, true);

  const response = await engine.execute(
    actionRequest(identity, "batch-access-denial", {
      kind: "batch",
      actions: [
        { kind: "navigate", url: "https://public.example/first" },
        { kind: "navigate", url: "https://public.example/second" },
      ],
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_ACCESS_DENIED");
    assert.equal(response.error.action_index, 0);
    assert.equal(response.error.completed_actions, 0);
    assert.equal(response.error.consecutive_access_denials, 1);
  }
  assert.equal(page?.publicNavigationCalls, 1);
});

test("keeps 403 active and locally blocks only the fourth navigation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.responseStatus = 403;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);
  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 10,
    maxNavigationsPerMinute: 4,
    now: () => 1_000_000,
  });
  await budget.load();
  (
    engine as unknown as {
      originBudget: OriginBudgetStore | undefined;
    }
  ).originBudget = budget;
  for (let attempt = 1; attempt <= 4; attempt++) {
    const response = await engine.execute(
      request(identity, String(attempt), "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, "BROWSER_ACCESS_DENIED");
      assert.equal(response.error.consecutive_access_denials, Math.min(3, attempt));
      assert.equal(
        response.error.origin_blocked_for_attachment,
        attempt >= 3 ? true : undefined,
      );
    }
  }
  assert.equal(page?.publicNavigationCalls, 3);

  const historyBack = await engine.execute(
    request(identity, "history-back", "back"),
  );
  assert.equal(
    historyBack.status,
    "ok",
    "history traversal must not guess the blocked target origin",
  );
  assert.deepEqual(page?.historyWaitUntil, ["commit"]);

  const historyForward = await engine.execute(
    request(identity, "history-forward", "forward"),
  );
  assert.equal(historyForward.status, "error");
  if (historyForward.status === "error") {
    assert.equal(
      historyForward.error.code,
      "BROWSER_ORIGIN_RATE_LIMITED",
      "history traversal must charge navigation budget to the active origin",
    );
  }
});

test("distinguishes suspected and required challenge fixtures", async () => {
  for (const fixture of [
    {
      selector: '[id*="captcha" i]',
      code: "BROWSER_CHALLENGE_SUSPECTED",
      recoverable: true,
    },
    {
      selector:
        'iframe[src^="https://challenges.cloudflare.com/turnstile/"]',
      code: "BROWSER_CHALLENGE_REQUIRED",
      recoverable: false,
    },
  ] as const) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const context = new FakeContext(() => {
      const page = new FakePage();
      page.challengeSelectors.add(fixture.selector);
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const response = await engine.execute(
      request(fixtureIdentity(1), "1", "navigate"),
    );
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, fixture.code);
      assert.equal(response.error.recoverable, fixture.recoverable);
      assert.equal(
        response.error.classifier_rules_version,
        "openlinker.browser.challenge-rules.v1",
      );
    }
  }
});

test("keeps suspected state sticky across unrelated errors after CDP loss", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.challengeSelectors.add('[id*="captcha" i]');
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fixtureIdentity(1);
  const suspected = await engine.execute(request(identity, "1", "navigate"));
  assert.equal(suspected.status, "error");
  if (suspected.status === "error") {
    assert.equal(suspected.error.code, "BROWSER_CHALLENGE_SUSPECTED");
  }

  const budget = new OriginBudgetStore({
    profileDirectory: root,
    profileGeneration: 1,
    maxActionsPerMinute: 1,
    maxNavigationsPerMinute: 1,
    now: () => 1_000_000,
  });
  await budget.load();
  assert.equal(
    budget.chargeAction(identity, "https://public.example"),
    undefined,
  );
  const internal = engine as unknown as {
    documentTracker: {
      generation: number;
      healthy: boolean;
      detach: () => Promise<void>;
    };
    originBudget: OriginBudgetStore | undefined;
  };
  internal.documentTracker = {
    generation: 1,
    healthy: false,
    detach: async () => undefined,
  };
  internal.originBudget = budget;

  const rateLimited = await engine.execute(
    request(identity, "2", "screenshot"),
  );
  assert.equal(rateLimited.status, "error");
  if (rateLimited.status === "error") {
    assert.equal(rateLimited.error.code, "BROWSER_ORIGIN_RATE_LIMITED");
    assert.equal(rateLimited.error.challenge_release_unavailable, true);
  }

  internal.originBudget = undefined;
  internal.documentTracker.healthy = true;
  internal.documentTracker.generation = 2;
  const reconnected = await engine.execute(
    request(identity, "3", "screenshot"),
  );
  assert.equal(reconnected.status, "error");
  if (reconnected.status === "error") {
    assert.equal(
      reconnected.error.code,
      "BROWSER_CHALLENGE_SUSPECTED",
    );
    assert.equal(
      reconnected.error.challenge_release_unavailable,
      true,
      "a healthy replacement tracker must not clear attachment-sticky state",
    );
  }

  context.createdPages[0]?.challengeSelectors.clear();
  const newAttachment = {
    ...identity,
    attachment_id: "55555555-5555-4555-8555-555555555555",
    control_epoch: 2,
  };
  const recovered = await engine.execute(
    request(newAttachment, "4", "screenshot"),
  );
  assert.equal(recovered.status, "ok");
  if (recovered.status === "ok") {
    assert.equal(
      recovered.observation.challenge_release_unavailable,
      undefined,
    );
  }
});

test("fails closed when the document tracker factory is missing", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => new FakePage());
  type UnsafeConstructor = new (
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: undefined,
    originBudget: undefined,
    documentTrackerFactory: undefined,
    controlState: ControlState,
  ) => BrowserEngine;
  const Constructor = BrowserEngine as unknown as UnsafeConstructor;
  const engine = new Constructor(
    context as unknown as BrowserContext,
    context.initialPage as unknown as Page,
    continuation,
    async () => true,
    undefined,
    undefined,
    undefined,
    testControlState(),
  );

  const response = await engine.execute(
    request(fixtureIdentity(1), "missing-tracker", "screenshot"),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_RUNTIME_UNAVAILABLE");
    assert.equal(response.error.recoverable, false);
  }
});

test("reclassifies trusted document changes but not same-document history", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.challengeSelectors.add('[id*="captcha" i]');
    return page;
  });
  const tracker = new FakeDocumentTracker();
  const engine = createEngine(
    context,
    continuation,
    async () => true,
    async () => tracker,
  );
  const identity = fixtureIdentity(1);

  const suspected = await engine.execute(request(identity, "1", "navigate"));
  assert.equal(suspected.status, "error");
  if (suspected.status === "error") {
    assert.equal(suspected.error.code, "BROWSER_CHALLENGE_SUSPECTED");
  }
  const page = context.createdPages[0];
  assert.ok(page);
  page.challengeSelectors.clear();

  const sameDocument = await engine.execute(
    request(identity, "2", "screenshot"),
  );
  assert.equal(sameDocument.status, "ok");
  assert.equal(
    (engine as unknown as { suspectedDocumentGeneration?: number })
      .suspectedDocumentGeneration,
    1,
    "pushState/same-document changes must not release the suspected gate",
  );

  tracker.generation = 2;
  const cleanDocument = await engine.execute(
    request(identity, "3", "screenshot"),
  );
  assert.equal(cleanDocument.status, "ok");
  assert.equal(
    (engine as unknown as { suspectedDocumentGeneration?: number })
      .suspectedDocumentGeneration,
    undefined,
  );

  page.challengeSelectors.add('[id*="captcha" i]');
  tracker.generation = 3;
  const restoredChallenge = await engine.execute(
    request(identity, "4", "screenshot"),
  );
  assert.equal(restoredChallenge.status, "error");
  if (restoredChallenge.status === "error") {
    assert.equal(
      restoredChallenge.error.code,
      "BROWSER_CHALLENGE_SUSPECTED",
    );
  }
});

test("focuses the pinned safe text handle without pointer activation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const target = new FakeElementState(safeTextMetadata());
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.hitTestElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "focus-1", {
      kind: "click",
      x: 120,
      y: 80,
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "ok");
  if (response.status === "ok") {
    assert.equal(response.observation.click_effect, "focused");
    assert.equal(response.observation.target_category, "text_input");
  }
  assert.equal(target.focusCalls, 1);
  assert.equal(target.clickCalls, 0);
  assert.equal(page?.activeElement, target);
});

test("types through the pinned active handle after global focus moves", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const original = new FakeElementState(safeTextMetadata(), "hello", 5, 5);
  const moved = new FakeElementState(safeTextMetadata(), "untouched", 9, 9);
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.activeElement = original;
    original.onMetadata = () => {
      if (page !== undefined) {
        page.activeElement = moved;
      }
    };
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "type-1", {
      kind: "type_non_secret",
      text: " world",
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "ok");
  assert.equal(original.value, "hello world");
  assert.equal(original.fillCalls, 1);
  assert.equal(moved.value, "untouched");
  assert.equal(moved.fillCalls, 0);
});

test("rejects an oversized existing value without changing the field", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const originalValue = "x".repeat(16 * 1024 + 1);
  const target = new FakeElementState(
    safeTextMetadata(),
    originalValue,
    originalValue.length,
    originalValue.length,
  );
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.activeElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "type-overflow", {
      kind: "type_non_secret",
      text: "y",
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_ACTION_REJECTED");
    assert.equal(response.error.recoverable, false);
  }
  assert.equal(target.value, originalValue);
  assert.equal(target.fillCalls, 0);
});

test("blocked click exposes only a coarse target category", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const target = new FakeElementState({
    ...safeTextMetadata(),
    tagName: "button",
    type: "submit",
    label: "PAGE_CONTROLLED_SECRET_LABEL",
  });
  const context = new FakeContext(() => {
    const page = new FakePage();
    page.hitTestElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);

  const response = await engine.execute(
    actionRequest(fixtureIdentity(1), "blocked-1", {
      kind: "click",
      x: 120,
      y: 80,
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_HIGH_IMPACT_ACTION_BLOCKED");
    assert.equal(response.error.target_category, "button");
    assert.equal(response.error.navigation_generation, 1);
    assert.ok(response.error.page_state_id);
  }
  assert.equal(
    JSON.stringify(response).includes("PAGE_CONTROLLED_SECRET_LABEL"),
    false,
  );
});

test("full policy activates an allowlisted button and blocks it elsewhere", async () => {
  for (const allowed of [true, false]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const target = new FakeElementState({
      ...safeTextMetadata(),
      tagName: "button",
      type: "button",
      label: "Star fixture",
    });
    const context = new FakeContext(() => {
      const page = new FakePage();
      page.hitTestElement = target;
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const identity = fullIdentity(allowed ? 11 : 12, allowed);
    assert.equal(
      (await engine.execute(request(identity, "1", "navigate"))).status,
      "ok",
    );
    const response = await engine.execute(
      actionRequest(identity, "2", {
        kind: "click",
        x: 120,
        y: 80,
        observation: "semantic",
      }),
    );
    if (allowed) {
      assert.equal(response.status, "ok");
      if (response.status === "ok") {
        assert.equal(response.observation.click_effect, "activated");
        assert.equal(response.observation.target_category, "button");
      }
      assert.equal(target.clickCalls, 1);
    } else {
      assert.equal(response.status, "error");
      if (response.status === "error") {
        assert.equal(response.error.code, "BROWSER_MUTATION_ORIGIN_BLOCKED");
        assert.equal(response.error.retry_same_action, false);
        assert.equal(response.error.attachment_usable, true);
      }
      assert.equal(target.clickCalls, 0);
    }
  }
});

test("full policy establishes or rejects state-changing page request outcomes", async () => {
  for (const requestOutcome of [
    "response",
    "finished",
    "failed",
    "server_error",
  ] as const) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    let page: FakePage | undefined;
    const target = new FakeElementState({
      ...safeTextMetadata(),
      tagName: "button",
      type: "button",
      label: "Mutation fixture",
    });
    const context = new FakeContext(() => {
      page = new FakePage();
      page.hitTestElement = target;
      target.onClick = () => {
        const request = new FakeMutationRequest("POST");
        page?.emit("request", request);
        if (requestOutcome === "response" || requestOutcome === "server_error") {
          page?.emit(
            "response",
            new FakeMutationResponse(
              request,
              requestOutcome === "response" ? 200 : 502,
            ),
          );
        }
        if (requestOutcome !== "response") {
          page?.emit(
            requestOutcome === "failed" ? "requestfailed" : "requestfinished",
            request,
          );
        }
      };
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const identity = fullIdentity(requestOutcome === "finished" ? 13 : 14, true);
    assert.equal(
      (await engine.execute(request(identity, "1", "navigate"))).status,
      "ok",
    );
    const response = await engine.execute(
      actionRequest(identity, "2", {
        kind: "click",
        x: 120,
        y: 80,
        observation: "semantic",
      }),
    );
    if (requestOutcome === "response" || requestOutcome === "finished") {
      assert.equal(response.status, "ok");
      if (response.status === "ok") {
        assert.equal(response.observation.mutation_requests_observed, 1);
      }
      continue;
    }
    assert.equal(response.status, "error");
    if (response.status === "error") {
      assert.equal(response.error.code, "BROWSER_MUTATION_OUTCOME_UNKNOWN");
      assert.equal(
        response.error.mutation_outcome_reason,
        "page_mutation_request_failed",
      );
      assert.equal(response.error.retry_same_action, false);
      assert.equal(response.error.attachment_usable, true);
      assert.equal(response.error.fresh_observation_required, true);
      assert.equal(response.error.mutation_requests_observed, 1);
    }
  }
});

test("requires a fresh observation before another action after an unknown mutation", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const target = new FakeElementState({
    ...safeTextMetadata(),
    tagName: "button",
    type: "button",
    label: "Mutation recovery fixture",
  });
  const context = new FakeContext(() => {
    page = new FakePage();
    page.hitTestElement = target;
    target.onClick = () => {
      const mutation = new FakeMutationRequest("POST");
      page?.emit("request", mutation);
      page?.emit("requestfailed", mutation);
    };
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(15, true);
  assert.equal(
    (await engine.execute(request(identity, "navigate", "navigate"))).status,
    "ok",
  );

  const mutation = actionRequest(identity, "mutation", {
    kind: "click",
    x: 120,
    y: 80,
    observation: "semantic",
  });
  const uncertain = await engine.execute(mutation);
  assert.equal(uncertain.status, "error");
  if (uncertain.status === "error") {
    assert.equal(uncertain.error.code, "BROWSER_MUTATION_OUTCOME_UNKNOWN");
    assert.equal(uncertain.error.fresh_observation_required, true);
  }

  const blocked = await engine.execute({
    ...mutation,
    action_id: "blocked-retry",
  });
  assert.equal(blocked.status, "error");
  if (blocked.status === "error") {
    assert.equal(blocked.error.code, "BROWSER_ACTION_REJECTED");
  }
  assert.equal(target.clickCalls, 1, "the uncertain click must not be retried");

  const observed = await engine.execute(
    request(identity, "fresh-observation", "screenshot"),
  );
  assert.equal(observed.status, "ok");

  const admitted = await engine.execute({
    ...mutation,
    action_id: "after-observation",
  });
  assert.equal(admitted.status, "error");
  if (admitted.status === "error") {
    assert.equal(admitted.error.code, "BROWSER_MUTATION_OUTCOME_UNKNOWN");
  }
  assert.equal(target.clickCalls, 2);
});

test("stops a full batch before the next action when mutation delivery becomes unknown", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  let page: FakePage | undefined;
  const target = new FakeElementState({
    ...safeTextMetadata(),
    tagName: "button",
    type: "button",
    label: "Batch mutation fixture",
  });
  const context = new FakeContext(() => {
    page = new FakePage();
    page.hitTestElement = target;
    target.onClick = () => {
      const mutation = new FakeMutationRequest("POST");
      page?.emit("request", mutation);
      page?.emit("requestfailed", mutation);
    };
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(16, true);
  assert.equal(
    (await engine.execute(request(identity, "navigate", "navigate"))).status,
    "ok",
  );

  const response = await engine.execute({
    ...request(identity, "batch", "screenshot"),
    action: {
      kind: "batch",
      observation: "semantic",
      actions: [
        { kind: "click", x: 120, y: 80 },
        { kind: "click", x: 120, y: 80 },
      ],
    },
  });
  assert.equal(response.status, "error");
  if (response.status === "error") {
    assert.equal(response.error.code, "BROWSER_MUTATION_OUTCOME_UNKNOWN");
    assert.equal(response.error.action_index, 0);
    assert.equal(response.error.completed_actions, 0);
  }
  assert.equal(target.clickCalls, 1, "the second batch click must not run");
});

test("full policy resolves a pinned child-frame target and requires both exact origins", async () => {
  for (const allowChild of [true, false]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const target = new FakeElementState({
      ...safeTextMetadata(),
      tagName: "button",
      type: "button",
      label: "Child frame mutation",
    });
    const child = new FakeChildFrame("https://child.example/frame");
    child.hitTestElement = target;
    const iframe = new FakeElementState(
      {
        ...safeTextMetadata(),
        tagName: "iframe",
        type: "",
        label: "",
      },
      "",
      0,
      0,
      child,
      { left: 40, top: 20 },
    );
    const context = new FakeContext(() => {
      const page = new FakePage();
      page.hitTestElement = iframe;
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const identity = fullIdentityWithOrigins(
      allowChild ? 31 : 32,
      allowChild
        ? ["https://child.example", "https://public.example"]
        : ["https://public.example"],
    );
    assert.equal(
      (await engine.execute(request(identity, "1", "navigate"))).status,
      "ok",
    );
    const response = await engine.execute(
      actionRequest(identity, "2", {
        kind: "click",
        x: 120,
        y: 80,
        observation: "semantic",
      }),
    );
    if (allowChild) {
      assert.equal(response.status, "ok");
      assert.equal(target.clickCalls, 1);
      assert.deepEqual(child.lastHitPoint, { localX: 80, localY: 60 });
    } else {
      assert.equal(response.status, "error");
      if (response.status === "error") {
        assert.equal(response.error.code, "BROWSER_MUTATION_ORIGIN_BLOCKED");
        assert.equal(response.error.retry_same_action, false);
      }
      assert.equal(target.clickCalls, 0);
    }
  }
});

test("full text entry dispatches Unicode units and stops after focus drift", async () => {
  for (const stealFocus of [false, true]) {
    const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
    const continuation = new PageContinuationStore(root);
    const target = new FakeElementState(safeTextMetadata());
    let page: FakePage | undefined;
    const context = new FakeContext(() => {
      page = new FakePage();
      page.activeElement = target;
      if (stealFocus) {
        page.onType = () => {
          if (page !== undefined) page.activeElement = undefined;
        };
      }
      return page;
    });
    const engine = createEngine(context, continuation, async () => true);
    const identity = fullIdentity(stealFocus ? 21 : 20, true);
    assert.equal(
      (await engine.execute(request(identity, "1", "navigate"))).status,
      "ok",
    );
    const response = await engine.execute(
      actionRequest(identity, "2", {
        kind: "type_non_secret",
        text: "A中🙂",
        observation: "semantic",
      }),
    );
    if (!stealFocus) {
      assert.equal(response.status, "ok");
      assert.deepEqual(page?.typedUnits, ["A", "中", "🙂"]);
      assert.equal(target.value, "A中🙂");
    } else {
      assert.equal(response.status, "error");
      if (response.status === "error") {
        assert.equal(response.error.code, "BROWSER_MUTATION_OUTCOME_UNKNOWN");
        assert.equal(response.error.attempted_units, 1);
        assert.equal(response.error.undispatched_units, 2);
        assert.equal(response.error.retry_same_action, false);
      }
      assert.deepEqual(page?.typedUnits, ["A"]);
    }
  }
});

test("full policy dispatches Space only for an allowlisted pinned control", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "openlinker-engine-"));
  const continuation = new PageContinuationStore(root);
  const target = new FakeElementState({
    ...safeTextMetadata(),
    tagName: "button",
    type: "button",
    label: "Toggle fixture",
  });
  let page: FakePage | undefined;
  const context = new FakeContext(() => {
    page = new FakePage();
    page.activeElement = target;
    return page;
  });
  const engine = createEngine(context, continuation, async () => true);
  const identity = fullIdentity(40, true);
  assert.equal(
    (await engine.execute(request(identity, "1", "navigate"))).status,
    "ok",
  );
  const response = await engine.execute(
    actionRequest(identity, "2", {
      kind: "keypress",
      key: "Space",
      observation: "semantic",
    }),
  );
  assert.equal(response.status, "ok");
  assert.deepEqual(page?.pressedKeys, ["Space"]);
});

type GatewayHealthProbe = (timeout: number) => Promise<boolean>;

function createEngine(
  context: FakeContext,
  continuation: PageContinuationStore,
  gatewayHealthProbe: GatewayHealthProbe,
  documentTrackerFactory?: (
    page: Page,
  ) => Promise<FakeDocumentTracker>,
): BrowserEngine {
  type TestConstructor = new (
    context: BrowserContext,
    page: Page,
    continuation: PageContinuationStore,
    gatewayHealthProbe: GatewayHealthProbe,
    environment: undefined,
    originBudget: undefined,
    documentTrackerFactory: (
      page: Page,
    ) => Promise<FakeDocumentTracker>,
    controlState: ControlState,
  ) => BrowserEngine;
  const Constructor = BrowserEngine as unknown as TestConstructor;
  return new Constructor(
    context as unknown as BrowserContext,
    context.initialPage as unknown as Page,
    continuation,
    gatewayHealthProbe,
    undefined,
    undefined,
    documentTrackerFactory ?? (async () => new FakeDocumentTracker()),
    testControlState(),
  );
}

function testControlState(): ControlState {
  return {
    controller: "agent",
    attachmentKey: "",
    pageWebSockets: new Set(),
    interactionPolicy: "restricted",
    mutationOrigins: new Set(),
    mutationBlockSerial: 0,
    blockedMutationRequests: 0,
    actionInFlight: false,
    frameOrigins: () => [],
  };
}

class FakeDocumentTracker {
  generation = 1;
  healthy = true;

  async detach(): Promise<void> {
    this.healthy = false;
  }
}

class FakeContext {
  readonly initialPage = new FakePage();
  readonly createdPages: FakePage[] = [];
  private readonly createPage: () => FakePage;

  constructor(createPage: () => FakePage) {
    this.createPage = createPage;
  }

  pages(): Page[] {
    return [this.initialPage, ...this.createdPages]
      .filter((page) => !page.isClosed()) as unknown as Page[];
  }

  async newPage(): Promise<Page> {
    const page = this.createPage();
    this.createdPages.push(page);
    return page as unknown as Page;
  }
}

class FakePage {
  readonly sessionStorage = new Map<string, string>();
  screenshotCalls = 0;
  titleCalls = 0;
  titleError: Error | undefined;
  ariaError: Error | undefined;
  bodyError: Error | undefined;
  waitError: Error | undefined;
  responseStatus = 200;
  retryAfter: string | undefined;
  publicNavigationCalls = 0;
  readonly historyWaitUntil: string[] = [];
  documentReadyWaits = 0;
  readonly challengeSelectors = new Set<string>();
  hitTestElement: FakeElementState | undefined;
  activeElement: FakeElementState | undefined;
  readonly typedUnits: string[] = [];
  readonly pressedKeys: string[] = [];
  onType: (() => void) | undefined;
  readonly keyboard = {
    type: async (text: string) => {
      this.typedUnits.push(text);
      if (this.activeElement !== undefined) {
        this.activeElement.value += text;
      }
      this.onType?.();
    },
    press: async (key: string) => {
      this.pressedKeys.push(key);
    },
  };
  readonly mouse = {
    wheel: async () => undefined,
  };
  private currentURL = "about:blank";
  private closed = false;
  private readonly frame: {
    url: () => string;
    page: () => { mainFrame: () => object };
  };
  private readonly listeners = new Map<string, Array<(value: any) => void>>();
  private readonly navigate: (url: string) => Promise<void>;

  constructor(navigate: (url: string) => Promise<void> = async () => undefined) {
    this.navigate = navigate;
    this.frame = {
      url: () => this.currentURL,
      page: () => ({ mainFrame: () => this.frame }),
    };
  }

  on(event: string, listener: (value: any) => void): this {
    const listeners = this.listeners.get(event) ?? [];
    listeners.push(listener);
    this.listeners.set(event, listeners);
    return this;
  }

  emit(event: string, value: unknown): void {
    for (const listener of this.listeners.get(event) ?? []) {
      listener(value);
    }
  }

  isClosed(): boolean {
    return this.closed;
  }

  async close(): Promise<void> {
    this.closed = true;
  }

  setDefaultTimeout(): void {}

  setDefaultNavigationTimeout(): void {}

  async goto(url: string): Promise<null> {
    await this.navigate(url);
    this.currentURL = url;
    if (url.startsWith("http://") || url.startsWith("https://")) {
      this.publicNavigationCalls++;
      const headers =
        this.retryAfter === undefined
          ? {}
          : { "retry-after": this.retryAfter };
      for (const listener of this.listeners.get("response") ?? []) {
        listener({
          request: () => ({
            isNavigationRequest: () => true,
          }),
          frame: () => this.frame,
          headers: () => headers,
          status: () => this.responseStatus,
          url: () => url,
        });
      }
    }
    return null;
  }

  url(): string {
    return this.currentURL;
  }

  mainFrame(): object {
    return this.frame;
  }

  frames(): object[] {
    return [this.frame];
  }

  async screenshot(): Promise<Buffer> {
    this.screenshotCalls++;
    return Buffer.from(`fixture screenshot ${this.screenshotCalls}`);
  }

  async waitForTimeout(): Promise<void> {
    if (this.waitError !== undefined) {
      throw this.waitError;
    }
  }

  async goBack(options: { waitUntil?: string }): Promise<null> {
    this.historyWaitUntil.push(options.waitUntil ?? "");
    return null;
  }

  async goForward(options: { waitUntil?: string }): Promise<null> {
    this.historyWaitUntil.push(options.waitUntil ?? "");
    return null;
  }

  async waitForFunction(): Promise<void> {
    this.documentReadyWaits++;
  }

  async waitForEvent(): Promise<never> {
    throw new playwrightErrors.TimeoutError("no navigation");
  }

  async evaluateHandle(
    callback: (...args: any[]) => unknown,
  ): Promise<FakeJSHandle> {
    const source = String(callback);
    const target = source.includes("elementFromPoint")
      ? this.hitTestElement
      : this.activeElement;
    return new FakeJSHandle(
      target,
      (focused) => {
        this.activeElement = focused;
      },
      () => this.activeElement === target,
      this.frame,
    );
  }

  locator(selector: string): {
    ariaSnapshot: () => Promise<string>;
    innerText: () => Promise<string>;
    count: () => Promise<number>;
  } {
    return {
      ariaSnapshot: async () => {
        if (this.ariaError !== undefined) {
          throw this.ariaError;
        }
        return "";
      },
      innerText: async () => {
        if (this.bodyError !== undefined) {
          throw this.bodyError;
        }
        return "";
      },
      count: async () => (this.challengeSelectors.has(selector) ? 1 : 0),
    };
  }

  async title(): Promise<string> {
    this.titleCalls++;
    if (this.titleError !== undefined) throw this.titleError;
    return this.currentURL === "about:blank" ? "" : "Fixture page";
  }
}

class FakeChildFrame {
  hitTestElement: FakeElementState | undefined;
  activeElement: FakeElementState | undefined;
  lastHitPoint: { localX: number; localY: number } | undefined;

  constructor(private readonly currentURL: string) {}

  url(): string {
    return this.currentURL;
  }

  async evaluateHandle(
    callback: (...args: any[]) => unknown,
    argument?: { localX: number; localY: number },
  ): Promise<FakeJSHandle> {
    const source = String(callback);
    const hitTest = source.includes("elementFromPoint");
    if (hitTest) this.lastHitPoint = argument;
    const target = hitTest ? this.hitTestElement : this.activeElement;
    return new FakeJSHandle(
      target,
      (focused) => {
        this.activeElement = focused;
      },
      () => this.activeElement === target,
      this,
    );
  }
}

interface FakeInteractiveMetadata {
  tagName: string;
  type: string;
  autocomplete: string;
  label: string;
  href: string;
  role: string;
  name: string;
  placeholder: string;
  formMethod: string;
  formAction: string;
  formRole: string;
  download: boolean;
}

class FakeElementState {
  focusCalls = 0;
  clickCalls = 0;
  fillCalls = 0;
  onMetadata: (() => void) | undefined;
  onClick: (() => void) | undefined;

  constructor(
    readonly metadata: FakeInteractiveMetadata,
    public value = "",
    public selectionStart: number | null = value.length,
    public selectionEnd: number | null = value.length,
    readonly childFrame: FakeChildFrame | undefined = undefined,
    readonly bounds: { left: number; top: number } = { left: 0, top: 0 },
  ) {}
}

class FakeJSHandle {
  private readonly element: FakeElementHandle | undefined;

  constructor(
    target: FakeElementState | undefined,
    onFocus: (target: FakeElementState) => void,
    isFocused: () => boolean,
    frame: object,
  ) {
    this.element =
      target === undefined
        ? undefined
        : new FakeElementHandle(target, onFocus, isFocused, frame);
  }

  asElement(): FakeElementHandle | null {
    return this.element ?? null;
  }

  async dispose(): Promise<void> {}
}

class FakeElementHandle {
  constructor(
    private readonly target: FakeElementState,
    private readonly onFocus: (target: FakeElementState) => void,
    private readonly isFocused: () => boolean,
    private readonly frame: object,
  ) {}

  async evaluate(callback: (...args: any[]) => unknown): Promise<unknown> {
    const source = String(callback);
    if (source.includes("activeElement")) {
      return this.isFocused();
    }
    if (source.includes("getBoundingClientRect")) {
      return this.target.bounds;
    }
    if (!source.includes("selectionStart")) {
      this.target.onMetadata?.();
      return this.target.metadata;
    }
    return {
      value: this.target.value,
      selectionStart: this.target.selectionStart,
      selectionEnd: this.target.selectionEnd,
    };
  }

  async focus(): Promise<void> {
    this.target.focusCalls++;
    this.onFocus(this.target);
  }

  async click(options?: { trial?: boolean }): Promise<void> {
    if (!options?.trial) {
      this.target.clickCalls++;
      this.target.onClick?.();
    }
  }

  async ownerFrame(): Promise<object> {
    return this.frame;
  }

  async contentFrame(): Promise<FakeChildFrame | null> {
    return this.target.childFrame ?? null;
  }

  async selectOption(value: string): Promise<void> {
    this.target.value = value;
  }

  async fill(value: string): Promise<void> {
    this.target.fillCalls++;
    this.target.value = value;
  }

  async dispose(): Promise<void> {}
}

class FakeMutationRequest {
  constructor(private readonly requestMethod: string) {}

  method(): string {
    return this.requestMethod;
  }

  isNavigationRequest(): boolean {
    return false;
  }
}

class FakeMutationResponse {
  constructor(
    private readonly mutationRequest: FakeMutationRequest,
    private readonly responseStatus: number,
  ) {}

  request(): FakeMutationRequest {
    return this.mutationRequest;
  }

  status(): number {
    return this.responseStatus;
  }

  frame(): undefined {
    return undefined;
  }

  headers(): Record<string, string> {
    return {};
  }

  url(): string {
    return "https://public.example/mutation";
  }
}

function safeTextMetadata(): FakeInteractiveMetadata {
  return {
    tagName: "input",
    type: "search",
    autocomplete: "off",
    label: "Public search",
    href: "",
    role: "",
    name: "q",
    placeholder: "",
    formMethod: "get",
    formAction: "https://public.example/search",
    formRole: "search",
    download: false,
  };
}

function request(
  identity: Identity,
  actionID: string,
  kind: "navigate" | "screenshot" | "preflight" | "back" | "forward",
  observation?: ObservationMode,
): EngineRequest {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline: new Date(Date.now() + 30_000).toISOString(),
    identity,
    action:
      kind === "navigate"
        ? {
            kind,
            url: "https://public.example/redirect",
            ...(observation === undefined ? {} : { observation }),
          }
        : {
            kind,
            ...(observation === undefined ? {} : { observation }),
          },
  };
}

function actionRequest(
  identity: Identity,
  actionID: string,
  action: BrowserAction,
): EngineRequest {
  return {
    contract_id: ENGINE_CONTRACT_ID,
    action_id: actionID,
    deadline: new Date(Date.now() + 30_000).toISOString(),
    identity,
    action,
  };
}

function fixtureIdentity(index: number): Identity {
  const suffix = index.toString(16).padStart(12, "0");
  return {
    run_id: "11111111-1111-4111-8111-111111111111",
    agent_id: "22222222-2222-4222-8222-222222222222",
    principal_scope_id: "ps1_fixture_scope",
    browser_session_id: `33333333-3333-4333-8333-${suffix}`,
    session_epoch: 1,
    attachment_id: "44444444-4444-4444-8444-444444444444",
    control_epoch: 1,
    controller: "agent",
    browser_interaction_policy: "restricted",
    browser_interaction_policy_generation: 1,
    browser_mutation_origins: [],
    browser_mutation_origins_sha256:
      "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
  };
}

function fullIdentity(index: number, allowPublic: boolean): Identity {
  const origin = allowPublic
    ? "https://public.example"
    : "https://allowed.example";
  return {
    ...fixtureIdentity(index),
    browser_interaction_policy: "full",
    browser_mutation_origins: [origin],
    browser_mutation_origins_sha256: allowPublic
      ? "a91c0941e6d38c53ed130419dca7b8607a4961d70c78d55e752296600f3a9731"
      : "e07337870d6bd1ab8ab8b6609cd3197ff039a04aa646598b47ab2165b5e54ddb",
  };
}

function fullIdentityWithOrigins(index: number, origins: string[]): Identity {
  const digest =
    origins.length === 1
      ? "a91c0941e6d38c53ed130419dca7b8607a4961d70c78d55e752296600f3a9731"
      : "b180fb106b9a048b6342aac52aea0c872a608336060574375811890d07275db2";
  return {
    ...fixtureIdentity(index),
    browser_interaction_policy: "full",
    browser_mutation_origins: origins,
    browser_mutation_origins_sha256: digest,
  };
}
