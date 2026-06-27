import assert from "node:assert/strict";
import test from "node:test";

import {
  resolveConnectionLogCapturePayload,
  resolveHibernateSnapshotCapturePayload,
  scheduleTerminalCloseDataCapture,
} from "./terminalCloseCapture.ts";

test("resolveConnectionLogCapturePayload returns null when finalize produces empty data", () => {
  assert.equal(
    resolveConnectionLogCapturePayload(() => ""),
    null,
  );
});

test("resolveConnectionLogCapturePayload returns buffered connection log data", () => {
  assert.deepEqual(
    resolveConnectionLogCapturePayload(() => "line one\r\nline two"),
    { data: "line one\r\nline two", source: "connection-log" },
  );
});

test("resolveHibernateSnapshotCapturePayload prefers combined snapshot fields", () => {
  assert.deepEqual(
    resolveHibernateSnapshotCapturePayload({
      snapshot: "full snapshot",
      viewportSnapshot: "viewport",
      scrollbackSnapshot: "scrollback",
      alternateScreen: false,
    }),
    { data: "full snapshot", source: "hibernate-serialize" },
  );

  assert.deepEqual(
    resolveHibernateSnapshotCapturePayload({
      snapshot: "",
      viewportSnapshot: "viewport",
      scrollbackSnapshot: "scrollback",
      alternateScreen: false,
    }),
    { data: "scrollbackviewport", source: "hibernate-serialize" },
  );
});

test("scheduleTerminalCloseDataCapture runs callback asynchronously", async () => {
  let ran = false;
  scheduleTerminalCloseDataCapture(() => {
    ran = true;
  });
  assert.equal(ran, false);
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
  assert.equal(ran, true);
});
