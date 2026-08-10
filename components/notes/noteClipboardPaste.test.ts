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

test("centered README hero blocks wrap in div align=center", () => {
  const html = `
    <p align="center">
      <img src="public/icon.png" alt="Netcatty" width="128" height="128">
    </p>
    <h1 align="center">Netcatty</h1>
    <p align="center">
      <strong>🔥 AI-Powered SSH Client</strong><br/>
      <a href="https://netcatty.app">netcatty.app</a>
    </p>
  `;
  const md = convertClipboardHtmlToMarkdown(html);
  assert.match(md, /<div align="center">/);
  assert.match(md, /<\/div>/);
  assert.match(md, /width="128"/);
  assert.match(md, /height="128"/);
  assert.match(md, /# Netcatty/);
  assert.match(md, /netcatty\.app/);
  // Center wrapper should appear before the logo / title content.
  const centerIdx = md.indexOf('<div align="center">');
  const logoIdx = md.search(/icon\.png|# Netcatty/);
  assert.ok(centerIdx >= 0 && logoIdx >= 0 && centerIdx < logoIdx);
});

test("island conversion keeps center on p align=center with image", () => {
  const plain = `
<p align="center">
  <img src="public/icon.png" alt="Netcatty" width="128" height="128">
</p>

<h1 align="center">Netcatty</h1>
`;
  const md = convertHtmlIslandsInMarkdown(plain);
  assert.match(md, /<div align="center">/);
  assert.match(md, /width="128"/);
  assert.match(md, /Netcatty/);
});

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

test("screenshot images keep width and height attributes", () => {
  const md = convertHtmlIslandsInMarkdown(CATTY_PASTE);
  assert.match(md, /^# 🔥 Catty Agent/m);
  assert.match(
    md,
    /<img\b[^>]*src="https:\/\/github\.com\/user-attachments\/assets\/3116165d-623a-4d3a-a28a-914befb9b72d"/,
  );
  assert.match(md, /width="3142"/);
  assert.match(md, /height="1764"/);
  assert.match(md, /alt="Screenshot 2026-07-02 at 22 51 24"/);
  assert.doesNotMatch(md, /\\#/);
  assert.doesNotMatch(md, /<a\s+name=/i);
});

test("serializeSafeHtmlImage preserves dimensions when present", () => {
  assert.equal(
    serializeSafeHtmlImage({
      src: "https://example.com/a.png",
      alt: "shot",
    }),
    "![shot](https://example.com/a.png)",
  );
  assert.match(
    serializeSafeHtmlImage({
      src: "https://example.com/a.png",
      alt: "shot",
      width: 3142,
      height: 1764,
    }),
    /<img\b[^>]*width="3142"[^>]*height="1764"[^>]*\/>/,
  );
  assert.match(
    serializeSafeHtmlImage({
      src: "https://cdn.ko-fi.com/cdn/kofi3.png?v=2",
      alt: "Support on Ko-fi",
      width: 150,
    }),
    /width="150"/,
  );
  assert.match(
    serializeSafeHtmlImage({
      src: "https://example.com/icon.png",
      alt: "icon",
      height: 24,
    }),
    /<img\b[^>]*height="24"[^>]*\/>/,
  );
});

test("serializeSafeHtmlImage rejects unresolved relative image sources", () => {
  assert.equal(
    serializeSafeHtmlImage({ src: "./docs/screenshot.png", alt: "shot" }),
    "",
  );
  assert.equal(
    serializeSafeHtmlImage({ src: "../assets/logo.png", alt: "logo" }),
    "",
  );
  assert.equal(
    serializeSafeHtmlImage({ src: "/assets/logo.png", alt: "logo" }),
    "",
  );
  assert.equal(
    serializeSafeHtmlImage({ src: "docs/screenshot.png", alt: "shot" }),
    "",
  );
  assert.equal(
    serializeSafeHtmlImage({
      src: "//cdn.example.com/a.png",
      alt: "cdn",
    }),
    "![cdn](//cdn.example.com/a.png)",
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

test("normalize removes orphan link closers but keeps image dimensions", () => {
  const messy = [
    "Intro",
    "](https://example.com/orphan)",
    '<img width="2000" height="1000" alt="wide" src="https://example.com/w.png" />',
    "Done",
  ].join("\n");
  const md = normalizePastedNoteMarkdown(messy);
  assert.doesNotMatch(md, /\]\(https:\/\/example\.com\/orphan\)/);
  assert.match(md, /src="https:\/\/example\.com\/w\.png"/);
  assert.match(md, /width="2000"/);
  assert.match(md, /height="1000"/);
});

test("normalize keeps link-closer lines inside fenced and indented code", () => {
  const source = [
    "Before",
    "](https://example.com/orphan)",
    "```md",
    "](https://example.com)",
    "```",
    "",
    "    ](https://example.com/indented)",
    "After",
  ].join("\n");
  const md = normalizePastedNoteMarkdown(source);
  assert.doesNotMatch(md, /^\]\(https:\/\/example\.com\/orphan\)$/m);
  assert.match(md, /```md\n\]\(https:\/\/example\.com\)\n```/);
  assert.match(md, /^ {4}\]\(https:\/\/example\.com\/indented\)$/m);
});

test("resolve pastes Catty-style mixed markdown+html with image sizes", () => {
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
  assert.match(payload.text, /width="3142"/);
  assert.match(payload.text, /height="1764"/);
});

test("repo README paste collapses shields badges without debris", () => {
  const readmeHead = readFileSync(new URL("../../README.md", import.meta.url), "utf8").slice(0, 2200);
  const payload = resolveNoteClipboardPaste({ plainText: readmeHead, htmlText: "" });
  assert.ok(payload.text.length > 50);
  assert.doesNotMatch(payload.text, /^\s*\]\([^)\n]+\)\s*$/m);
  // Large screenshot keeps dimensions in source.
  assert.match(payload.text, /width="3142"/);
  assert.match(payload.text, /height="1764"/);
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
  assert.match(payload.text, /src="https:\/\/cdn\.example\.com\/x\.png"/);
  assert.match(payload.text, /width="2000"/);
  assert.match(payload.text, /height="1000"/);
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
