import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sidePanelSource = readFileSync(new URL("../SftpSidePanel.tsx", import.meta.url), "utf8");

test("SFTP side panel rebinds after same-tab SSH start-over", () => {
  assert.match(sidePanelSource, /shouldRebindSftpSidePanelSourceSession\(/);
  assert.match(sidePanelSource, /shouldDeferSftpSidePanelAutoConnectForSession\(/);
  assert.match(sidePanelSource, /lastSourceSessionStatusRef/);
  assert.match(
    sidePanelSource,
    /previousStatus:\s*lastSourceSessionStatusRef\.current/,
  );
});
