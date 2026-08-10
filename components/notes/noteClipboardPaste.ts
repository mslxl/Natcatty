/**
 * Clipboard resolution for vault markdown notes.
 *
 * Product policy (lossy but clean — not GitHub README clone):
 * - HTML → Markdown via Turndown (+ island conversion for mixed sources)
 * - Linked badge images become plain text links (no broken ](url) debris)
 * - Image width/height attrs are preserved in source; CSS max-width:100%
 *   scales large screenshots in the side panel without cropping
 * - Center/align HTML from READMEs is intentionally dropped
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
  service.addRule("stripEmptyAnchors", {
    filter: (node) => (
      node.nodeName === "A"
      && !(node as HTMLElement).getAttribute("href")
      && !(node.textContent ?? "").trim()
    ),
    replacement: () => "",
  });
  service.addRule("skipDataImages", {
    filter: (node) => (
      node.nodeName === "IMG"
      && ((node as HTMLImageElement).getAttribute("src") ?? "").startsWith("data:")
    ),
    replacement: () => "",
  });
  // Keep width/height when present (HTML <img>); CSS scales them in the panel.
  service.addRule("imagesForNotes", {
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
  if (/^https?:\/\//i.test(trimmed)) return true;
  if (trimmed.startsWith("//")) return true;
  if (trimmed.startsWith("/") || trimmed.startsWith("./") || trimmed.startsWith("../")) return true;
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
 * Build image markdown for notes.
 * - With width/height → sanitized HTML <img> (MDX keeps dimensions; CSS scales)
 * - Without → standard ![alt](src)
 *
 * Display safety: side-panel CSS uses max-width:100% + height:auto so large
 * intrinsic sizes (e.g. 3142×1764 screenshots) shrink instead of cropping.
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
  const alt = (input.alt ?? "").replace(/[[\]]/g, "");
  const title = input.title?.trim() || "";
  const widthRaw = input.width != null ? String(input.width).trim() : "";
  const heightRaw = input.height != null ? String(input.height).trim() : "";
  // Accept plain px numbers or percent strings.
  const safeWidth = /^(?:\d+(?:\.\d+)?%?)$/.test(widthRaw) ? widthRaw : "";
  const safeHeight = /^(?:\d+(?:\.\d+)?%?)$/.test(heightRaw) ? heightRaw : "";

  if (!safeWidth && !safeHeight) {
    const titlePart = title ? ` "${title.replace(/"/g, '\\"')}"` : "";
    return `![${alt}](${src}${titlePart})`;
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

/** Parse a single <img …> tag into safe markdown or modest HTML. */
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

const extractMarkdownImageAlt = (imageChunk: string): string => {
  const md = /!\[([^\]]*)\]/.exec(imageChunk);
  if (md) return (md[1] || "link").trim() || "link";
  const htmlAlt = /alt\s*=\s*(?:"([^"]*)"|'([^']*)')/i.exec(imageChunk);
  if (htmlAlt) return (htmlAlt[1] ?? htmlAlt[2] ?? "link").trim() || "link";
  return "link";
};

/**
 * Collapse README-style linked badges into plain text links.
 *
 * GitHub: [![Release](badge.svg)](https://…)
 * Broken after island conversion with newlines:
 *   [\n\n![Release](badge.svg)\n\n](https://…)
 * MDX then shows the image + raw `](url)` debris — avoid that entirely.
 */
export const collapseLinkedImagesToTextLinks = (markdown: string): string => {
  let body = markdown.replace(/\r\n?/g, "\n");

  // [![alt](img)](href) including internal whitespace/newlines
  body = body.replace(
    /\[\s*!\[[^\]]*\]\([^)]+\)\s*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g,
    (full, href: string) => {
      const alt = extractMarkdownImageAlt(full);
      return `[${alt}](${href})`;
    },
  );

  // [<img …>](href) including whitespace/newlines
  body = body.replace(
    /\[\s*<img\b[^>]*>\s*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/gi,
    (full, href: string) => {
      const alt = extractMarkdownImageAlt(full);
      return `[${alt}](${href})`;
    },
  );

  return body;
};

/**
 * Final cleanup for note paste: badges → text links, re-sanitize img tags
 * (keeps width/height), tidy blank lines. Called after Turndown / islands.
 */
export const normalizePastedNoteMarkdown = (markdown: string): string => {
  let body = collapseLinkedImagesToTextLinks(markdown);

  // Re-serialize remaining HTML images (sanitize attrs; keep dimensions).
  body = body.replace(/<img\b[^>]*>/gi, (tag) => {
    const converted = convertHtmlImgTagToMarkdownOrHtml(tag.trim());
    return converted || "";
  });

  // Drop orphan link closers that sometimes survive broken badge conversion.
  body = body.replace(/^\s*\]\([^)\n]+\)\s*$/gm, "");

  return trimBlankLines(body);
};

/**
 * Convert a full HTML clipboard document with Turndown (+ GFM).
 */
export const convertClipboardHtmlToMarkdown = (html: string): string => {
  if (!looksLikeClipboardHtml(html)) return "";
  return normalizePastedNoteMarkdown(turndownFragment(html));
};

/**
 * Convert only HTML islands inside markdown source.
 * Preserves # headings, lists, blockquotes, fences — never escapes them.
 */
export const convertHtmlIslandsInMarkdown = (markdown: string): string => {
  // Collapse badges first while link wrappers are still intact.
  let body = collapseLinkedImagesToTextLinks(markdown.replace(/\r\n?/g, "\n"));

  if (!plainMarkdownContainsHtml(body)) {
    return normalizePastedNoteMarkdown(body);
  }

  const fenceToken = (index: number) => `@@NETCATTY_MD_FENCE_${index}@@`;
  const fences: string[] = [];
  body = body.replace(
    /(?:^|\n)(```|~~~)[^\n]*\n[\s\S]*?\n\1[ \t]*(?=\n|$)/g,
    (match) => {
      const token = fenceToken(fences.length);
      fences.push(match);
      return token;
    },
  );

  body = body.replace(/<!--[\s\S]*?-->/g, "");

  // Self-closing / void tags — emit image markdown without extra blank lines
  // when still inside a link (should be rare after collapse).
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

  for (let pass = 0; pass < 8; pass += 1) {
    const next = body.replace(
      /<([a-zA-Z][\w:-]*)(\s[^>]*)?>([\s\S]*?)<\/\1\s*>/g,
      (full, tag: string) => {
        const lower = String(tag).toLowerCase();
        if (lower === "script" || lower === "style") return "";
        // Center wrappers from READMEs: keep children only (lossy layout).
        if (lower === "p" || lower === "div") {
          const align = /\balign\s*=\s*(?:"|')?center/i.test(full);
          const md = turndownFragment(full);
          if (!md.trim()) return "";
          // Don't re-wrap with extra spacing that blows up badge rows more than needed.
          return align ? `\n\n${md.trim()}\n\n` : `\n\n${md.trim()}\n\n`;
        }
        const md = turndownFragment(full);
        if (!md.trim()) return "";
        if (
          /^(section|article|table|ul|ol|blockquote|h[1-6]|pre|figure)$/i.test(lower)
        ) {
          return `\n\n${md.trim()}\n\n`;
        }
        return md;
      },
    );
    if (next === body) break;
    body = next;
  }

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

  body = body.replace(/@@NETCATTY_MD_FENCE_(\d+)@@/g, (_, idx: string) => (
    fences[Number(idx)] ?? ""
  ));

  return normalizePastedNoteMarkdown(body);
};

/**
 * Resolve what text should enter the note editor from a clipboard event.
 */
export const resolveNoteClipboardPaste = (input: {
  plainText: string;
  htmlText: string;
}): NoteClipboardPastePayload => {
  const plain = (input.plainText ?? "").replace(/\r\n?/g, "\n");
  const html = input.htmlText ?? "";

  if (looksLikeClipboardHtml(html) && isPrimarilyHtmlDocument(html)) {
    const converted = convertClipboardHtmlToMarkdown(html);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

  if (looksLikeClipboardHtml(html) && !shouldInsertClipboardTextAsMarkdown(plain)) {
    const converted = convertClipboardHtmlToMarkdown(html);
    if (converted.trim()) {
      return { text: converted, kind: "html-converted" };
    }
  }

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
      // Still normalize badges / oversized HTML that may already be in the source.
      return { text: normalizePastedNoteMarkdown(plain), kind: "markdown" };
    }
  }

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
