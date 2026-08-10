/**
 * Clipboard resolution for vault markdown notes.
 *
 * Uses Turndown (mature HTML→Markdown) for browser/GitHub/Word HTML pastes.
 * Prefer text/html when present — plain often keeps raw HTML that MDX cannot insert.
 */

import TurndownService from "turndown";
import { gfm } from "turndown-plugin-gfm";

export type NoteClipboardPasteKind =
  | "markdown"
  | "html-converted"
  | "plain"
  | "empty";

export type NoteClipboardPastePayload = {
  text: string;
  kind: NoteClipboardPasteKind;
};

const PASTED_MARKDOWN_PATTERNS = [
  /^ {0,3}#{1,6}\s+\S/m,
  /^ {0,3}(?:[-+*]|\d+[.)])\s+\S/m,
  /^ {0,3}>\s+\S/m,
  /^ {0,3}(?:```|~~~)/m,
  /^ {0,3}[-*_](?:\s*[-*_]){2,}\s*$/m,
  /^ {0,3}\|?.+\|.+\n {0,3}\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$/m,
  /(^|[^!])\[[^\]\n]+\]\([^) \n]+(?:\s+"[^"\n]*")?\)/,
  /(^|[\s([{])(?:\*\*|__)\S[\s\S]*?\S(?:\*\*|__)(?=$|[\s\])}.,;:!?])/,
  /(^|[\s([{])`[^`\n]+`(?=$|[\s\])}.,;:!?])/,
];

/** True when plain clipboard text already looks like structured markdown source. */
export const shouldInsertClipboardTextAsMarkdown = (text: string): boolean => {
  const markdown = text.replace(/\r\n?/g, "\n").trim();
  if (!markdown) return false;
  return PASTED_MARKDOWN_PATTERNS.some((pattern) => pattern.test(markdown));
};

/** True when clipboard HTML is worth converting (not empty / not a lone meta tag). */
export const looksLikeClipboardHtml = (html: string): boolean => {
  const trimmed = html.trim();
  if (!trimmed) return false;
  if (!/<[a-zA-Z!/?]/.test(trimmed)) return false;
  const withoutMeta = trimmed
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<meta\b[^>]*>/gi, "")
    .replace(/<\/?(?:html|head|body)\b[^>]*>/gi, "")
    .trim();
  return withoutMeta.length > 0;
};

/** Plain markdown that still embeds raw HTML (GitHub-style) — risky for MDX insert. */
export const plainMarkdownContainsHtml = (text: string): boolean => (
  /<\/?[a-zA-Z][^>]*>/.test(text)
);

let turndownSingleton: TurndownService | null = null;

const getTurndown = (): TurndownService => {
  if (turndownSingleton) return turndownSingleton;
  const service = new TurndownService({
    headingStyle: "atx",
    hr: "---",
    bulletListMarker: "-",
    codeBlockStyle: "fenced",
    emDelimiter: "*",
    strongDelimiter: "**",
    linkStyle: "inlined",
    // Keep preformatted whitespace reasonable for ops paste.
    preformattedCode: true,
  });
  service.use(gfm);
  // Drop empty anchors / name-only bookmarks (common in GitHub headings).
  service.addRule("stripEmptyAnchors", {
    filter: (node) => (
      node.nodeName === "A"
      && !(node as HTMLElement).getAttribute("href")
      && !(node.textContent ?? "").trim()
    ),
    replacement: () => "",
  });
  // Skip huge data: images from Office / browser screenshots inline.
  service.addRule("skipDataImages", {
    filter: (node) => (
      node.nodeName === "IMG"
      && ((node as HTMLImageElement).getAttribute("src") ?? "").startsWith("data:")
    ),
    replacement: () => "",
  });
  turndownSingleton = service;
  return service;
};

const trimBlankLines = (value: string): string => (
  value
    .replace(/\r\n?/g, "\n")
    .replace(/[ \t]+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/^\n+/, "")
    .replace(/\n+$/, "")
);

/**
 * Convert clipboard HTML with Turndown (+ GFM tables/strikethrough/task lists).
 */
export const convertClipboardHtmlToMarkdown = (html: string): string => {
  if (!looksLikeClipboardHtml(html)) return "";
  try {
    const md = getTurndown().turndown(html);
    return trimBlankLines(md);
  } catch {
    return "";
  }
};

/**
 * Resolve what text should enter the note editor from a clipboard event.
 *
 * Priority:
 * 1. text/html → Turndown (browser / GitHub / Word rich paste)
 * 2. structured plain markdown (true .md source without HTML islands)
 * 3. plain text (leave to Lexical if unstructured)
 */
export const resolveNoteClipboardPaste = (input: {
  plainText: string;
  htmlText: string;
}): NoteClipboardPastePayload => {
  const plain = (input.plainText ?? "").replace(/\r\n?/g, "\n");
  const html = input.htmlText ?? "";

  // Prefer HTML conversion when the OS provides a real HTML payload.
  // GitHub/browser plain often keeps raw <img>/<a name> that MDX insertMarkdown no-ops on.
  if (looksLikeClipboardHtml(html)) {
    const converted = convertClipboardHtmlToMarkdown(html);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

  if (shouldInsertClipboardTextAsMarkdown(plain)) {
    // Plain markdown with embedded HTML tags: try wrapping as HTML for Turndown.
    if (plainMarkdownContainsHtml(plain) && looksLikeClipboardHtml(plain)) {
      const converted = convertClipboardHtmlToMarkdown(plain);
      if (converted.trim()) {
        return { text: converted, kind: "html-converted" };
      }
    }
    return { text: plain, kind: "markdown" };
  }

  // Plain is unstructured but may still be a bare HTML fragment (some apps).
  if (plainMarkdownContainsHtml(plain) && looksLikeClipboardHtml(plain)) {
    const converted = convertClipboardHtmlToMarkdown(plain);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

  if (plain.trim()) {
    return { text: plain, kind: "plain" };
  }

  return { text: "", kind: "empty" };
};

/**
 * Whether paste capture should take over (preventDefault + insert as markdown).
 * HTML-converted / structured markdown always intercept so Lexical never swallows paste.
 */
export const shouldInterceptResolvedNotePaste = (input: {
  editorMode: "edit" | "preview";
  pasteInsideCodeBlock: boolean;
  payload: NoteClipboardPastePayload;
}): boolean => {
  if (input.editorMode !== "edit") return false;
  if (input.pasteInsideCodeBlock) return false;
  if (input.payload.kind === "empty") return false;
  if (input.payload.kind === "html-converted") return true;
  if (input.payload.kind === "markdown") return true;
  return false;
};
