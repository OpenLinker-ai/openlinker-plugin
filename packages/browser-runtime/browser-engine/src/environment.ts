import type { EnvironmentEvidence } from "./protocol.js";

export const DEFAULT_BROWSER_ENGINE = "chromium";
export const DEFAULT_BROWSER_DISTRIBUTION = "playwright_chromium";
export const DEFAULT_BROWSER_LOCALE = "en-US";
export const DEFAULT_BROWSER_TIMEZONE = "UTC";
export const DEFAULT_FONT_CONTRACT_VERSION = "openlinker.browser.fonts.v1";
export const DEFAULT_MAX_ACTIONS_PER_ORIGIN_MINUTE = 120;
export const DEFAULT_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE = 20;

export interface BrowserEnvironmentConfig {
  engine: "chromium" | "chrome";
  distribution:
    | "playwright_chromium"
    | "google_chrome"
    | "chrome_for_testing";
  locale: string;
  timezone: string;
  browserVersion: string;
  fontContractVersion: string;
  fontManifestSHA256: string;
  profileGeneration: number;
  maxActionsPerOriginMinute: number;
  maxNavigationsPerOriginMinute: number;
  executablePath?: string;
}

export function parseBrowserEnvironment(
  environment: NodeJS.ProcessEnv,
): BrowserEnvironmentConfig {
  const engine = environment.OPENLINKER_BROWSER_ENGINE ?? DEFAULT_BROWSER_ENGINE;
  if (engine !== "chromium" && engine !== "chrome") {
    throw new Error("OPENLINKER_BROWSER_ENGINE must be chromium or chrome");
  }
  const defaultDistribution =
    engine === "chromium" ? DEFAULT_BROWSER_DISTRIBUTION : "";
  const distribution =
    environment.OPENLINKER_BROWSER_DISTRIBUTION ?? defaultDistribution;
  if (
    (engine === "chromium" && distribution !== "playwright_chromium") ||
    (engine === "chrome" &&
      distribution !== "google_chrome" &&
      distribution !== "chrome_for_testing")
  ) {
    throw new Error("Browser engine and distribution are incompatible");
  }
  const locale =
    environment.OPENLINKER_BROWSER_LOCALE ?? DEFAULT_BROWSER_LOCALE;
  if (!isLocale(locale)) {
    throw new Error("OPENLINKER_BROWSER_LOCALE is invalid");
  }
  const timezone =
    environment.OPENLINKER_BROWSER_TIMEZONE ?? DEFAULT_BROWSER_TIMEZONE;
  if (!isTimezone(timezone)) {
    throw new Error("OPENLINKER_BROWSER_TIMEZONE is invalid");
  }
  const browserVersion = normalizeBrowserVersion(
    environment.OPENLINKER_BROWSER_VERSION ?? "",
  );
  const fontContractVersion =
    environment.OPENLINKER_BROWSER_FONT_CONTRACT_VERSION ??
    DEFAULT_FONT_CONTRACT_VERSION;
  if (!isOpaqueIdentifier(fontContractVersion, 64)) {
    throw new Error("OPENLINKER_BROWSER_FONT_CONTRACT_VERSION is invalid");
  }
  const fontManifestSHA256 =
    environment.OPENLINKER_BROWSER_FONT_MANIFEST_SHA256 ?? "";
  if (!/^[0-9a-f]{64}$/.test(fontManifestSHA256)) {
    throw new Error("OPENLINKER_BROWSER_FONT_MANIFEST_SHA256 is invalid");
  }
  const profileGeneration = configuredInteger(
    environment.OPENLINKER_BROWSER_PROFILE_GENERATION,
    1,
    1,
    Number.MAX_SAFE_INTEGER,
    "OPENLINKER_BROWSER_PROFILE_GENERATION",
  );
  const maxActionsPerOriginMinute = configuredInteger(
    environment.OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE,
    DEFAULT_MAX_ACTIONS_PER_ORIGIN_MINUTE,
    1,
    600,
    "OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE",
  );
  const maxNavigationsPerOriginMinute = configuredInteger(
    environment.OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE,
    DEFAULT_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE,
    1,
    60,
    "OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE",
  );
  const executablePath = configuredExecutablePath(
    environment.OPENLINKER_BROWSER_EXECUTABLE_PATH,
    engine,
  );
  return {
    engine,
    distribution: distribution as BrowserEnvironmentConfig["distribution"],
    locale,
    timezone,
    browserVersion,
    fontContractVersion,
    fontManifestSHA256,
    profileGeneration,
    maxActionsPerOriginMinute,
    maxNavigationsPerOriginMinute,
    ...(executablePath === undefined ? {} : { executablePath }),
  };
}

function configuredExecutablePath(
  raw: string | undefined,
  engine: "chromium" | "chrome",
): string | undefined {
  if (raw === undefined || raw === "") return undefined;
  if (
    engine !== "chrome" ||
    !raw.startsWith("/") ||
    raw.includes("\0") ||
    raw.split("/").includes("..")
  ) {
    throw new Error("OPENLINKER_BROWSER_EXECUTABLE_PATH is invalid");
  }
  return raw;
}

function configuredInteger(
  raw: string | undefined,
  fallback: number,
  minimum: number,
  maximum: number,
  label: string,
): number {
  const value = raw ?? String(fallback);
  if (!/^[1-9][0-9]*$/.test(value)) {
    throw new Error(`${label} is invalid`);
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${label} is invalid`);
  }
  return parsed;
}

export function environmentEvidence(
  config: BrowserEnvironmentConfig,
  browserVersion: string,
): EnvironmentEvidence {
  const normalizedVersion = normalizeBrowserVersion(browserVersion);
  if (normalizedVersion !== config.browserVersion) {
    throw new Error("Browser version does not match the locked image configuration");
  }
  return {
    browser_engine: config.engine,
    browser_distribution: config.distribution,
    browser_version: normalizedVersion,
    browser_major_version: Number.parseInt(
      normalizedVersion.split(".", 1)[0] ?? "",
      10,
    ),
    browser_locale: config.locale,
    browser_timezone: config.timezone,
    font_contract_version: config.fontContractVersion,
    font_manifest_sha256: config.fontManifestSHA256,
  };
}

export function normalizeBrowserVersion(raw: string): string {
  const value = raw.trim();
  if (
    value.length > 64 ||
    !/^[1-9][0-9]{0,3}(?:\.[0-9]{1,8}){1,3}$/.test(value)
  ) {
    throw new Error("Browser version is invalid");
  }
  return value;
}

function isLocale(value: string): boolean {
  return (
    value.length >= 2 &&
    value.length <= 64 &&
    /^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$/.test(value)
  );
}

function isTimezone(value: string): boolean {
  return (
    value.length >= 1 &&
    value.length <= 128 &&
    !value.startsWith("/") &&
    !value.includes("..") &&
    /^[A-Za-z0-9_+\-/]+$/.test(value)
  );
}

function isOpaqueIdentifier(value: string, maximum: number): boolean {
  return (
    value.length >= 1 &&
    value.length <= maximum &&
    /^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(value)
  );
}
