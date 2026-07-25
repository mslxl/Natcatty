import assert from "node:assert/strict";
import test from "node:test";

import { hasNewSourceFingerprint, shouldApplyTransferProgress } from "./transferProgressMetadata";

test("source fingerprint metadata changes bypass ordinary progress throttling", () => {
  assert.equal(hasNewSourceFingerprint("sha256:old", "sha256:new"), true);
  assert.equal(hasNewSourceFingerprint("sha256:same", "sha256:same"), false);
  assert.equal(shouldApplyTransferProgress({
    elapsedMs: 10,
    transferred: 20,
    total: 100,
    incomingSourceFingerprint: "sha256:new",
  }), true);
  assert.equal(shouldApplyTransferProgress({
    elapsedMs: 10,
    transferred: 20,
    total: 100,
  }), false);
});
