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
  assert.match(lifecycleSource.slice(queryableIndex, queryCallIndex), /reachableMainWindows\s*\n?\s*\.map\(\(candidate\) => candidate\.webContents\)/);
  assert.doesNotMatch(lifecycleGuardSetup, /isVisible|isMinimized/);
});
