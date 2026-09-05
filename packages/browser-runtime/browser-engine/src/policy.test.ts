import assert from "node:assert/strict";
import test from "node:test";

import {
  allowsGetSearchSubmit,
  allowsPageRequestMethod,
  blocksHighImpactActivation,
  blocksKeypress,
  blocksNonSecretTyping,
  type InteractiveMetadata,
} from "./policy.js";

const SAFE_TEXT: InteractiveMetadata = {
  tagName: "input",
  type: "text",
  autocomplete: "",
  label: "Search",
  href: "",
  role: "",
  name: "q",
  placeholder: "Search",
  formMethod: "get",
  formAction: "https://example.com/search",
  formRole: "search",
  download: false,
};

const SAFE_LINK: InteractiveMetadata = {
  ...SAFE_TEXT,
  tagName: "a",
  type: "",
  label: "Documentation",
  href: "https://example.com/docs",
  name: "",
  placeholder: "",
  formMethod: "",
  formAction: "",
  formRole: "",
};

test("typing allows only ordinary non-sensitive text controls", () => {
  assert.equal(blocksNonSecretTyping(SAFE_TEXT), false);
  assert.equal(
    blocksNonSecretTyping({ ...SAFE_TEXT, tagName: "textarea" }),
    false,
  );
  for (const fixture of [
    { ...SAFE_TEXT, type: "password" },
    { ...SAFE_TEXT, type: "email" },
    { ...SAFE_TEXT, type: "tel" },
    { ...SAFE_TEXT, autocomplete: "section-login username" },
    { ...SAFE_TEXT, autocomplete: "one-time-code" },
    { ...SAFE_TEXT, autocomplete: "cc-number" },
    { ...SAFE_TEXT, name: "api_key" },
    { ...SAFE_TEXT, placeholder: "输入验证码" },
    { ...SAFE_TEXT, tagName: "div", role: "textbox" },
  ]) {
    assert.equal(blocksNonSecretTyping(fixture), true, JSON.stringify(fixture));
  }
});

test("activation allows only ordinary public links", () => {
  assert.equal(blocksHighImpactActivation(SAFE_LINK), false);
  for (const fixture of [
    { ...SAFE_LINK, tagName: "button", href: "" },
    { ...SAFE_LINK, tagName: "div", role: "button", href: "" },
    { ...SAFE_LINK, href: "javascript:alert(1)" },
    { ...SAFE_LINK, href: "http://127.0.0.1/" },
    { ...SAFE_LINK, download: true },
    { ...SAFE_LINK, label: "删除账户" },
    { ...SAFE_LINK, label: "Place order" },
  ]) {
    assert.equal(
      blocksHighImpactActivation(fixture),
      true,
      JSON.stringify(fixture),
    );
  }
});

test("Enter submission requires a public GET search form", () => {
  assert.equal(allowsGetSearchSubmit(SAFE_TEXT), true);
  assert.equal(
    allowsGetSearchSubmit({ ...SAFE_TEXT, type: "search", formRole: "" }),
    true,
  );
  for (const fixture of [
    { ...SAFE_TEXT, formMethod: "post" },
    { ...SAFE_TEXT, formAction: "http://127.0.0.1/search" },
    { ...SAFE_TEXT, name: "comment", formRole: "", type: "text" },
    { ...SAFE_TEXT, autocomplete: "username" },
  ]) {
    assert.equal(allowsGetSearchSubmit(fixture), false, JSON.stringify(fixture));
  }
});

test("keyboard navigation cannot mutate disallowed controls", () => {
  assert.equal(blocksKeypress(SAFE_TEXT, "Enter"), false);
  assert.equal(blocksKeypress(SAFE_TEXT, "Backspace"), false);
  assert.equal(blocksKeypress(SAFE_TEXT, "ArrowDown"), false);
  assert.equal(blocksKeypress(SAFE_TEXT, "Tab"), false);
  assert.equal(blocksKeypress(SAFE_TEXT, "Escape"), false);
  for (const fixture of [
    { ...SAFE_TEXT, tagName: "select" },
    { ...SAFE_TEXT, type: "range" },
    { ...SAFE_TEXT, type: "radio" },
    { ...SAFE_TEXT, tagName: "div", role: "listbox" },
  ]) {
    for (const key of ["ArrowDown", "ArrowUp", "Home", "End"]) {
      assert.equal(
        blocksKeypress(fixture, key),
        true,
        `${key} ${JSON.stringify(fixture)}`,
      );
    }
  }
  assert.equal(
    blocksKeypress({ ...SAFE_TEXT, autocomplete: "username" }, "Delete"),
    true,
  );
});

test("page network policy rejects state-changing methods", () => {
  for (const method of ["GET", "HEAD", "OPTIONS", "get"]) {
    assert.equal(allowsPageRequestMethod(method), true);
  }
  for (const method of ["POST", "PUT", "PATCH", "DELETE", "CONNECT"]) {
    assert.equal(allowsPageRequestMethod(method), false);
  }
});
