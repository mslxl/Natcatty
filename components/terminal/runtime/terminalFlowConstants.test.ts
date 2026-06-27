import { createRequire } from "node:module";
import assert from "node:assert/strict";
import test from "node:test";

import terminalFlowConstantsJson from "../../../infrastructure/config/terminalFlowConstants.json";
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
const sharedConstantsCjs = require("../../../infrastructure/config/terminalFlowConstants.cjs") as typeof terminalFlowConstantsJson;

test("renderer flow constants match shared terminalFlowConstants.json", () => {
  assert.equal(FLOW_HIGH_WATER_MARK, terminalFlowConstantsJson.FLOW_HIGH_WATER_MARK);
  assert.equal(FLOW_LOW_WATER_MARK, terminalFlowConstantsJson.FLOW_LOW_WATER_MARK);
  assert.equal(FLOW_CHAR_COUNT_ACK_SIZE, terminalFlowConstantsJson.FLOW_CHAR_COUNT_ACK_SIZE);
  assert.equal(
    MAX_PENDING_WRITE_COALESCE_BYTES,
    terminalFlowConstantsJson.MAX_PENDING_WRITE_COALESCE_BYTES,
  );
  assert.equal(
    MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
    terminalFlowConstantsJson.MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD,
  );
  assert.equal(
    XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
    terminalFlowConstantsJson.XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES,
  );
  assert.equal(
    XTERM_WRITE_CALLBACK_BATCH_BYTES,
    terminalFlowConstantsJson.XTERM_WRITE_CALLBACK_BATCH_BYTES,
  );
  assert.deepEqual(sharedConstantsCjs, terminalFlowConstantsJson);
  assert.ok(FLOW_CHAR_COUNT_ACK_SIZE <= FLOW_LOW_WATER_MARK);
  assert.ok(MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD < MAX_PENDING_WRITE_COALESCE_BYTES);
});

test("terminal flood limits stay small enough to keep input responsive", () => {
  assert.ok(FLOW_HIGH_WATER_MARK <= 32 * 1024);
  assert.ok(FLOW_LOW_WATER_MARK <= 8 * 1024);
  assert.ok(FLOW_CHAR_COUNT_ACK_SIZE <= 4 * 1024);
  assert.ok(MAX_PENDING_WRITE_COALESCE_BYTES <= 32 * 1024);
  assert.ok(MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD <= 8 * 1024);
  assert.ok(XTERM_WRITE_CALLBACK_BATCH_BYTES <= FLOW_HIGH_WATER_MARK);
});
