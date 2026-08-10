import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  mergeNoteMarkdownDocumentPaste,
  NOTE_MARKDOWN_PASTE_INSERT_MAX_CHARS,
  resolveNoteMarkdownPasteSettleAttempts,
  resolveNoteMarkdownPasteStrategy,
  shouldInterceptNoteMarkdownPaste,
} from "./InlineMarkdownEditor.tsx";

test("markdown paste intercepts structured clipboard text in edit mode even without a Lexical selection", () => {
  assert.equal(
    shouldInterceptNoteMarkdownPaste({
      editorMode: "edit",
      pasteInsideCodeBlock: false,
      clipboardText: "# Heading\n\n- item",
      canInsertMarkdownAtSelection: true,
    }),
    true,
  );
  // After a prior insertMarkdown clears the caret, continuous paste must still
  // be recoverable via document setMarkdown rather than a swallowed preventDefault.
  assert.equal(
    shouldInterceptNoteMarkdownPaste({
      editorMode: "edit",
      pasteInsideCodeBlock: false,
      clipboardText: "# Heading\n\n- item",
      canInsertMarkdownAtSelection: false,
    }),
    true,
  );
  assert.equal(
    shouldInterceptNoteMarkdownPaste({
      editorMode: "preview",
      pasteInsideCodeBlock: false,
      clipboardText: "# Heading\n\n- item",
      canInsertMarkdownAtSelection: true,
    }),
    false,
  );
});

test("document paste merge preserves first-line indentation and appends after current body", () => {
  assert.equal(
    mergeNoteMarkdownDocumentPaste("Existing note", "# Pasted\n\n- item"),
    "Existing note\n\n# Pasted\n\n- item",
  );
  assert.equal(
    mergeNoteMarkdownDocumentPaste("   ", "# Only paste"),
    "# Only paste",
  );
  assert.equal(
    mergeNoteMarkdownDocumentPaste("- parent", "  - child"),
    "- parent\n\n  - child",
  );
  assert.equal(
    mergeNoteMarkdownDocumentPaste("Existing", "\n\n# Pasted\n"),
    "Existing\n\n# Pasted",
  );
});

test("paste strategy uses document merge when caret is missing or paste is long", () => {
  assert.equal(
    resolveNoteMarkdownPasteStrategy({
      canInsertMarkdownAtSelection: false,
      clipboardText: "# short",
    }),
    "document-merge",
  );
  assert.equal(
    resolveNoteMarkdownPasteStrategy({
      canInsertMarkdownAtSelection: true,
      clipboardText: "# short body",
    }),
    "insert-at-selection",
  );
  const longMarkdown = `# Title\n\n${"paragraph text ".repeat(400)}`;
  assert.ok(longMarkdown.length >= NOTE_MARKDOWN_PASTE_INSERT_MAX_CHARS);
  assert.equal(
    resolveNoteMarkdownPasteStrategy({
      canInsertMarkdownAtSelection: true,
      clipboardText: longMarkdown,
    }),
    "document-merge",
  );
});

test("paste settle attempts scale with clipboard size", () => {
  assert.equal(resolveNoteMarkdownPasteSettleAttempts(100), 6);
  assert.equal(resolveNoteMarkdownPasteSettleAttempts(3_000), 6);
  assert.equal(resolveNoteMarkdownPasteSettleAttempts(12_000), 8);
  assert.equal(resolveNoteMarkdownPasteSettleAttempts(100_000), 24);
});

test("InlineMarkdownEditor only preventDefaults markdown paste after a successful intercept guard", () => {
  const source = readFileSync(new URL("./InlineMarkdownEditor.tsx", import.meta.url), "utf8");

  assert.match(source, /shouldInterceptNoteMarkdownPaste/);
  assert.match(source, /hasActiveLexicalTextSelection/);
  assert.match(source, /mergeNoteMarkdownDocumentPaste/);
  assert.match(source, /setMarkdown\(/);
  assert.match(source, /resolveNoteMarkdownPasteStrategy/);
  assert.match(
    source,
    /shouldInterceptNoteMarkdownPaste\([\s\S]*?\)[\s\S]*?event\.preventDefault\(\)/,
  );
  assert.match(
    source,
    /if \(\s*!shouldInterceptNoteMarkdownPaste\([\s\S]*?\)\s*\{\s*return;\s*\}/,
  );
  // Long / no-caret path goes document-merge; short selection keeps insertMarkdown.
  assert.match(source, /strategy === "document-merge"/);
  assert.match(source, /editor\.insertMarkdown\(markdown\)/);
  assert.match(source, /pasteRecoveryGenerationRef/);
  assert.match(source, /tryCommitSettledPaste/);
  assert.match(source, /editor\.getMarkdown\(\)/);
  // Selection-path no-op recovery must fall back to document merge (long paste).
  assert.match(source, /if \(attempt >= maxAttempts\) \{\s*applyDocumentPaste\(\);/);
  // Document merge must read live editor markdown, not only the possibly-stale ref.
  assert.match(
    source,
    /const currentMarkdown = editor\.getMarkdown\(\);[\s\S]*mergeNoteMarkdownDocumentPaste\(currentMarkdown, markdown\)/,
  );
});
