import assert from "node:assert/strict";
import test from "node:test";

import {
  resolveTerminalSidePanelAutoOpen,
  type TerminalSidePanelAutoOpenTab,
} from "./terminalSidePanelAutoOpen.ts";

test("terminal side panel auto-open stays off by default", () => {
  assert.equal(
    resolveTerminalSidePanelAutoOpen({
      enabled: false,
      selectedTab: "scripts",
      sftpAvailable: true,
    }),
    null,
  );
});

test("terminal side panel auto-open returns the selected non-SFTP pane", () => {
  assert.equal(
    resolveTerminalSidePanelAutoOpen({
      enabled: true,
      selectedTab: "scripts",
      sftpAvailable: false,
    }),
    "scripts",
  );
});

test("terminal side panel auto-open skips SFTP when the session cannot use it", () => {
  assert.equal(
    resolveTerminalSidePanelAutoOpen({
      enabled: true,
      selectedTab: "sftp",
      sftpAvailable: false,
    }),
    null,
  );
});

test("terminal side panel auto-open accepts every selectable side pane", () => {
  const tabs: TerminalSidePanelAutoOpenTab[] = [
    "sftp",
    "scripts",
    "history",
    "theme",
    "system",
    "notes",
    "ai",
  ];

  assert.deepEqual(
    tabs.map((selectedTab) =>
      resolveTerminalSidePanelAutoOpen({
        enabled: true,
        selectedTab,
        sftpAvailable: true,
      }),
    ),
    tabs,
  );
});
