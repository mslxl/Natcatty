import assert from "node:assert/strict";
import test from "node:test";

import {
  convertClipboardHtmlToMarkdown,
  resolveNoteClipboardPaste,
  shouldInterceptResolvedNotePaste,
  shouldInsertClipboardTextAsMarkdown,
} from "./noteClipboardPaste.ts";

test("html clipboard converts headings lists links and emphasis", () => {
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
  assert.match(md, /^- check logs/m);
  assert.match(md, /\[docs\]\(https:\/\/example\.com\)/);
});

test("resolve prefers structured plain markdown over html", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "# From .md file\n\n- item",
    htmlText: "<h1>HTML title</h1>",
  });
  assert.equal(payload.kind, "markdown");
  assert.match(payload.text, /# From \.md file/);
});

test("resolve converts html when plain is unstructured", () => {
  const payload = resolveNoteClipboardPaste({
    plainText: "Runbook\nRestart sshd on prod.",
    htmlText: "<h1>Runbook</h1><p>Restart <b>sshd</b>.</p>",
  });
  assert.equal(payload.kind, "html-converted");
  assert.match(payload.text, /^# Runbook/m);
  assert.match(payload.text, /\*\*sshd\*\*/);
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
  assert.equal(
    shouldInterceptResolvedNotePaste({
      editorMode: "edit",
      pasteInsideCodeBlock: true,
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
