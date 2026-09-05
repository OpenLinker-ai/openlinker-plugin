import type { Page } from "playwright-core";

export const CLASSIFIER_RULES_VERSION =
  "openlinker.browser.challenge-rules.v1";

export type ChallengeConfidence = "none" | "suspected" | "required";

const REQUIRED_SELECTORS = [
  'iframe[src^="https://challenges.cloudflare.com/turnstile/"]',
  'iframe[src^="https://newassets.hcaptcha.com/captcha/"]',
  'iframe[src^="https://www.google.com/recaptcha/"]',
] as const;

const SUSPECTED_SELECTORS = [
  '[id*="captcha" i]',
  '[class*="captcha" i]',
  '[data-testid*="challenge" i]',
  'iframe[title*="challenge" i]',
] as const;

export async function classifyChallenge(
  page: Page,
): Promise<ChallengeConfidence> {
  for (const selector of REQUIRED_SELECTORS) {
    if ((await page.locator(selector).count()) > 0) {
      return "required";
    }
  }
  for (const selector of SUSPECTED_SELECTORS) {
    if ((await page.locator(selector).count()) > 0) {
      return "suspected";
    }
  }
  return "none";
}

export function parseRetryAfter(
  raw: string | undefined,
  now: number,
): number | undefined {
  if (raw === undefined || raw.trim() !== raw || raw === "") {
    return undefined;
  }
  let delay: number;
  if (/^[0-9]+$/.test(raw)) {
    delay = Number.parseInt(raw, 10) * 1000;
  } else {
    const deadline = Date.parse(raw);
    if (!Number.isFinite(deadline)) {
      return undefined;
    }
    delay = deadline - now;
  }
  if (!Number.isSafeInteger(delay) || delay <= 0) {
    return undefined;
  }
  return Math.min(delay, 15 * 60 * 1000);
}
