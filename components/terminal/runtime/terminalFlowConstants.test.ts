import assert from "node:assert/strict";
import test from "node:test";

import {
  FLOW_CHAR_COUNT_ACK_SIZE,
  FLOW_HIGH_WATER_MARK,
  FLOW_LOW_WATER_MARK,
} from "./terminalFlowConstants.ts";

test("renderer flow constants match VS Code FlowControlConstants", () => {
  assert.equal(FLOW_HIGH_WATER_MARK, 100_000);
  assert.equal(FLOW_LOW_WATER_MARK, 5_000);
  assert.equal(FLOW_CHAR_COUNT_ACK_SIZE, 5_000);
  assert.ok(FLOW_CHAR_COUNT_ACK_SIZE <= FLOW_LOW_WATER_MARK);
});