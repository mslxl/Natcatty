const test = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const path = require("node:path");

test("before-quit dirty editor guard queries hidden app content windows", () => {
  const source = readFileSync(path.join(__dirname, "main.cjs"), "utf8");
  const lifecycleSource = readFileSync(path.join(__dirname, "main", "appLockLifecycle.cjs"), "utf8");
  const beforeQuitIndex = source.indexOf('app.on("before-quit"');
  const getAppContentWindowsIndex = source.indexOf("getAppContentWindows", beforeQuitIndex);
  const handleBeforeQuitIndex = source.indexOf("handleBeforeQuit", beforeQuitIndex);
  const handleBeforeQuitEndIndex = source.indexOf("}).catch", handleBeforeQuitIndex);
  const queryableIndex = lifecycleSource.indexOf("const queryableWebContents");
  const queryCallIndex = lifecycleSource.indexOf("queryDirtyEditors", queryableIndex);
  const lifecycleBeforeQuitIndex = lifecycleSource.indexOf("async function handleBeforeQuit");
  const lifecycleGuardSetup = lifecycleSource.slice(lifecycleBeforeQuitIndex, queryCallIndex);

  assert.notEqual(beforeQuitIndex, -1);
  assert.notEqual(getAppContentWindowsIndex, -1);
  assert.notEqual(handleBeforeQuitIndex, -1);
  assert.notEqual(handleBeforeQuitEndIndex, -1);
  assert.notEqual(lifecycleBeforeQuitIndex, -1);
  assert.notEqual(queryableIndex, -1);
  assert.ok(getAppContentWindowsIndex < handleBeforeQuitIndex);
  assert.match(source.slice(beforeQuitIndex, handleBeforeQuitIndex), /const appContentWindows = typeof getWindowManager\(\)\.getAppContentWindows === "function"/);
  assert.match(source.slice(handleBeforeQuitIndex, handleBeforeQuitEndIndex), /mainWindows,\s*\n\s*queryDirtyEditors/);
  assert.match(lifecycleGuardSetup, /const reachableMainWindows = \(Array\.isArray\(mainWindows\) \? mainWindows : \[\]\)\.filter/);
  // Prefer queryableWindows (reachable + non-crashed webContents) over a raw
  // reachableMainWindows map so dirty checks and focus targets stay aligned.
  assert.match(
    lifecycleSource.slice(queryableIndex, queryCallIndex),
    /queryableWindows\s*\n?\s*\.map\(\(candidate\) => candidate\.webContents\)/,
  );
  assert.doesNotMatch(lifecycleGuardSetup, /isVisible|isMinimized/);
});

test("macOS reopen after last window re-applies app lock for a fresh session", () => {
  const source = readFileSync(path.join(__dirname, "main.cjs"), "utf8");
  const lifecycleSource = readFileSync(path.join(__dirname, "main", "appLockLifecycle.cjs"), "utf8");

  assert.match(lifecycleSource, /function ensureAppLockForFreshSession/);
  assert.match(lifecycleSource, /function hasNoUsableAppContentWindows/);
  assert.match(source, /ensureAppLockForFreshSession/);
  assert.match(source, /hasNoUsableAppContentWindows/);

  const createIndex = source.indexOf("async function createAndShowMainWindow");
  const createBodyEnd = source.indexOf("async function deliverSshDeepLink", createIndex);
  assert.notEqual(createIndex, -1);
  assert.notEqual(createBodyEnd, -1);
  const createBody = source.slice(createIndex, createBodyEnd);
  assert.match(createBody, /hasNoUsableAppContentWindows\(appContentWindows\)/);
  assert.match(createBody, /ensureAppLockForFreshSession\(appLockController,\s*"startup"\)/);

  const allClosedIndex = source.indexOf('app.on("window-all-closed"');
  assert.notEqual(allClosedIndex, -1);
  const allClosedBody = source.slice(allClosedIndex, allClosedIndex + 350);
  assert.match(allClosedBody, /process\.platform !== "darwin"/);
  assert.match(allClosedBody, /ensureAppLockForFreshSession\(appLockController,\s*"startup"\)/);
});
