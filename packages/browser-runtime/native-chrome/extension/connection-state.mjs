const CONNECTION_STATES = Object.freeze({
  DISCONNECTED: "disconnected",
  RECONNECTING: "reconnecting",
  READY: "ready",
  CLOSED: "closed",
});

class NativeConnectionController {
  #cancelTimer;
  #connectNative;
  #handleRequest;
  #maxReconnectAttempts;
  #maxReconnectDelayMs;
  #port;
  #reconnectAttempts = 0;
  #reconnectBaseDelayMs;
  #scheduleTimer;
  #timer;

  constructor({
    connectNative,
    handleRequest,
    scheduleTimer = (callback, delay) => globalThis.setTimeout(callback, delay),
    cancelTimer = (timer) => globalThis.clearTimeout(timer),
    maxReconnectAttempts = 8,
    reconnectBaseDelayMs = 100,
    maxReconnectDelayMs = 5_000,
  }) {
    if (
      typeof connectNative !== "function" ||
      typeof handleRequest !== "function" ||
      typeof scheduleTimer !== "function" ||
      typeof cancelTimer !== "function" ||
      !Number.isSafeInteger(maxReconnectAttempts) ||
      maxReconnectAttempts < 1 ||
      !Number.isSafeInteger(reconnectBaseDelayMs) ||
      reconnectBaseDelayMs < 1 ||
      !Number.isSafeInteger(maxReconnectDelayMs) ||
      maxReconnectDelayMs < reconnectBaseDelayMs
    ) {
      throw new Error("native connection controller configuration is invalid");
    }
    this.#connectNative = connectNative;
    this.#handleRequest = handleRequest;
    this.#scheduleTimer = scheduleTimer;
    this.#cancelTimer = cancelTimer;
    this.#maxReconnectAttempts = maxReconnectAttempts;
    this.#reconnectBaseDelayMs = reconnectBaseDelayMs;
    this.#maxReconnectDelayMs = maxReconnectDelayMs;
  }

  state = CONNECTION_STATES.DISCONNECTED;

  get reconnectAttempts() {
    return this.#reconnectAttempts;
  }

  start() {
    if (this.state === CONNECTION_STATES.CLOSED) return this.state;
    if (this.#port === undefined && this.#timer === undefined) {
      this.#attemptConnect();
    }
    return this.state;
  }

  close() {
    if (this.state === CONNECTION_STATES.CLOSED) return;
    this.state = CONNECTION_STATES.CLOSED;
    if (this.#timer !== undefined) {
      this.#cancelTimer(this.#timer);
      this.#timer = undefined;
    }
    const port = this.#port;
    this.#port = undefined;
    try {
      port?.disconnect();
    } catch {
      // The browser may already have torn the Native port down.
    }
  }

  #attemptConnect() {
    if (this.state === CONNECTION_STATES.CLOSED || this.#port !== undefined) return;
    this.state = CONNECTION_STATES.RECONNECTING;
    let port;
    try {
      port = this.#connectNative();
      if (
        port === null ||
        typeof port !== "object" ||
        typeof port.postMessage !== "function" ||
        typeof port.onMessage?.addListener !== "function" ||
        typeof port.onDisconnect?.addListener !== "function"
      ) {
        throw new Error("native port is invalid");
      }
    } catch {
      this.state = CONNECTION_STATES.DISCONNECTED;
      this.#scheduleReconnect();
      return;
    }
    this.#port = port;
    port.onMessage.addListener((message) => {
      if (this.#port !== port || this.state === CONNECTION_STATES.CLOSED) return;
      const response = this.#handleRequest(message);
      port.postMessage(response);
      this.#reconnectAttempts = 0;
      this.state = CONNECTION_STATES.READY;
    });
    port.onDisconnect.addListener(() => {
      if (this.#port !== port || this.state === CONNECTION_STATES.CLOSED) return;
      this.#port = undefined;
      this.state = CONNECTION_STATES.DISCONNECTED;
      this.#scheduleReconnect();
    });
  }

  #scheduleReconnect() {
    if (
      this.state === CONNECTION_STATES.CLOSED ||
      this.#timer !== undefined
    ) {
      return;
    }
    if (this.#reconnectAttempts >= this.#maxReconnectAttempts) {
      this.state = CONNECTION_STATES.CLOSED;
      return;
    }
    const delay = Math.min(
      this.#maxReconnectDelayMs,
      this.#reconnectBaseDelayMs * 2 ** this.#reconnectAttempts,
    );
    this.#reconnectAttempts += 1;
    this.#timer = this.#scheduleTimer(() => {
      this.#timer = undefined;
      this.#attemptConnect();
    }, delay);
  }
}

export { CONNECTION_STATES, NativeConnectionController };
