import assert from "node:assert/strict";
import test from "node:test";

import { parseRetryAfter } from "./site-classifier.js";

test("accepts bounded Retry-After delta and HTTP-date forms", () => {
  const now = Date.parse("2030-01-01T00:00:00Z");
  assert.equal(parseRetryAfter("30", now), 30_000);
  assert.equal(
    parseRetryAfter("Tue, 01 Jan 2030 00:02:00 GMT", now),
    120_000,
  );
  assert.equal(parseRetryAfter("3600", now), 15 * 60 * 1000);
});

test("ignores malformed, negative, past and padded Retry-After", () => {
  const now = Date.parse("2030-01-01T00:00:00Z");
  for (const value of [
    undefined,
    "",
    "-1",
    " 30",
    "not-a-date",
    "Mon, 31 Dec 2029 23:59:59 GMT",
  ]) {
    assert.equal(parseRetryAfter(value, now), undefined);
  }
});
