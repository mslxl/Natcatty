import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

const constants = require("../../../infrastructure/config/terminalFlowConstants.cjs") as {
  FLOW_HIGH_WATER_MARK: number;
  FLOW_LOW_WATER_MARK: number;
  FLOW_CHAR_COUNT_ACK_SIZE: number;
  MAX_PENDING_WRITE_COALESCE_BYTES: number;
  MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD: number;
  XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES: number;
  XTERM_WRITE_CALLBACK_BATCH_BYTES: number;
};

/**
 * Terminal output flow-control thresholds.
 *
 * Single source of truth: infrastructure/config/terminalFlowConstants.cjs
 * (aligned with VS Code FlowControlConstants).
 */
export const FLOW_HIGH_WATER_MARK = constants.FLOW_HIGH_WATER_MARK;
export const FLOW_LOW_WATER_MARK = constants.FLOW_LOW_WATER_MARK;
export const FLOW_CHAR_COUNT_ACK_SIZE = constants.FLOW_CHAR_COUNT_ACK_SIZE;
export const MAX_PENDING_WRITE_COALESCE_BYTES = constants.MAX_PENDING_WRITE_COALESCE_BYTES;
export const MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD =
  constants.MAX_PENDING_WRITE_COALESCE_BYTES_FLOOD;
export const XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES =
  constants.XTERM_WRITE_CALLBACK_FAST_PATH_MAX_BYTES;
export const XTERM_WRITE_CALLBACK_BATCH_BYTES = constants.XTERM_WRITE_CALLBACK_BATCH_BYTES;