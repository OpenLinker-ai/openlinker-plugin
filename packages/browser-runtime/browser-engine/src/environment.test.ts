import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  environmentEvidence,
  normalizeBrowserVersion,
  parseBrowserEnvironment,
} from "./environment.js";

const fontSHA = "a".repeat(64);
const browserVersions = JSON.parse(
  readFileSync(
    new URL("../browser-versions.json", import.meta.url),
    "utf8",
  ),
) as Record<string, string>;
const browserVersion = browserVersions["linux-amd64"] ?? "";

test("locks the image to the observed Chromium binary generation", () => {
  const registry = JSON.parse(
    readFileSync(
      new URL("../node_modules/playwright-core/browsers.json", import.meta.url),
      "utf8",
    ),
  ) as {
    browsers: Array<{ name: string; browserVersion?: string }>;
  };
  const chromium = registry.browsers.find((entry) => entry.name === "chromium");
  assert.deepEqual(Object.keys(browserVersions).sort(), [
    "linux-amd64",
    "linux-arm64",
  ]);
  for (const locked of Object.values(browserVersions)) {
    assert.match(locked, /^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/);
    assert.equal(locked.split(".", 1)[0], browserVersion.split(".", 1)[0]);
  }
  assert.equal(chromium?.browserVersion, browserVersion);
});

test("defaults to the pinned Chromium environment", () => {
  const config = parseBrowserEnvironment({
    OPENLINKER_BROWSER_VERSION: browserVersion,
    OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
  });
  assert.deepEqual(config, {
    engine: "chromium",
    distribution: "playwright_chromium",
    locale: "en-US",
    timezone: "UTC",
    browserVersion,
    fontContractVersion: "openlinker.browser.fonts.v1",
    fontManifestSHA256: fontSHA,
    profileGeneration: 1,
    maxActionsPerOriginMinute: 120,
    maxNavigationsPerOriginMinute: 20,
  });
  assert.deepEqual(environmentEvidence(config, browserVersion), {
    browser_engine: "chromium",
    browser_distribution: "playwright_chromium",
    browser_version: browserVersion,
    browser_major_version: 149,
    browser_locale: "en-US",
    browser_timezone: "UTC",
    font_contract_version: "openlinker.browser.fonts.v1",
    font_manifest_sha256: fontSHA,
  });
});

test("accepts only an explicit compatible Chrome distribution", () => {
  assert.equal(
    parseBrowserEnvironment({
      OPENLINKER_BROWSER_ENGINE: "chrome",
      OPENLINKER_BROWSER_DISTRIBUTION: "chrome_for_testing",
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
    }).distribution,
    "chrome_for_testing",
  );
  for (const environment of [
    {
      OPENLINKER_BROWSER_ENGINE: "chrome",
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
    },
    {
      OPENLINKER_BROWSER_ENGINE: "chromium",
      OPENLINKER_BROWSER_DISTRIBUTION: "google_chrome",
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
    },
    {
      OPENLINKER_BROWSER_ENGINE: "firefox",
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
    },
  ]) {
    assert.throws(() => parseBrowserEnvironment(environment));
  }
});

test("rejects invalid locale timezone version and font evidence", () => {
  for (const environment of [
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: "",
    },
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: "A".repeat(64),
    },
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
      OPENLINKER_BROWSER_LOCALE: "en US",
    },
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
      OPENLINKER_BROWSER_TIMEZONE: "../UTC",
    },
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
      OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE: "601",
    },
    {
      OPENLINKER_BROWSER_VERSION: browserVersion,
      OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
      OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE: "0",
    },
  ]) {
    assert.throws(() => parseBrowserEnvironment(environment));
  }
  const config = parseBrowserEnvironment({
    OPENLINKER_BROWSER_VERSION: browserVersion,
    OPENLINKER_BROWSER_FONT_MANIFEST_SHA256: fontSHA,
  });
  assert.throws(() => environmentEvidence(config, "150.0.1.0"));
  for (const version of ["", "149", "v149.0", "149.0 beta", "0.1"]) {
    assert.throws(() => normalizeBrowserVersion(version));
  }
});
