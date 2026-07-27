import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { getQuickAddSnippetInitialCommand } from "./QuickAddSnippetDialog.tsx";

const source = readFileSync(new URL("./QuickAddSnippetDialog.tsx", import.meta.url), "utf8");

test("quick add snippet event can prefill command", () => {
  const event = {
    detail: { command: "ls -la\npwd" },
  } as CustomEvent<{ command?: string }>;

  assert.equal(getQuickAddSnippetInitialCommand(event), "ls -la\npwd");
});

test("quick add snippet event defaults to an empty command", () => {
  assert.equal(getQuickAddSnippetInitialCommand({} as Event), "");
  assert.equal(
    getQuickAddSnippetInitialCommand({
      detail: { command: 123 },
    } as unknown as Event),
    "",
  );
});

test("quick add snippet form binds shortkeys and uses a side panel drawer", () => {
  assert.match(source, /AsidePanel/);
  assert.match(source, /snippets\.field\.shortkey/);
  assert.match(source, /keyEventToString/);
  assert.match(source, /shortkey: shortkey \|\| undefined/);
  assert.match(source, /if \(e\.defaultPrevented\) return/);
  // Modal contract for hasOpenAppDialog / Cmd+W interception
  assert.match(source, /role="dialog"/);
  assert.match(source, /data-state="open"/);
  assert.match(source, /data-dialog-close="true"/);
});
