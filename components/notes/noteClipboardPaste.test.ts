import assert from "node:assert/strict";
import test from "node:test";

import {
  convertClipboardHtmlToMarkdown,
  resolveNoteClipboardPaste,
  shouldInterceptResolvedNotePaste,
  shouldInsertClipboardTextAsMarkdown,
} from "./noteClipboardPaste.ts";

test("turndown converts headings lists links and emphasis from html clipboard", () => {
  const html = `
    <html><body>
    <!--StartFragment-->
    <h1>Runbook</h1>
    <p>Restart <strong>sshd</strong> on <em>prod</em>.</p>
    <ul><li>check logs</li><li>open <a href="https://example.com">docs</a></li></ul>
    <!--EndFragment-->
    </body></html>
  `;
  const md = convertClipboardHtmlToMarkdown(html);
  assert.match(md, /^# Runbook/m);
  assert.match(md, /\*\*sshd\*\*/);
  assert.match(md, /\*prod\*/);
  assert.match(md, /check logs/);
  assert.match(md, /\[docs\]\(https:\/\/example\.com\)/);
});

test("resolve prefers html→turndown over plain when both exist (GitHub-style paste)", () => {
  // GitHub puts markdown+raw HTML in plain and rendered HTML in text/html.
  // Prefer HTML conversion so <img>/<a name> do not break MDX insertMarkdown.
  const payload = resolveNoteClipboardPaste({
    plainText: [
      "---",
      "",
      '<img width="3142" alt="shot" src="https://example.com/a.png" />',
      "",
      '<a name="catty-agent"></a>',
      "# 🔥 Catty Agent — Your IT Ops AI Partner",
      "",
      "> 🚀 **Boost your IT ops daily work with AI power.**",
      "",
      "### 🔥 What can Catty Agent do?",
      "",
      "- 🚀 **Natural language server management** — just tell it",
    ].join("\n"),
    htmlText: `
      <h1>🔥 Catty Agent — Your IT Ops AI Partner</h1>
      <blockquote><p>🚀 <strong>Boost your IT ops daily work with AI power.</strong></p></blockquote>
      <h3>🔥 What can Catty Agent do?</h3>
      <ul>
        <li>🚀 <strong>Natural language server management</strong> — just tell it</li>
      </ul>
      <img width="3142" alt="shot" src="https://example.com/a.png" />
      <a name="catty-agent"></a>
    `,
  });
  assert.equal(payload.kind, "html-converted");
  assert.match(payload.text, /Catty Agent/);
  assert.match(payload.text, /Natural language server management/);
  assert.doesNotMatch(payload.text, /<img\b/i);
  assert.doesNotMatch(payload.text, /<a\s+name=/i);
});

test("resolve uses structured plain markdown when html is absent", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "# From .md file\n\n- item",
    htmlText: "",
  });
  assert.equal(payload.kind, "markdown");
  assert.match(payload.text, /# From \.md file/);
});

test("html-converted paste is always intercepted in edit mode", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "flat text",
    htmlText: "<h2>Section</h2><p>body</p>",
  });
  assert.equal(payload.kind, "html-converted");
  assert.equal(
    shouldInterceptResolvedNotePaste({
      editorMode: "edit",
      pasteInsideCodeBlock: false,
      payload,
    }),
    true,
  );
  assert.equal(
    shouldInterceptResolvedNotePaste({
      editorMode: "preview",
      pasteInsideCodeBlock: false,
      payload,
    }),
    false,
  );
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

test("long browser html paste converts without needing markdown markers in plain", () => {
  const paragraphs = Array.from({ length: 40 }, (_, i) => (
    `<p>Paragraph ${i + 1} with <b>bold</b> text and a <a href="https://ex.com/${i}">link</a>.</p>`
  )).join("");
  const html = `<meta charset="utf-8">${paragraphs}`;
  const plain = Array.from({ length: 40 }, (_, i) => (
    `Paragraph ${i + 1} with bold text and a link.`
  )).join("\n");
  const payload = resolveNoteClipboardPaste({ plainText: plain, htmlText: html });
  assert.equal(payload.kind, "html-converted");
  assert.ok(payload.text.length > 100);
  assert.match(payload.text, /\*\*bold\*\*/);
  assert.match(payload.text, /\[link\]\(https:\/\/ex\.com\/0\)/);
});
