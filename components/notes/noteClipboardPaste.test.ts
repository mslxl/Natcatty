import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  collapseLinkedImagesToTextLinks,
  convertClipboardHtmlToMarkdown,
  convertHtmlIslandsInMarkdown,
  normalizePastedNoteMarkdown,
  resolveNoteClipboardPaste,
  serializeSafeHtmlImage,
  shouldInterceptResolvedNotePaste,
  shouldInsertClipboardTextAsMarkdown,
  NOTE_IMAGE_INTRINSIC_WIDTH_CAP,
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
`;

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

test("large screenshot images become markdown without baked pixel width", () => {
  const md = convertHtmlIslandsInMarkdown(CATTY_PASTE);
  assert.match(md, /^# 🔥 Catty Agent/m);
  assert.match(
    md,
    /!\[Screenshot 2026-07-02 at 22 51 24\]\(https:\/\/github\.com\/user-attachments\/assets\/3116165d-623a-4d3a-a28a-914befb9b72d\)/,
  );
  assert.doesNotMatch(md, /width="3142"/);
  assert.doesNotMatch(md, /height="1764"/);
  assert.doesNotMatch(md, /\\#/);
  assert.doesNotMatch(md, /<a\s+name=/i);
});

test("modest badge-sized images may keep a small html width", () => {
  assert.ok(NOTE_IMAGE_INTRINSIC_WIDTH_CAP > 150);
  assert.match(
    serializeSafeHtmlImage({
      src: "https://cdn.ko-fi.com/cdn/kofi3.png?v=2",
      alt: "Support on Ko-fi",
      width: 150,
    }),
    /<img\b[^>]*width="150"/,
  );
  assert.equal(
    serializeSafeHtmlImage({
      src: "https://example.com/big.png",
      alt: "shot",
      width: 3142,
      height: 1764,
    }),
    "![shot](https://example.com/big.png)",
  );
});

test("linked badge images collapse to clean text links", () => {
  const source = [
    "[![GitHub Release](https://img.shields.io/github/v/release/binaricat/Netcatty)](https://github.com/binaricat/Netcatty/releases/latest)",
    "",
    "[ ",
    "![Platform](https://img.shields.io/badge/Platform-macOS-blue)",
    " ](#)",
    "",
    "[",
    '<img alt="Support on Ko-fi" width="150" src="https://cdn.ko-fi.com/cdn/kofi3.png?v=2" />',
    "](https://ko-fi.com/binaricat)",
  ].join("\n");

  const md = collapseLinkedImagesToTextLinks(source);
  assert.match(md, /\[GitHub Release\]\(https:\/\/github\.com\/binaricat\/Netcatty\/releases\/latest\)/);
  assert.match(md, /\[Platform\]\(#\)/);
  assert.match(md, /\[Support on Ko-fi\]\(https:\/\/ko-fi\.com\/binaricat\)/);
  assert.doesNotMatch(md, /!\[GitHub Release\]/);
  assert.doesNotMatch(md, /^\s*\]\(/m);
});

test("normalize removes orphan link closers and oversized html img attrs", () => {
  const messy = [
    "Intro",
    "](https://example.com/orphan)",
    '<img width="2000" height="1000" alt="wide" src="https://example.com/w.png" />',
    "Done",
  ].join("\n");
  const md = normalizePastedNoteMarkdown(messy);
  assert.doesNotMatch(md, /\]\(https:\/\/example\.com\/orphan\)/);
  assert.match(md, /!\[wide\]\(https:\/\/example\.com\/w\.png\)/);
  assert.doesNotMatch(md, /width="2000"/);
});

test("resolve pastes Catty-style mixed markdown+html cleanly", () => {
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
  assert.doesNotMatch(payload.text, /width="3142"/);
});

test("repo README paste collapses shields badges without debris", () => {
  const readmeHead = readFileSync(new URL("../../README.md", import.meta.url), "utf8").slice(0, 2200);
  const payload = resolveNoteClipboardPaste({ plainText: readmeHead, htmlText: "" });
  assert.ok(payload.text.length > 50);
  // No dangling markdown link closers from badge conversion.
  assert.doesNotMatch(payload.text, /^\s*\]\([^)\n]+\)\s*$/m);
  // Badges become text links or clean images, not split wrappers.
  if (payload.text.includes("shields.io")) {
    assert.doesNotMatch(
      payload.text,
      /\[\s*\n+\s*!\[[^\]]+\]\(https:\/\/img\.shields\.io/,
    );
  }
});

test("resolve uses full turndown for browser StartFragment html", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "flat text without structure",
    htmlText: `
      <html><body>
      <!--StartFragment-->
      <h1>From browser</h1>
      <p>Hello <b>world</b></p>
      <img alt="x" src="https://cdn.example.com/x.png" width="2000" height="1000" />
      <!--EndFragment-->
      </body></html>
    `,
  });
  assert.equal(payload.kind, "html-converted");
  assert.match(payload.text, /^# From browser/m);
  assert.match(payload.text, /\*\*world\*\*/);
  assert.match(payload.text, /!\[x\]\(https:\/\/cdn\.example\.com\/x\.png\)/);
  assert.doesNotMatch(payload.text, /width="2000"/);
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
