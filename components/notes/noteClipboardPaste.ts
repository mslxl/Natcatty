/**
 * Clipboard resolution for vault markdown notes.
 *
 * Uses Turndown (mature HTML→Markdown) for real HTML clipboard payloads.
 * For mixed markdown + HTML islands (GitHub-style plain text with <img>),
 * only convert HTML islands — never turndown the whole string (that escapes
 * # / ** and collapses structure).
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
  // Common image markdown / raw HTML image from docs pastes.
  /!\[[^\]]*\]\([^)\s]+\)/,
  /<img\b/i,
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

/** Plain markdown that still embeds raw HTML (GitHub-style). */
export const plainMarkdownContainsHtml = (text: string): boolean => (
  /<\/?[a-zA-Z][^>]*>/.test(text)
);

/**
 * True when the payload is primarily an HTML document (browser / Word / GitHub
 * rich clipboard), not markdown-with-a-few-tags.
 */
export const isPrimarilyHtmlDocument = (html: string): boolean => {
  const trimmed = html.trim();
  if (!trimmed) return false;
  if (/<!--StartFragment-->/i.test(trimmed)) return true;
  if (/<\s*html[\s>]/i.test(trimmed)) return true;
  if (/<\s*body[\s>]/i.test(trimmed)) return true;
  // High tag density vs remaining text → treat as HTML document.
  const withoutTags = trimmed.replace(/<[^>]+>/g, "").replace(/\s+/g, " ").trim();
  const tagChars = (trimmed.match(/<[^>]+>/g) ?? []).join("").length;
  if (tagChars === 0) return false;
  if (withoutTags.length === 0) return true;
  return tagChars >= withoutTags.length * 0.35;
};

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
    preformattedCode: true,
  });
  service.use(gfm);
  // Drop empty name-only anchors (GitHub heading bookmarks).
  service.addRule("stripEmptyAnchors", {
    filter: (node) => (
      node.nodeName === "A"
      && !(node as HTMLElement).getAttribute("href")
      && !(node.textContent ?? "").trim()
    ),
    replacement: () => "",
  });
  // Skip only huge base64 data URLs from Office paste.
  service.addRule("skipDataImages", {
    filter: (node) => (
      node.nodeName === "IMG"
      && ((node as HTMLImageElement).getAttribute("src") ?? "").startsWith("data:")
    ),
    replacement: () => "",
  });
  // Preserve width/height as HTML <img> so MDXEditor's HtmlImageVisitor keeps
  // dimensions (plain ![alt](src) drops size — GitHub READMEs use width attrs).
  service.addRule("imagesPreserveDimensions", {
    filter: "img",
    replacement: (_content, node) => {
      const el = node as HTMLImageElement;
      const src = (el.getAttribute("src") ?? "").trim();
      if (!src || src.startsWith("data:")) return "";
      const html = serializeSafeHtmlImage({
        src,
        alt: el.getAttribute("alt") ?? "",
        title: el.getAttribute("title") ?? undefined,
        width: el.getAttribute("width") ?? undefined,
        height: el.getAttribute("height") ?? undefined,
      });
      return html ? `\n\n${html}\n\n` : "";
    },
  });
  turndownSingleton = service;
  return service;
};

const isSafeImageSrc = (src: string): boolean => {
  const trimmed = src.trim();
  if (!trimmed) return false;
  if (trimmed.startsWith("data:")) return false;
  // Allow https, http, protocol-relative, and relative paths from READMEs.
  if (/^https?:\/\//i.test(trimmed)) return true;
  if (trimmed.startsWith("//")) return true;
  if (trimmed.startsWith("/") || trimmed.startsWith("./") || trimmed.startsWith("../")) return true;
  // Bare relative like public/icon.png
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(trimmed)) return true;
  return false;
};

const escapeHtmlAttr = (value: string): string => (
  value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
);

/**
 * Build a sanitized HTML img tag. When width/height are present, keep HTML so
 * MDXEditor can import dimensions; otherwise emit standard markdown image.
 */
export const serializeSafeHtmlImage = (input: {
  src: string;
  alt?: string;
  title?: string;
  width?: string | number | null;
  height?: string | number | null;
}): string => {
  const src = (input.src ?? "").trim();
  if (!isSafeImageSrc(src)) return "";
  const alt = input.alt ?? "";
  const title = input.title?.trim() || "";
  const width = input.width != null && String(input.width).trim() !== ""
    ? String(input.width).trim()
    : "";
  const height = input.height != null && String(input.height).trim() !== ""
    ? String(input.height).trim()
    : "";
  // Only keep numeric (or percent) dimensions — ignore junk attributes.
  const safeWidth = /^(?:\d+(?:\.\d+)?%?)$/.test(width) ? width : "";
  const safeHeight = /^(?:\d+(?:\.\d+)?%?)$/.test(height) ? height : "";

  if (!safeWidth && !safeHeight) {
    const titlePart = title ? ` "${title.replace(/"/g, '\\"')}"` : "";
    return `![${alt.replace(/[[\]]/g, "")}](${src}${titlePart})`;
  }

  const parts = [
    `src="${escapeHtmlAttr(src)}"`,
    `alt="${escapeHtmlAttr(alt)}"`,
  ];
  if (title) parts.push(`title="${escapeHtmlAttr(title)}"`);
  if (safeWidth) parts.push(`width="${escapeHtmlAttr(safeWidth)}"`);
  if (safeHeight) parts.push(`height="${escapeHtmlAttr(safeHeight)}"`);
  return `<img ${parts.join(" ")} />`;
};

const trimBlankLines = (value: string): string => (
  value
    .replace(/\r\n?/g, "\n")
    .replace(/[ \t]+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/^\n+/, "")
    .replace(/\n+$/, "")
);

const turndownFragment = (html: string): string => {
  try {
    return getTurndown().turndown(html);
  } catch {
    return "";
  }
};

/** Parse a single <img …> tag into safe markdown or dimension-preserving HTML. */
export const convertHtmlImgTagToMarkdownOrHtml = (imgTag: string): string => {
  const match = imgTag.match(/^<img\b([^>]*)\/?\s*>$/i);
  if (!match) return turndownFragment(imgTag).trim();
  const attrBlob = match[1] ?? "";
  const getAttr = (name: string): string => {
    const re = new RegExp(`\\b${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, "i");
    const m = re.exec(attrBlob);
    return (m?.[1] ?? m?.[2] ?? m?.[3] ?? "").trim();
  };
  return serializeSafeHtmlImage({
    src: getAttr("src"),
    alt: getAttr("alt"),
    title: getAttr("title") || undefined,
    width: getAttr("width") || undefined,
    height: getAttr("height") || undefined,
  });
};

/**
 * Convert a full HTML clipboard document with Turndown (+ GFM).
 */
export const convertClipboardHtmlToMarkdown = (html: string): string => {
  if (!looksLikeClipboardHtml(html)) return "";
  return trimBlankLines(turndownFragment(html));
};

/**
 * Convert only HTML islands inside markdown source.
 * Preserves # headings, lists, blockquotes, fences — never escapes them.
 */
export const convertHtmlIslandsInMarkdown = (markdown: string): string => {
  if (!plainMarkdownContainsHtml(markdown)) {
    return markdown.replace(/\r\n?/g, "\n");
  }

  // Protect fenced code so we never rewrite HTML examples inside fences.
  // Use a printable sentinel (not NUL) so eslint no-control-regex stays clean.
  const fenceToken = (index: number) => `@@NETCATTY_MD_FENCE_${index}@@`;
  const fences: string[] = [];
  let body = markdown.replace(/\r\n?/g, "\n").replace(
    /(?:^|\n)(```|~~~)[^\n]*\n[\s\S]*?\n\1[ \t]*(?=\n|$)/g,
    (match) => {
      const token = fenceToken(fences.length);
      fences.push(match);
      return token;
    },
  );

  // Comments
  body = body.replace(/<!--[\s\S]*?-->/g, "");

  // Self-closing / void tags (img, br, hr, …).
  body = body.replace(
    /<([a-zA-Z][\w:-]*)(\s[^>]*)?\s*\/>/g,
    (full, tag: string) => {
      if (String(tag).toLowerCase() === "img") {
        const md = convertHtmlImgTagToMarkdownOrHtml(full);
        return md ? `\n\n${md}\n\n` : "";
      }
      const md = turndownFragment(full);
      if (/^!\[/.test(md.trim()) || /^<img\b/i.test(md.trim())) {
        return `\n\n${md.trim()}\n\n`;
      }
      return md;
    },
  );

  // Paired tags — non-greedy; good enough for GitHub <a name>, <span>, tables.
  // Repeat until stable for nested simple cases.
  for (let pass = 0; pass < 8; pass += 1) {
    const next = body.replace(
      /<([a-zA-Z][\w:-]*)(\s[^>]*)?>([\s\S]*?)<\/\1\s*>/g,
      (full, tag: string) => {
        const lower = String(tag).toLowerCase();
        // Keep raw script/style out of notes.
        if (lower === "script" || lower === "style") return "";
        const md = turndownFragment(full);
        if (!md.trim()) return "";
        // Block-level tags get surrounding newlines.
        if (
          /^(p|div|section|article|table|ul|ol|blockquote|h[1-6]|pre|figure)$/i
            .test(lower)
        ) {
          return `\n\n${md.trim()}\n\n`;
        }
        return md;
      },
    );
    if (next === body) break;
    body = next;
  }

  // Stray unclosed void-like tags (img without />).
  body = body.replace(
    /<(img|br|hr)\b([^>]*)>/gi,
    (full, tag: string) => {
      if (String(tag).toLowerCase() === "img") {
        const md = convertHtmlImgTagToMarkdownOrHtml(full);
        return md ? `\n\n${md}\n\n` : "";
      }
      const md = turndownFragment(full.endsWith("/>") ? full : full.replace(/>$/, " />"));
      if (/^!\[/.test(md.trim()) || /^<img\b/i.test(md.trim())) {
        return `\n\n${md.trim()}\n\n`;
      }
      return md;
    },
  );

  // Restore fences
  body = body.replace(/@@NETCATTY_MD_FENCE_(\d+)@@/g, (_, idx: string) => (
    fences[Number(idx)] ?? ""
  ));

  return trimBlankLines(body);
};

/**
 * Resolve what text should enter the note editor from a clipboard event.
 *
 * Priority:
 * 1. Real HTML document clipboard → full Turndown
 * 2. Mixed markdown + HTML islands → island conversion (preserve markdown)
 * 3. Clean structured markdown → as-is
 * 4. Plain text → leave to Lexical if unstructured
 */
export const resolveNoteClipboardPaste = (input: {
  plainText: string;
  htmlText: string;
}): NoteClipboardPastePayload => {
  const plain = (input.plainText ?? "").replace(/\r\n?/g, "\n");
  const html = input.htmlText ?? "";

  // Browser / Word / GitHub rich HTML payload.
  if (looksLikeClipboardHtml(html) && isPrimarilyHtmlDocument(html)) {
    const converted = convertClipboardHtmlToMarkdown(html);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

  // HTML payload that is really a fragment mixed with text — still turndown full
  // fragment when plain is empty or unstructured.
  if (looksLikeClipboardHtml(html) && !shouldInsertClipboardTextAsMarkdown(plain)) {
    const converted = convertClipboardHtmlToMarkdown(html);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

  // Structured plain (possibly with HTML islands like <img> / <a name>).
  if (shouldInsertClipboardTextAsMarkdown(plain) || plainMarkdownContainsHtml(plain)) {
    if (plainMarkdownContainsHtml(plain)) {
      const converted = convertHtmlIslandsInMarkdown(plain);
      if (converted.trim()) {
        return {
          text: converted,
          kind: plainMarkdownContainsHtml(converted) ? "markdown" : "html-converted",
        };
      }
    }
    if (shouldInsertClipboardTextAsMarkdown(plain)) {
      return { text: plain, kind: "markdown" };
    }
  }

  // Last resort: treat plain as HTML fragment.
  if (looksLikeClipboardHtml(plain)) {
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
