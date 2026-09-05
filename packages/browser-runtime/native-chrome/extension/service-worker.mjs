import { NATIVE_HOST_NAME, handleNativeRequest } from "./protocol.mjs";
import {
  CONNECTION_STATES,
  NativeConnectionController,
} from "./connection-state.mjs";

const controller = new NativeConnectionController({
  connectNative: () => chrome.runtime.connectNative(NATIVE_HOST_NAME),
  handleRequest: (message) =>
    handleNativeRequest(
      message,
      chrome.runtime.getManifest(),
      chrome.runtime.id,
    ),
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (
    sender.id !== chrome.runtime.id ||
    message === null ||
    typeof message !== "object" ||
    Array.isArray(message) ||
    Object.keys(message).length !== 1 ||
    message.type !== "openlinker.activate"
  ) {
    sendResponse({ ready: false });
    return false;
  }
  controller.start();
  sendResponse({ ready: controller.state === CONNECTION_STATES.READY });
  return false;
});

chrome.runtime.onSuspend?.addListener(() => controller.close());

controller.start();
