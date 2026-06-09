import assert from "node:assert/strict";
import test from "node:test";

import { readFileSync } from "node:fs";

test("active tab changes notify chrome theme before react subscribers", () => {
  const storeSource = readFileSync(new URL("./activeTabStore.ts", import.meta.url), "utf8");
  const syncSource = readFileSync(new URL("./activeChromeThemeSync.ts", import.meta.url), "utf8");

  const setActiveTabIdBody = storeSource.match(/setActiveTabId = \(id: string\) => \{[\s\S]*?\n {2}\};/)?.[0] ?? "";
  assert.match(setActiveTabIdBody, /this\.syncListeners\.forEach\(\(listener\) => listener\(id\)\)/);
  assert.match(setActiveTabIdBody, /this\.scheduleNotify\(\)/);
  assert.ok(
    setActiveTabIdBody.indexOf("syncListeners.forEach") < setActiveTabIdBody.indexOf("scheduleNotify"),
    "sync chrome theme listeners must run before deferred react notify",
  );
  assert.match(syncSource, /activeTabStore\.subscribeSync\(notifyActiveChromeThemeForTab\)/);
  assert.match(syncSource, /isActiveChromeThemeResolvable/);
  assert.match(syncSource, /clearTopTabsChromeThemeVars/);
});
