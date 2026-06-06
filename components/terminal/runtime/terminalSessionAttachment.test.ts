import test from "node:test";
import assert from "node:assert/strict";
import type { Terminal as XTerm } from "@xterm/xterm";

import { writeSessionData } from "./terminalSessionAttachment.ts";

const createFakeTerm = (activeType = "normal") => {
  const writes: string[] = [];
  const term = {
    buffer: {
      active: { type: activeType },
    },
    write(data: string, callback?: () => void) {
      writes.push(data);
      callback?.();
    },
    scrollToBottom() {},
  } as unknown as XTerm;

  return { term, writes };
};

const createContext = (showLineTimestamps: boolean) => ({
  terminalSettingsRef: {
    current: {
      showLineTimestamps,
      scrollOnOutput: false,
      forcePromptNewLine: false,
    },
  },
  terminalSettings: {
    showLineTimestamps,
    scrollOnOutput: false,
    forcePromptNewLine: false,
  },
  terminalBackend: {},
  sessionRef: { current: "session-1" },
  promptLineBreakStateRef: { current: undefined },
});

test("writeSessionData prefixes terminal output lines when enabled", () => {
  const { term, writes } = createFakeTerm();
  writeSessionData(createContext(true) as never, term, "hello\r\nnext");

  assert.equal(writes.length, 1);
  assert.equal((writes[0].match(/\[\d{2}:\d{2}:\d{2}\]/g) ?? []).length, 2);
  assert.ok(writes[0].includes("\x1b[2;90m["));
  assert.ok(writes[0].includes("] \x1b[22;39mhello\r\n\x1b[2;90m["));
  assert.ok(writes[0].endsWith("] \x1b[22;39mnext"));
});

test("writeSessionData skips timestamps on the alternate screen", () => {
  const { term, writes } = createFakeTerm("alternate");
  writeSessionData(createContext(true) as never, term, "vim screen");

  assert.deepEqual(writes, ["vim screen"]);
});
