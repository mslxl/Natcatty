import assert from "node:assert/strict";
import test from "node:test";

import {
  convertClipboardHtmlToMarkdown,
  convertHtmlIslandsInMarkdown,
  resolveNoteClipboardPaste,
  shouldInterceptResolvedNotePaste,
  shouldInsertClipboardTextAsMarkdown,
} from "./noteClipboardPaste.ts";

const CATTY_PASTE = `---

<img width="3142" height="1764" alt="Screenshot 2026-07-02 at 22 51 24" src="https://github.com/user-attachments/assets/3116165d-623a-4d3a-a28a-914befb9b72d" />

---

<a name="catty-agent"></a>
# 🔥 Catty Agent — Your IT Ops AI Partner

> 🚀 **Boost your IT ops daily work with AI power.** Catty Agent is the built-in AI assistant that understands your servers, executes commands, and handles complex multi-host operations — all through natural conversation.
### 🔥 What can Catty Agent do?

- 🚀 **Natural language server management** — just tell it what you need, no more memorizing commands
- 🔥 **Real-time server diagnostics** — check status, inspect logs, monitor resources through conversation
- 🚀 **Multi-host orchestration** — coordinate tasks across multiple servers simultaneously
- 🔥 **Intelligent context awareness** — understands your server environment and provides tailored responses
- 🚀 **One-click complex operations** — set up clusters, deploy services, and more with simple instructions`;

test("turndown converts pure html clipboard", () => {
  const html = `
    <html><body>
    <!--StartFragment-->
    <h1>Runbook</h1>
    <p>Restart <strong>sshd</strong> on <em>prod</em>.</p>
    <ul><li>check logs</li><li>open <a href="https://example.com">docs</a></li></ul>
    <img alt="shot" src="https://example.com/a.png" />
    <!--EndFragment-->
    </body></html>
  `;
  const md = convertClipboardHtmlToMarkdown(html);
  assert.match(md, /^# Runbook/m);
  assert.match(md, /\*\*sshd\*\*/);
  assert.match(md, /\[docs\]\(https:\/\/example\.com\)/);
  assert.match(md, /!\[shot\]\(https:\/\/example\.com\/a\.png\)/);
});

test("html islands in markdown become image syntax without escaping headings", () => {
  const md = convertHtmlIslandsInMarkdown(CATTY_PASTE);
  assert.match(md, /^# 🔥 Catty Agent/m);
  assert.match(md, /^### 🔥 What can Catty Agent do\?/m);
  assert.match(md, /^> 🚀 \*\*Boost your IT ops/m);
  assert.match(md, /^- 🚀 \*\*Natural language server management\*\*/m);
  assert.match(
    md,
    /!\[Screenshot 2026-07-02 at 22 51 24\]\(https:\/\/github\.com\/user-attachments\/assets\/3116165d-623a-4d3a-a28a-914befb9b72d\)/,
  );
  // Must not escape markdown structure the way full-document turndown does.
  assert.doesNotMatch(md, /\\#/);
  assert.doesNotMatch(md, /\\\*\*/);
  assert.doesNotMatch(md, /\\---/);
  assert.doesNotMatch(md, /<img\b/i);
  assert.doesNotMatch(md, /<a\s+name=/i);
});

test("resolve pastes Catty-style mixed markdown+html from plain clipboard", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: CATTY_PASTE,
    htmlText: "",
  });
  assert.ok(payload.kind === "html-converted" || payload.kind === "markdown");
  assert.equal(
    shouldInterceptResolvedNotePaste({
      editorMode: "edit",
      pasteInsideCodeBlock: false,
      payload,
    }),
    true,
  );
  assert.match(payload.text, /^# 🔥 Catty Agent/m);
  assert.match(payload.text, /!\[Screenshot[^\]]*\]\(https:\/\/github\.com\/user-attachments/);
  assert.match(payload.text, /Natural language server management/);
});

test("resolve uses full turndown for browser StartFragment html", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "flat text without structure",
    htmlText: `
      <html><body>
      <!--StartFragment-->
      <h1>From browser</h1>
      <p>Hello <b>world</b></p>
      <img alt="x" src="https://cdn.example.com/x.png" />
      <!--EndFragment-->
      </body></html>
    `,
  });
  assert.equal(payload.kind, "html-converted");
  assert.match(payload.text, /^# From browser/m);
  assert.match(payload.text, /\*\*world\*\*/);
  assert.match(payload.text, /!\[x\]\(https:\/\/cdn\.example\.com\/x\.png\)/);
});

test("resolve uses structured plain markdown when html is absent", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "# From .md file\n\n- item",
    htmlText: "",
  });
  assert.equal(payload.kind, "markdown");
  assert.match(payload.text, /# From \.md file/);
});

test("plain unstructured text is not intercepted", () => {
  assert.equal(shouldInsertClipboardTextAsMarkdown("hello world"), false);
  const payload = resolveNoteClipboardPaste({
    plainText: "hello world",
    htmlText: "",
  });
  assert.equal(payload.kind, "plain");
  assert.equal(
    shouldInterceptResolvedNotePaste({
      editorMode: "edit",
      pasteInsideCodeBlock: false,
      payload,
    }),
    false,
  );
});
