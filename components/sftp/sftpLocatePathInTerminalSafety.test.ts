import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sidePanelSource = readFileSync(new URL("../SftpSidePanel.tsx", import.meta.url), "utf8");
const slotSource = readFileSync(
  new URL("../terminalLayer/terminalLayerSidePanelSlots.tsx", import.meta.url),
  "utf8",
);
const connectionsSource = readFileSync(
  new URL("../../application/state/sftp/useSftpConnections.ts", import.meta.url),
  "utf8",
);
const terminalLayerSource = readFileSync(
  new URL("../TerminalLayer.tsx", import.meta.url),
  "utf8",
);

test("locate-path write skips sessions waiting on sensitive/password prompts", () => {
  assert.match(
    sidePanelSource,
    /isTerminalSensitiveInputActive\(action\.sessionId\)[\s\S]*?writeToSession\(action\.sessionId, action\.data/,
  );
  assert.match(
    sidePanelSource,
    /if \(isTerminalSensitiveInputActive\(action\.sessionId\)\) return;/,
  );
});

test("locate-path write requires an idle shell prompt before PTY injection", () => {
  assert.match(
    sidePanelSource,
    /isTerminalReadyForCommandInjection\(action\.sessionId\)[\s\S]*?writeToSession\(action\.sessionId, action\.data/,
  );
  assert.match(
    sidePanelSource,
    /if \(!isTerminalReadyForCommandInjection\(action\.sessionId\)\) return;/,
  );
});

test("locate-path uses the confirmed toolbar path rather than an optimistic navigate target", () => {
  assert.match(sidePanelSource, /getNextSftpToolbarDisplayPath\(/);
  assert.match(
    sidePanelSource,
    /path: confirmedLocatePathRef\.current \|\| connection\?\.currentPath/,
  );
});

test("locate-path uses focused session fallback when SFTP cannot reuse the terminal", () => {
  assert.match(sidePanelSource, /resolveLocateSftpPathSessionId\(\{\s*activeSessionId,\s*focusedSessionId,/);
  assert.match(
    slotSource,
    /focusedSessionId=\{panelFocusedSessionId\}/,
  );
});

test("queued uploads keep the target route separate from the live cancellation route", () => {
  assert.match(
    slotSource,
    /pendingUploadObservedRoute=\{pendingUploadObservedRoute\}/,
  );
  assert.match(
    sidePanelSource,
    /const cancellationRoute = pendingUploadObservedRoute \?\? \{/,
  );
});

test("queued uploads gate path publication by pending and bound route state", () => {
  assert.match(
    slotSource,
    /resolveSftpSidePanelPathPublication\(/,
  );
  assert.match(
    sidePanelSource,
    /routeSessionId: connection\.routeSessionId \?\? null/,
  );
});

test("dormant queued uploads cannot navigate or upload before cancellation", () => {
  assert.match(sidePanelSource, /if \(!pendingUpload\.activated\) return;/);
  assert.match(
    sidePanelSource,
    /const autoConnectPendingUpload = pendingUpload\?\.activated \? pendingUpload : null;/,
  );
  assert.doesNotMatch(
    sidePanelSource,
    /const pendingMatchesTarget = Boolean\(\s*pendingUpload\?/,
  );
});

test("route ownership is committed with the connection instead of transient pane state", () => {
  assert.match(
    connectionsSource,
    /routeSessionId: options\?\.routeSessionId/,
  );
  assert.doesNotMatch(sidePanelSource, /connectionRouteSessionIdMapRef/);
});

test("opening workspace SFTP reads path memory for the focused terminal", () => {
  assert.match(
    terminalLayerSource,
    /const sourceSessionId = getActiveTerminalSessionId\(\);[\s\S]*?resolveSftpOpenTarget\(\{\s*tabId,\s*host,\s*originSessionId: sourceSessionId,/,
  );
});
