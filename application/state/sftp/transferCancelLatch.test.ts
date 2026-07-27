import assert from "node:assert/strict";
import test from "node:test";

import {
  clearTransferCancelledTree,
  isTransferCancelledFlag,
  isTransferOrRootCancelled,
  markTransferCancelledTree,
  resetTransferCancelLatchesForTests,
} from "./transferCancelLatch";

test("cancel flags are process-global for root and children", () => {
  resetTransferCancelLatchesForTests();
  markTransferCancelledTree("parent", ["c1", "c2"]);
  assert.equal(isTransferCancelledFlag("parent"), true);
  assert.equal(isTransferOrRootCancelled("parent", "c1"), true);
  assert.equal(isTransferCancelledFlag("other"), false);
  clearTransferCancelledTree("parent", ["c1", "c2"]);
  assert.equal(isTransferCancelledFlag("parent"), false);
  assert.equal(isTransferCancelledFlag("c1"), false);
});
