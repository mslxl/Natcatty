import { createRequire } from "node:module";
import assert from "node:assert/strict";
import test from "node:test";

import {
  FLOW_CHAR_COUNT_ACK_SIZE,
  FLOW_HIGH_WATER_MARK,
  FLOW_LOW_WATER_MARK,
  MAX_PENDING_WRITE_COALESCE_BYTES,
  MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
  XTERM_WRITE_CALLBACK_BATCH_BYTES,
  XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
} from "./terminalFlowConstants.ts";

const require = createRequire(import.meta.url);
const sharedConstants = require("../../../infrastructure/config/terminalFlowConstants.cjs") as {
  FLOW_HIGH_WATER_MARK: number;
  FLOW_LOW_WATER_MARK: number;
  FLOW_CHAR_COUNT_ACK_SIZE: number;
  MAX_PENDING_WRITE_COALESCE_BYTES: number;
  MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD: number;
  XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES: number;
  XTERM_WRITE_CALLBACK_BATCH_BYTES: number;
};

test("renderer flow constants match shared terminalFlowConstants.cjs", () => {
  assert.equal(FLOW_HIGH_WATER_MARK, sharedConstants.FLOW_HIGH_WATER_MARK);
  assert.equal(FLOW_LOW_WATER_MARK, sharedConstants.FLOW_LOW_WATER_MARK);
  assert.equal(FLOW_CHAR_COUNT_ACK_SIZE, sharedConstants.FLOW_CHAR_COUNT_ACK_SIZE);
  assert.equal(
    MAX_PENDING_WRITE_COALESCE_BYTES,
    sharedConstants.MAX_PENDING_WRITE_COALESCE_BYTES,
  );
  assert.equal(
    MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
    sharedConstants.MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
  );
  assert.equal(
    XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
    sharedConstants.XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
  );
  assert.equal(
    XTERM_WRITE_CALLBACK_BATCH_BYTES,
    sharedConstants.XTERM_WRITE_CALLBACK_BATCH_BYTES,
  );
  assert.ok(FLOW_CHAR_COUNT_ACK_SIZE <= FLOW_LOW_WATER_MARK);
  assert.ok(MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD < MAX_PENDING_WRITE_COALESCE_BYTES);
});