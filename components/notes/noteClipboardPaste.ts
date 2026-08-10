/**
 * Clipboard resolution for vault markdown notes.
 * Prefer structured plain markdown; otherwise convert browser/Word HTML to markdown.
 */

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

export type NoteClipboardPasteKind =
  | "markdown"
  | "html-converted"
  | "plain"
  | "empty";

export type NoteClipboardPastePayload = {
  text: string;
  kind: NoteClipboardPasteKind;
};

const HTML_FRAGMENT_START = "<!--StartFragment-->";
const HTML_FRAGMENT_END = "<!--EndFragment-->";

/** True when clipboard HTML is worth converting (not empty / not a lone meta tag). */
export const looksLikeClipboardHtml = (html: string): boolean => {
  const trimmed = html.trim();
  if (!trimmed) return false;
  // Browser pastes always include tags; reject pure text mislabeled as html.
  if (!/<[a-zA-Z!/?]/.test(trimmed)) return false;
  // Ignore empty shells / charset-only payloads.
  const withoutMeta = trimmed
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<meta\b[^>]*>/gi, "")
    .replace(/<\/?(?:html|head|body)\b[^>]*>/gi, "")
    .trim();
  return withoutMeta.length > 0;
};

const decodeHtmlEntities = (value: string): string => {
  return value
    .replace(/&nbsp;/gi, " ")
    .replace(/&amp;/gi, "&")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/&#x27;/gi, "'")
    .replace(/&#(\d+);/g, (_, code: string) => {
      const n = Number(code);
      return Number.isFinite(n) ? String.fromCodePoint(n) : _;
    })
    .replace(/&#x([0-9a-f]+);/gi, (_, hex: string) => {
      const n = Number.parseInt(hex, 16);
      return Number.isFinite(n) ? String.fromCodePoint(n) : _;
    });
};

const extractClipboardHtmlFragment = (html: string): string => {
  const start = html.indexOf(HTML_FRAGMENT_START);
  const end = html.indexOf(HTML_FRAGMENT_END);
  if (start !== -1 && end !== -1 && end > start) {
    return html.slice(start + HTML_FRAGMENT_START.length, end);
  }
  return html;
};

type HtmlNode =
  | { type: "text"; value: string }
  | {
    type: "element";
    tag: string;
    attrs: Record<string, string>;
    children: HtmlNode[];
  };

const VOID_TAGS = new Set([
  "area",
  "base",
  "br",
  "col",
  "embed",
  "hr",
  "img",
  "input",
  "link",
  "meta",
  "param",
  "source",
  "track",
  "wbr",
]);

const SKIP_TAGS = new Set([
  "script",
  "style",
  "noscript",
  "template",
  "head",
  "meta",
  "link",
  "title",
]);

const parseAttrs = (raw: string): Record<string, string> => {
  const attrs: Record<string, string> = {};
  const re = /([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(raw)) !== null) {
    const name = match[1].toLowerCase();
    if (name === "/" || name.startsWith("on")) continue;
    attrs[name] = match[2] ?? match[3] ?? match[4] ?? "";
  }
  return attrs;
};

type ParseFrame = {
  tag: string;
  attrs: Record<string, string>;
  children: HtmlNode[];
};

/**
 * Minimal HTML fragment parser (browser / Office clipboard HTML).
 * Unknown tags keep children; not a full HTML5 parser.
 */
export const parseHtmlFragmentTree = (html: string): HtmlNode[] => {
  const source = extractClipboardHtmlFragment(html)
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<!doctype[^>]*>/gi, "");

  const root: HtmlNode[] = [];
  const stack: ParseFrame[] = [];
  let i = 0;

  const childrenOf = (): HtmlNode[] => (
    stack.length > 0 ? stack[stack.length - 1].children : root
  );

  const closeUntil = (tag: string) => {
    let found = -1;
    for (let depth = stack.length - 1; depth >= 0; depth -= 1) {
      if (stack[depth].tag === tag) {
        found = depth;
        break;
      }
    }
    if (found === -1) return;
    while (stack.length > found) {
      const frame = stack.pop()!;
      childrenOf().push({
        type: "element",
        tag: frame.tag,
        attrs: frame.attrs,
        children: frame.children,
      });
    }
  };

  while (i < source.length) {
    if (source[i] !== "<") {
      const next = source.indexOf("<", i);
      const text = source.slice(i, next === -1 ? source.length : next);
      i = next === -1 ? source.length : next;
      if (text) {
        childrenOf().push({ type: "text", value: decodeHtmlEntities(text) });
      }
      continue;
    }

    if (source.startsWith("<!--", i)) {
      const end = source.indexOf("-->", i + 4);
      i = end === -1 ? source.length : end + 3;
      continue;
    }

    const close = source.indexOf(">", i + 1);
    if (close === -1) break;
    const raw = source.slice(i + 1, close).trim();
    i = close + 1;
    if (!raw || raw.startsWith("!")) continue;

    if (raw.startsWith("/")) {
      const tag = raw.slice(1).trim().toLowerCase().split(/\s+/)[0] ?? "";
      if (tag) closeUntil(tag);
      continue;
    }

    const selfClosing = raw.endsWith("/");
    const body = selfClosing ? raw.slice(0, -1).trim() : raw;
    const spaceIdx = body.search(/\s/);
    const tag = (spaceIdx === -1 ? body : body.slice(0, spaceIdx)).toLowerCase();
    if (!tag) continue;
    const attrs = parseAttrs(spaceIdx === -1 ? "" : body.slice(spaceIdx));

    if (SKIP_TAGS.has(tag)) {
      if (!VOID_TAGS.has(tag) && !selfClosing) {
        const re = new RegExp(`</${tag}\\s*>`, "i");
        const rest = source.slice(i);
        const m = re.exec(rest);
        if (m) i += m.index + m[0].length;
      }
      continue;
    }

    if (VOID_TAGS.has(tag) || selfClosing) {
      childrenOf().push({ type: "element", tag, attrs, children: [] });
      continue;
    }

    stack.push({ tag, attrs, children: [] });
  }

  while (stack.length > 0) {
    const frame = stack.pop()!;
    childrenOf().push({
      type: "element",
      tag: frame.tag,
      attrs: frame.attrs,
      children: frame.children,
    });
  }

  return root;
};

const collapseInlineWhitespace = (value: string): string => (
  value.replace(/[ \t\f\v]+/g, " ")
);

const trimBlankLines = (value: string): string => (
  value
    .replace(/\r\n?/g, "\n")
    .replace(/[ \t]+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/^\n+/, "")
    .replace(/\n+$/, "")
);

type SerializeContext = {
  listDepth: number;
  ordered: boolean;
  listIndex: number;
  inPre: boolean;
};

const escapeMdLinkText = (value: string): string => (
  value.replace(/\[/g, "\\[").replace(/\]/g, "\\]")
);

const childrenToMarkdown = (nodes: HtmlNode[], ctx: SerializeContext): string => (
  nodes.map((node) => nodeToMarkdown(node, ctx)).join("")
);

const isBlockTag = (tag: string): boolean => (
  /^(p|div|section|article|header|footer|main|aside|h[1-6]|ul|ol|li|blockquote|pre|table|thead|tbody|tr|hr|br)$/i
    .test(tag)
);

function nodeToMarkdown(node: HtmlNode, ctx: SerializeContext): string {
  if (node.type === "text") {
    if (ctx.inPre) return node.value.replace(/\r\n?/g, "\n");
    return collapseInlineWhitespace(node.value);
  }

  const tag = node.tag;
  const kids = () => childrenToMarkdown(node.children, ctx);

  if (tag === "br") return "\n";
  if (tag === "hr") return "\n\n---\n\n";

  if (/^h([1-6])$/.test(tag)) {
    const level = Number(tag[1]);
    const body = kids().trim();
    if (!body) return "";
    return `\n\n${"#".repeat(level)} ${body}\n\n`;
  }

  if (tag === "p" || tag === "div" || tag === "section" || tag === "article") {
    const body = kids().trim();
    if (!body) return "\n\n";
    return `\n\n${body}\n\n`;
  }

  if (tag === "blockquote") {
    const body = kids().trim();
    if (!body) return "";
    const quoted = body
      .split("\n")
      .map((line) => (line.trim() ? `> ${line}` : ">"))
      .join("\n");
    return `\n\n${quoted}\n\n`;
  }

  if (tag === "pre") {
    const codeCtx: SerializeContext = { ...ctx, inPre: true };
    let body = childrenToMarkdown(node.children, codeCtx);
    body = body.replace(/^\n+|\n+$/g, "");
    const fence = body.includes("```") ? "~~~" : "```";
    return `\n\n${fence}\n${body}\n${fence}\n\n`;
  }

  if (tag === "code") {
    if (ctx.inPre) return kids();
    const body = kids().replace(/\n/g, " ").trim();
    if (!body) return "";
    const tick = body.includes("`") ? "``" : "`";
    return `${tick}${body}${tick}`;
  }

  if (tag === "ul" || tag === "ol") {
    const listCtx: SerializeContext = {
      ...ctx,
      listDepth: ctx.listDepth + 1,
      ordered: tag === "ol",
      listIndex: 0,
    };
    const body = childrenToMarkdown(node.children, listCtx).trim();
    return body ? `\n\n${body}\n\n` : "";
  }

  if (tag === "li") {
    ctx.listIndex += 1;
    const indent = "  ".repeat(Math.max(0, ctx.listDepth - 1));
    const marker = ctx.ordered ? `${ctx.listIndex}.` : "-";
    const body = kids().trim().replace(/\n+/g, "\n");
    if (!body) return `${indent}${marker}\n`;
    const lines = body.split("\n");
    const first = `${indent}${marker} ${lines[0]}`;
    const rest = lines.slice(1).map((line) => (
      line ? `${indent}  ${line}` : ""
    ));
    return `${[first, ...rest].join("\n")}\n`;
  }

  if (tag === "a") {
    const href = (node.attrs.href ?? "").trim();
    const body = kids().trim() || href;
    if (!href) return body;
    if (/^javascript:/i.test(href)) return body;
    return `[${escapeMdLinkText(body)}](${href})`;
  }

  if (tag === "img") {
    const src = (node.attrs.src ?? "").trim();
    if (!src || src.startsWith("data:")) return "";
    const alt = (node.attrs.alt ?? "").trim();
    return `![${escapeMdLinkText(alt)}](${src})`;
  }

  if (tag === "strong" || tag === "b") {
    const body = kids().trim();
    return body ? `**${body}**` : "";
  }

  if (tag === "em" || tag === "i") {
    const body = kids().trim();
    return body ? `*${body}*` : "";
  }

  if (tag === "del" || tag === "s" || tag === "strike") {
    const body = kids().trim();
    return body ? `~~${body}~~` : "";
  }

  if (tag === "table") {
    return tableToMarkdown(node);
  }

  // Span / font / o:p / unknown: keep children.
  const body = kids();
  if (isBlockTag(tag) && body.trim()) {
    return `\n\n${body.trim()}\n\n`;
  }
  return body;
}

const tableToMarkdown = (table: Extract<HtmlNode, { type: "element" }>): string => {
  const rows: string[][] = [];
  const walk = (nodes: HtmlNode[]) => {
    for (const node of nodes) {
      if (node.type !== "element") continue;
      if (node.tag === "tr") {
        const cells: string[] = [];
        for (const cell of node.children) {
          if (cell.type === "element" && (cell.tag === "td" || cell.tag === "th")) {
            cells.push(
              childrenToMarkdown(cell.children, {
                listDepth: 0,
                ordered: false,
                listIndex: 0,
                inPre: false,
              }).replace(/\n+/g, " ").trim(),
            );
          }
        }
        if (cells.length > 0) rows.push(cells);
      } else {
        walk(node.children);
      }
    }
  };
  walk(table.children);
  if (rows.length === 0) return "";
  const width = Math.max(...rows.map((r) => r.length));
  const normalized = rows.map((r) => {
    const copy = [...r];
    while (copy.length < width) copy.push("");
    return copy;
  });
  const header = normalized[0];
  const sep = header.map(() => "---");
  const body = normalized.slice(1);
  const lines = [
    `| ${header.join(" | ")} |`,
    `| ${sep.join(" | ")} |`,
    ...body.map((r) => `| ${r.join(" | ")} |`),
  ];
  return `\n\n${lines.join("\n")}\n\n`;
};

/**
 * Convert clipboard HTML (browser / Word fragment) into Markdown.
 * Pure string parser so unit tests run under plain Node.
 */
export const convertClipboardHtmlToMarkdown = (html: string): string => {
  if (!looksLikeClipboardHtml(html)) return "";
  const tree = parseHtmlFragmentTree(html);
  const md = childrenToMarkdown(tree, {
    listDepth: 0,
    ordered: false,
    listIndex: 0,
    inPre: false,
  });
  return trimBlankLines(md);
};

/**
 * Resolve what text should enter the note editor from a clipboard event.
 * - Structured plain markdown wins (user copied .md source).
 * - Else convert HTML so headings/lists/links survive browser copy.
 * - Else fall back to plain text (browser default path may still run).
 */
export const resolveNoteClipboardPaste = (input: {
  plainText: string;
  htmlText: string;
}): NoteClipboardPastePayload => {
  const plain = (input.plainText ?? "").replace(/\r\n?/g, "\n");
  const html = input.htmlText ?? "";

  if (shouldInsertClipboardTextAsMarkdown(plain)) {
    return { text: plain, kind: "markdown" };
  }

  if (looksLikeClipboardHtml(html)) {
    const converted = convertClipboardHtmlToMarkdown(html);
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
 * HTML-converted payloads always intercept so Lexical never swallows rich HTML.
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
  // Plain unstructured text: leave to Lexical/browser.
  return false;
};
