/**
 * Clipboard resolution for vault markdown notes.
 *
 * Product policy (lossy but clean — not GitHub README clone):
 * - HTML → Markdown via Turndown (+ island conversion for mixed sources)
 * - Linked badge images become plain text links (no broken ](url) debris)
 * - Image width/height attrs are preserved in source; CSS max-width:100%
 *   scales large screenshots in the side panel without cropping
 * - Centered blocks (align=center / text-align:center) wrap as
 *   <div align="center"> so MDX GenericHTML + CSS keep hero title/logo centered
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
  // README hero blocks: <p align="center"> / <h1 align="center"> → keep center.
  service.addRule("keepCenteredBlocks", {
    filter: (node) => isCenteredBlockElement(node as HTMLElement),
    replacement: (content, node) => {
      let inner = content.trim();
      const tag = (node as HTMLElement).nodeName.toLowerCase();
      const heading = /^h([1-6])$/.exec(tag);
      // Restore ATX heading markers (turndown children are plain text for h*).
      if (heading && inner && !/^#{1,6}\s/m.test(inner)) {
        inner = `${"#".repeat(Number(heading[1]))} ${inner}`;
      }
      return wrapCenteredMarkdown(inner);
    },
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

const CENTERED_BLOCK_TAGS = new Set([
  "P",
  "DIV",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "SECTION",
  "HEADER",
]);

/** True when a DOM/HTML block is explicitly center-aligned (GitHub README style). */
export const isCenteredBlockElement = (node: HTMLElement | Element | null | undefined): boolean => {
  if (!node || !("nodeName" in node)) return false;
  if (!CENTERED_BLOCK_TAGS.has(node.nodeName)) return false;
  const el = node as HTMLElement;
  const align = (el.getAttribute?.("align") ?? "").trim().toLowerCase();
  if (align === "center") return true;
  const style = el.getAttribute?.("style") ?? "";
  if (/text-align\s*:\s*center/i.test(style)) return true;
  return false;
};

/** Detect center alignment on a raw HTML open-tag blob. */
export const htmlOpenTagIsCentered = (openTagOrFull: string): boolean => {
  if (/\balign\s*=\s*(?:"|')?center(?:"|')?/i.test(openTagOrFull)) return true;
  if (/text-align\s*:\s*center/i.test(openTagOrFull)) return true;
  return false;
};

/**
 * Wrap turndown content so MDX GenericHTML keeps align=center.
 * Blank lines inside let nested markdown / img HTML still parse.
 */
export const wrapCenteredMarkdown = (inner: string): string => {
  const body = inner.replace(/\r\n?/g, "\n").trim();
  if (!body) return "";
  // Avoid double-wrapping.
  if (/^<div\s+align="center">/i.test(body) && /<\/div>\s*$/i.test(body)) {
    return `\n\n${body}\n\n`;
  }
  return `\n\n<div align="center">\n\n${body}\n\n</div>\n\n`;
};

const isSafeImageSrc = (src: string): boolean => {
  const trimmed = src.trim();
  if (!trimmed) return false;
  // Never keep huge clipboard bitmaps / scripted URLs.
  if (trimmed.startsWith("data:")) return false;
  if (/^javascript:/i.test(trimmed)) return false;
  if (/^https?:\/\//i.test(trimmed)) return true;
  // Protocol-relative CDN (//img.shields.io/…).
  if (trimmed.startsWith("//") && /^\/\/[^/\s]/.test(trimmed)) return true;
  // Relative paths from README paste (public/icon.png). They may 404 in the
  // note renderer, but dropping them silently loses logos; keep the tag so
  // users still see a broken-image affordance / can fix the URL.
  if (trimmed.startsWith("/") || trimmed.startsWith("./") || trimmed.startsWith("../")) {
    return true;
  }
  // Bare relative: public/foo.svg — reject only obvious scheme-like tokens.
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
 * Display safety: side-panel CSS uses max-width:100% + height:auto (when width
 * is present or height is absent) so large intrinsic sizes shrink without
 * cropping, while height-only icons keep their HTML height.
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
 * Keep linked badge *images* (not text-only), but force a single line so MDX
 * does not tear `[![alt](img)](href)` into image + orphan `](href)`.
 *
 * Prefers HTML `<a href="…"><img … /></a>` when the image still has dimensions
 * (shields / ko-fi) — MDX GenericHTML + img visitor render reliably.
 */
export const normalizeLinkedBadgeImages = (markdown: string): string => {
  let body = markdown.replace(/\r\n?/g, "\n");

  // [![alt](img)](href) with optional internal whitespace/newlines → one line
  body = body.replace(
    /\[\s*!\[[^\]]*\]\(([^)]+)\)\s*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g,
    (full, imgSrc: string, href: string) => {
      const alt = extractMarkdownImageAlt(full);
      const src = (imgSrc || "").trim().split(/\s+/)[0] ?? "";
      if (!src || !isSafeImageSrc(src)) {
        return `[${alt}](${href})`;
      }
      // Shields / badge URLs: keep as linked markdown image (no pixel size).
      return `[![${alt}](${src})](${href})`;
    },
  );

  // [<img …>](href) → <a href><img></a> (keeps width/height + click target)
  body = body.replace(
    /\[\s*(<img\b[^>]*>)\s*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/gi,
    (_full, imgTag: string, href: string) => {
      const safeImg = convertHtmlImgTagToMarkdownOrHtml(String(imgTag).trim());
      if (!safeImg) return "";
      if (safeImg.startsWith("<img")) {
        return `<a href="${escapeHtmlAttr(href)}">${safeImg}</a>`;
      }
      const m = /!\[([^\]]*)\]\(([^)\s]+)\)/.exec(safeImg);
      if (m) return `[![${m[1]}](${m[2]})](${href})`;
      return `[${extractMarkdownImageAlt(safeImg)}](${href})`;
    },
  );

  // HTML <a href="…"> … <img> … </a> (README badge row) → compact linked image
  body = body.replace(
    /<a\b([^>]*)>\s*(<img\b[^>]*>)\s*<\/a>/gi,
    (full, aAttrs: string, imgTag: string) => {
      const hrefMatch = /\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i.exec(aAttrs);
      const href = (hrefMatch?.[1] ?? hrefMatch?.[2] ?? hrefMatch?.[3] ?? "").trim();
      if (!href || /^javascript:/i.test(href)) return convertHtmlImgTagToMarkdownOrHtml(imgTag) || "";
      const safeImg = convertHtmlImgTagToMarkdownOrHtml(imgTag.trim());
      if (!safeImg) return "";
      if (safeImg.startsWith("<img")) {
        return `<a href="${escapeHtmlAttr(href)}">${safeImg}</a>`;
      }
      const m = /!\[([^\]]*)\]\(([^)\s]+)\)/.exec(safeImg);
      if (m) return `[![${m[1]}](${m[2]})](${href})`;
      return `[${extractMarkdownImageAlt(full)}](${href})`;
    },
  );

  return body;
};

/** @deprecated Use normalizeLinkedBadgeImages — kept as alias for older tests. */
export const collapseLinkedImagesToTextLinks = normalizeLinkedBadgeImages;

/**
 * Drop orphan `](url)` lines left by broken badge conversion, but leave
 * fenced / indented code alone (docs and parser fixtures often show that form).
 */
const stripOrphanLinkClosersOutsideCode = (markdown: string): string => {
  const placeholders: string[] = [];
  const stash = (chunk: string): string => {
    const token = `@@NETCATTY_MD_CODE_${placeholders.length}@@`;
    placeholders.push(chunk);
    return token;
  };

  let body = markdown;

  // Fenced code (``` / ~~~). Lookbehind keeps the prior newline so orphan
  // closer lines above a fence stay alone for the strip below.
  body = body.replace(
    /(?<=^|\n)(```|~~~)[^\n]*\n[\s\S]*?\n\1[ \t]*(?=\n|$)/g,
    (match) => stash(match),
  );

  // Indented code blocks (4 spaces or a tab).
  body = body.replace(
    /(?<=^|\n)(?:(?: {4}|\t).*(?:\n(?: {4}|\t).*)*)/g,
    (match) => stash(match),
  );

  body = body.replace(/^\s*\]\([^)\n]+\)\s*$/gm, "");

  return body.replace(/@@NETCATTY_MD_CODE_(\d+)@@/g, (_, idx: string) => (
    placeholders[Number(idx)] ?? ""
  ));
};

/**
 * Final cleanup for note paste: tighten linked badges (keep images),
 * re-sanitize img tags (keeps width/height), tidy blank lines.
 */
export const normalizePastedNoteMarkdown = (markdown: string): string => {
  let body = normalizeLinkedBadgeImages(markdown);

  // Re-serialize standalone HTML images (keep width/height). Leave whole <a>…</a>
  // anchors alone so linked badges stay <a><img></a>.
  body = body.replace(/<a\b[^>]*>[\s\S]*?<\/a>|<img\b[^>]*>/gi, (chunk) => {
    if (/^<a\b/i.test(chunk)) return chunk;
    return convertHtmlImgTagToMarkdownOrHtml(chunk.trim()) || "";
  });

  // Second pass after img sanitization may recreate spaced wrappers.
  body = normalizeLinkedBadgeImages(body);
  body = stripOrphanLinkClosersOutsideCode(body);

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
  // Do NOT normalize linked badges before turndown — converting <a><img> to
  // markdown first leaves [![…]](…) as text inside <p align=center>, and
  // turndown then escapes it to \[!\[…\]\](…).
  let body = markdown.replace(/\r\n?/g, "\n");

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
        // Already-emitted center wrappers — never re-turndown (escapes ** / #).
        if (lower === "div" && htmlOpenTagIsCentered(full)) {
          return full;
        }
        // Centered p/h* (README hero): turndown keepCenteredBlocks → div align=center.
        if (
          (lower === "p" || /^h[1-6]$/.test(lower))
          && htmlOpenTagIsCentered(full)
        ) {
          const md = turndownFragment(full);
          return md.trim() ? `\n\n${md.trim()}\n\n` : "";
        }
        const md = turndownFragment(full);
        if (!md.trim()) return "";
        if (
          /^(p|div|section|article|table|ul|ol|blockquote|h[1-6]|pre|figure)$/i.test(lower)
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
