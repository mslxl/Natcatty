import assert from "node:assert/strict";
import test from "node:test";

import {
  bumpTransferControlEpoch,
  getTransferControlEpoch,
  isTransferControlEpochCurrent,
  resetTransferControlEpochsForTests,
} from "./transferControlEpoch";

test("control epoch starts at 0 and bumps monotonically per id", () => {
  resetTransferControlEpochsForTests();
  assert.equal(getTransferControlEpoch("a"), 0);
  assert.equal(bumpTransferControlEpoch("a"), 1);
  assert.equal(bumpTransferControlEpoch("a"), 2);
  assert.equal(getTransferControlEpoch("a"), 2);
  assert.equal(getTransferControlEpoch("b"), 0);
  assert.equal(isTransferControlEpochCurrent("a", 2), true);
  assert.equal(isTransferControlEpochCurrent("a", 1), false);
});

test("resume-style bump invalidates a captured pause epoch", () => {
  resetTransferControlEpochsForTests();
  const pauseEpoch = bumpTransferControlEpoch("folder");
  assert.equal(isTransferControlEpochCurrent("folder", pauseEpoch), true);
  // User hits resume immediately.
  bumpTransferControlEpoch("folder");
  assert.equal(isTransferControlEpochCurrent("folder", pauseEpoch), false);
});
