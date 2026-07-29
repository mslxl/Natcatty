import assert from "node:assert/strict";
import test from "node:test";

import { runTrackedTransferAttempt } from "./useSftpTransfers.ts";

test("a throwing completion handler does not leave the transfer marked in flight", async () => {
  const inFlight = new Set<string>();
  const completionHandler = async () => {
    throw new Error("completion callback failed");
  };

  await assert.rejects(
    runTrackedTransferAttempt(inFlight, "transfer-1", async () => {
      await completionHandler();
      return "completed";
    }),
    /completion callback failed/,
  );
  assert.equal(inFlight.has("transfer-1"), false);

  let reruns = 0;
  const result = await runTrackedTransferAttempt(inFlight, "transfer-1", async () => {
    reruns += 1;
    return "completed";
  });
  assert.equal(result, "completed");
  assert.equal(reruns, 1);
  assert.equal(inFlight.has("transfer-1"), false);
});
