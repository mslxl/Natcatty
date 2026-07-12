import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  SCRIPT_OVERLAY_TOP_COMPACT_PX,
  SCRIPT_OVERLAY_TOP_DEFAULT_PX,
} from "./ScriptExecutionOverlay.tsx";

test("script overlay sits lower under the full host toolbar than under compact chrome", () => {
  assert.equal(SCRIPT_OVERLAY_TOP_DEFAULT_PX, 34);
  assert.equal(SCRIPT_OVERLAY_TOP_COMPACT_PX, 8);
  assert.ok(SCRIPT_OVERLAY_TOP_COMPACT_PX < SCRIPT_OVERLAY_TOP_DEFAULT_PX);
});

test("script overlay covers compact speed-dial full-width instead of reserving a right gutter", () => {
  const overlaySource = readFileSync(
    fileURLToPath(new URL("./ScriptExecutionOverlay.tsx", import.meta.url)),
    "utf8",
  );
  const terminalSource = readFileSync(
    fileURLToPath(new URL("../Terminal.tsx", import.meta.url)),
    "utf8",
  );

  assert.match(overlaySource, /compactTopChrome/);
  assert.match(overlaySource, /left-2 right-2/);
  assert.match(overlaySource, /z-40/);
  assert.doesNotMatch(overlaySource, /right-10/);
  assert.match(overlaySource, /SCRIPT_OVERLAY_TOP_COMPACT_PX/);
  assert.match(
    terminalSource,
    /compactTopChrome=\{terminalSettings\?\.showHostInfoBar === false\}/,
  );
});
