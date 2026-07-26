import assert from "node:assert/strict";
import test from "node:test";

import {
  DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS,
  isTransferPoolIdleReclaimDisabled,
  resolveSftpTransferPoolIdleTtlMs,
} from "../../../infrastructure/config/sftpTransferPool";

test("default pool idle TTL is five minutes", () => {
  assert.equal(DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS, 5 * 60_000);
  assert.equal(resolveSftpTransferPoolIdleTtlMs(() => null), DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS);
});

test("accepts preset values including never-reclaim zero", () => {
  assert.equal(resolveSftpTransferPoolIdleTtlMs(() => 60_000), 60_000);
  assert.equal(resolveSftpTransferPoolIdleTtlMs(() => 0), 0);
  assert.equal(resolveSftpTransferPoolIdleTtlMs(() => 123), DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS);
});

test("zero disables idle reclaim", () => {
  assert.equal(isTransferPoolIdleReclaimDisabled(0), true);
  assert.equal(isTransferPoolIdleReclaimDisabled(300_000), false);
});
