import test from "node:test";
import assert from "node:assert/strict";

import {
  createTerminalOutputTimestampPrefixer,
  formatTerminalOutputTimestamp,
} from "./terminalOutputTimestamps.ts";

test("formats terminal output timestamps as bracketed local time", () => {
  assert.equal(
    formatTerminalOutputTimestamp(new Date(2026, 5, 6, 9, 8, 7)),
    "\x1b[2;90m[09:08:07] \x1b[22;39m",
  );
});

test("prefixes each non-empty terminal output line across chunks", () => {
  const prefixer = createTerminalOutputTimestampPrefixer({
    now: () => new Date(2026, 5, 6, 10, 11, 12),
  });

  assert.equal(prefixer.append("hello"), "\x1b[2;90m[10:11:12] \x1b[22;39mhello");
  assert.equal(prefixer.append(" world\r\nnext"), " world\r\n\x1b[2;90m[10:11:12] \x1b[22;39mnext");
  assert.equal(prefixer.append("\r\n"), "\r\n");
});

test("does not timestamp blank lines or repeated carriage-return updates", () => {
  const prefixer = createTerminalOutputTimestampPrefixer({
    now: () => new Date(2026, 5, 6, 1, 2, 3),
  });

  assert.equal(
    prefixer.append("\r\n\r\nprogress 1\rprogress 2\n"),
    "\r\n\r\n\x1b[2;90m[01:02:03] \x1b[22;39mprogress 1\rprogress 2\n",
  );
});

test("waits until printable output after leading terminal controls", () => {
  const prefixer = createTerminalOutputTimestampPrefixer({
    now: () => new Date(2026, 5, 6, 4, 5, 6),
  });

  assert.equal(
    prefixer.append("\x1b[?2004l\rpermission denied\r\n\x1b[01;32muser@host\x1b[00m$ "),
    "\x1b[?2004l\r\x1b[2;90m[04:05:06] \x1b[22;39mpermission denied\r\n\x1b[2;90m[04:05:06] \x1b[22;39m\x1b[01;32muser@host\x1b[00m$ ",
  );
});
