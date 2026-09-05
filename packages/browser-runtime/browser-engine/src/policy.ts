import { isPublicHTTPURL } from "./protocol.js";

export interface InteractiveMetadata {
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

const CREDENTIAL_AUTOCOMPLETE = new Set([
  "username",
  "email",
  "tel",
  "current-password",
  "new-password",
  "one-time-code",
  "cc-name",
  "cc-given-name",
  "cc-additional-name",
  "cc-family-name",
  "cc-number",
  "cc-exp",
  "cc-exp-month",
  "cc-exp-year",
  "cc-csc",
  "cc-type",
  "transaction-currency",
  "transaction-amount",
]);

// Text matching is defense in depth only. Structural element, input, link, and
// form attributes remain the primary Phase 1 decision boundary.
const SENSITIVE_FIELD_HINT =
  /(?:password|passwd|passcode|secret|token|api[-_ ]?key|otp|one[-_ ]?time|credit[-_ ]?card|card[-_ ]?(?:number|cvc|cvv)|密码|口令|密钥|令牌|验证码|银行卡|信用卡)/iu;

const HIGH_IMPACT_HINT =
  /(?:\b(?:buy|purchase|pay|place order|confirm order|transfer|send money|delete account|close account|publish|post publicly|sign contract|accept offer)\b|购买|支付|下单|转账|汇款|删除账户|注销账户|发布|公开发送|签署|接受报价)/iu;

const SEARCH_FIELD_HINT =
  /^(?:q|query|search|searchterm|search_term|keyword|keywords)$/i;

export function blocksNonSecretTyping(metadata: InteractiveMetadata): boolean {
  const tagName = metadata.tagName.toLowerCase();
  const type = metadata.type.toLowerCase();
  if (
    (tagName !== "input" && tagName !== "textarea") ||
    (tagName === "input" && type !== "text" && type !== "search")
  ) {
    return true;
  }
  const autocompleteTokens = metadata.autocomplete
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  if (autocompleteTokens.some((token) => CREDENTIAL_AUTOCOMPLETE.has(token))) {
    return true;
  }
  return SENSITIVE_FIELD_HINT.test(
    [
      metadata.name,
      metadata.placeholder,
      metadata.label,
    ].join(" "),
  );
}

export function blocksHighImpactActivation(metadata: InteractiveMetadata): boolean {
  if (
    metadata.tagName.toLowerCase() !== "a" ||
    metadata.download ||
    (metadata.role !== "" && metadata.role.toLowerCase() !== "link") ||
    !isPublicHTTPURL(metadata.href)
  ) {
    return true;
  }
  return HIGH_IMPACT_HINT.test(`${metadata.label} ${metadata.href}`);
}

export function allowsGetSearchSubmit(metadata: InteractiveMetadata): boolean {
  if (blocksNonSecretTyping(metadata)) {
    return false;
  }
  const searchField =
    metadata.type.toLowerCase() === "search" ||
    metadata.formRole.toLowerCase() === "search" ||
    SEARCH_FIELD_HINT.test(metadata.name);
  return (
    searchField &&
    metadata.formMethod.toLowerCase() === "get" &&
    isPublicHTTPURL(metadata.formAction)
  );
}

export function blocksKeypress(
  metadata: InteractiveMetadata,
  key: string,
): boolean {
  if (key === "Enter") {
    return !allowsGetSearchSubmit(metadata);
  }
  if (key === "Backspace" || key === "Delete") {
    return blocksNonSecretTyping(metadata);
  }
  if (key === "Tab" || key === "Escape") {
    return false;
  }
  const tagName = metadata.tagName.toLowerCase();
  if (
    tagName === "" ||
    tagName === "html" ||
    tagName === "body" ||
    tagName === "a" ||
    tagName === "textarea"
  ) {
    return false;
  }
  if (tagName === "input") {
    const type = metadata.type.toLowerCase();
    return type !== "text" && type !== "search";
  }
  // Arrow/Home/End keys change select, radio, range, spinbutton and other
  // custom widget values. Those controls are outside the Phase 1 contract.
  return true;
}

export function allowsPageRequestMethod(method: string): boolean {
  switch (method.toUpperCase()) {
    case "GET":
    case "HEAD":
    case "OPTIONS":
      return true;
    default:
      return false;
  }
}
