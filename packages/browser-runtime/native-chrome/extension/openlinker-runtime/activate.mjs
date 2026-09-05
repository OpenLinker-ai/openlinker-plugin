const status = document.querySelector("#status");

chrome.runtime.sendMessage(
  { type: "openlinker.activate" },
  (response) => {
    if (chrome.runtime.lastError || response?.ready !== true) {
      status.textContent = "Browser Runtime connection unavailable.";
      return;
    }
    status.textContent = "Browser Runtime connection ready.";
  },
);
