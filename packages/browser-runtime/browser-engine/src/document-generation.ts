import type {
  BrowserContext,
  CDPSession,
  Page,
} from "playwright-core";

export class DocumentGenerationTracker {
  private readonly session: CDPSession;
  private readonly mainFrameID: string;
  private loaderID: string;
  private generationValue = 1;
  private healthyValue = true;
  private intentionalDetach = false;

  private constructor(
    session: CDPSession,
    mainFrameID: string,
    loaderID: string,
  ) {
    this.session = session;
    this.mainFrameID = mainFrameID;
    this.loaderID = loaderID;
    session.on("Page.frameNavigated", (event) => {
      if (
        !this.healthyValue ||
        event.frame.id !== this.mainFrameID ||
        event.frame.loaderId === undefined ||
        event.frame.loaderId === "" ||
        event.frame.loaderId === this.loaderID
      ) {
        return;
      }
      this.loaderID = event.frame.loaderId;
      this.generationValue++;
    });
    session.on("close", () => {
      if (!this.intentionalDetach) {
        this.healthyValue = false;
      }
    });
  }

  static async create(
    context: BrowserContext,
    page: Page,
  ): Promise<DocumentGenerationTracker> {
    const session = await context.newCDPSession(page);
    try {
      await session.send("Page.enable");
      const frameTree = await session.send("Page.getFrameTree");
      const frame = frameTree.frameTree.frame;
      if (
        typeof frame.id !== "string" ||
        frame.id === "" ||
        typeof frame.loaderId !== "string" ||
        frame.loaderId === ""
      ) {
        throw new Error("main-frame loader identity is unavailable");
      }
      return new DocumentGenerationTracker(
        session,
        frame.id,
        frame.loaderId,
      );
    } catch (error) {
      await session.detach().catch(() => undefined);
      throw error;
    }
  }

  get generation(): number {
    return this.generationValue;
  }

  get healthy(): boolean {
    return this.healthyValue;
  }

  async detach(): Promise<void> {
    if (!this.healthyValue) {
      return;
    }
    this.intentionalDetach = true;
    this.healthyValue = false;
    await this.session.detach().catch(() => undefined);
  }
}
