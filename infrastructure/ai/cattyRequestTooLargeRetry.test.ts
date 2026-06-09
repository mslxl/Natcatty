import test from "node:test";
import assert from "node:assert/strict";

import {
  createCattyRequestTooLargeRetryError,
  hadToolProgressBeforeRequestTooLarge,
} from "./cattyRequestTooLargeRetry.ts";

test("createCattyRequestTooLargeRetryError marks 413 retry errors after tool progress", () => {
  const source = Object.assign(new Error("HTTP 413 Request Entity Too Large"), {
    status: 413,
    responseBody: "<html>too large</html>",
  });

  const retryError = createCattyRequestTooLargeRetryError(source, true);

  assert.equal(retryError.statusCode, 413);
  assert.equal(retryError.status, 413);
  assert.equal(retryError.responseBody, "<html>too large</html>");
  assert.equal(retryError.cause, source);
  assert.equal(hadToolProgressBeforeRequestTooLarge(retryError), true);
});

test("hadToolProgressBeforeRequestTooLarge is false when no tool progress was recorded", () => {
  const retryError = createCattyRequestTooLargeRetryError("HTTP 413", false);

  assert.equal(hadToolProgressBeforeRequestTooLarge(retryError), false);
  assert.equal(hadToolProgressBeforeRequestTooLarge(new Error("HTTP 413")), false);
});
