import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./useSessionState.ts", import.meta.url), "utf8");

test("active-tab changes use patchSessionRestoreActiveTabId not full schedulePersist", () => {
  assert.match(source, /patchSessionRestoreActiveTabId/);
  assert.match(source, /scheduleActiveTabPatch/);
  // Active-tab subscription must call the patch scheduler, not the full persist.
  assert.match(
    source,
    /activeTabStore\.subscribeSync\(scheduleActiveTabPatch\)/,
  );
  // Full structural persist remains for sessions/workspaces/tabOrder + pagehide.
  assert.match(source, /scheduleSessionRestorePersistRef\.current = schedulePersist/);
  assert.match(source, /pagehide/);
  assert.match(source, /beforeunload/);
});
