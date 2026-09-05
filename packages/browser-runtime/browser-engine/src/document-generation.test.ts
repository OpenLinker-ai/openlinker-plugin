import assert from "node:assert/strict";
import test from "node:test";

import type {
  BrowserContext,
  CDPSession,
  Page,
} from "playwright-core";

import { DocumentGenerationTracker } from "./document-generation.js";

test("advances only for a different trusted main-frame loader", async () => {
  const session = new FakeCDPSession("frame-main", "loader-a");
  const tracker = await DocumentGenerationTracker.create(
    {
      newCDPSession: async () => session as unknown as CDPSession,
    } as unknown as BrowserContext,
    {} as Page,
  );
  assert.equal(tracker.generation, 1);
  session.emit("Page.navigatedWithinDocument", {
    frameId: "frame-main",
    url: "https://example.com/spa",
  });
  session.emit("Page.frameNavigated", {
    frame: {
      id: "frame-child",
      loaderId: "loader-child",
      url: "https://example.com/frame",
      domainAndRegistry: "",
      securityOrigin: "https://example.com",
      mimeType: "text/html",
      secureContextType: "Secure",
      crossOriginIsolatedContextType: "NotIsolated",
      gatedAPIFeatures: [],
    },
    type: "Navigation",
  });
  session.emit("Page.frameNavigated", {
    frame: {
      id: "frame-main",
      loaderId: "loader-a",
      url: "https://example.com/back",
      domainAndRegistry: "",
      securityOrigin: "https://example.com",
      mimeType: "text/html",
      secureContextType: "Secure",
      crossOriginIsolatedContextType: "NotIsolated",
      gatedAPIFeatures: [],
    },
    type: "BackForwardCacheRestore",
  });
  assert.equal(tracker.generation, 1);
  session.emit("Page.frameNavigated", {
    frame: {
      id: "frame-main",
      loaderId: "loader-b",
      url: "https://example.com/other",
      domainAndRegistry: "",
      securityOrigin: "https://example.com",
      mimeType: "text/html",
      secureContextType: "Secure",
      crossOriginIsolatedContextType: "NotIsolated",
      gatedAPIFeatures: [],
    },
    type: "Navigation",
  });
  assert.equal(tracker.generation, 2);
  session.emit("Page.frameNavigated", {
    frame: {
      id: "frame-main",
      loaderId: "loader-a",
      url: "https://example.com/back",
      domainAndRegistry: "",
      securityOrigin: "https://example.com",
      mimeType: "text/html",
      secureContextType: "Secure",
      crossOriginIsolatedContextType: "NotIsolated",
      gatedAPIFeatures: [],
    },
    type: "BackForwardCacheRestore",
  });
  assert.equal(tracker.generation, 3);
  session.emit("Page.frameNavigated", {
    frame: {
      id: "frame-main",
      loaderId: "loader-b",
      url: "https://example.com/other",
      domainAndRegistry: "",
      securityOrigin: "https://example.com",
      mimeType: "text/html",
      secureContextType: "Secure",
      crossOriginIsolatedContextType: "NotIsolated",
      gatedAPIFeatures: [],
    },
    type: "BackForwardCacheRestore",
  });
  assert.equal(tracker.generation, 4);
});

test("fails preflight without a baseline loader and marks runtime loss", async () => {
  const missing = new FakeCDPSession("frame-main", "");
  await assert.rejects(() =>
    DocumentGenerationTracker.create(
      {
        newCDPSession: async () => missing as unknown as CDPSession,
      } as unknown as BrowserContext,
      {} as Page,
    ),
  );

  const session = new FakeCDPSession("frame-main", "loader-a");
  const tracker = await DocumentGenerationTracker.create(
    {
      newCDPSession: async () => session as unknown as CDPSession,
    } as unknown as BrowserContext,
    {} as Page,
  );
  session.emit("close", session);
  assert.equal(tracker.healthy, false);
});

class FakeCDPSession {
  private readonly listeners = new Map<string, Array<(payload: any) => void>>();

  constructor(
    private readonly frameID: string,
    private readonly loaderID: string,
  ) {}

  on(event: string, listener: (payload: any) => void): this {
    const listeners = this.listeners.get(event) ?? [];
    listeners.push(listener);
    this.listeners.set(event, listeners);
    return this;
  }

  emit(event: string, payload: any): void {
    for (const listener of this.listeners.get(event) ?? []) {
      listener(payload);
    }
  }

  async send(method: string): Promise<any> {
    if (method === "Page.enable") {
      return {};
    }
    if (method === "Page.getFrameTree") {
      return {
        frameTree: {
          frame: {
            id: this.frameID,
            loaderId: this.loaderID,
          },
        },
      };
    }
    throw new Error("unexpected CDP command");
  }

  async detach(): Promise<void> {
    this.emit("close", this);
  }
}
