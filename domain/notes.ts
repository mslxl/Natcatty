import type { Host, VaultNote } from "./models";
import { getNextVaultOrder, normalizeVaultOrder, sortByVaultOrder } from "./vaultOrder";

const cleanStringArray = (values: unknown): string[] | undefined => {
  if (!Array.isArray(values)) return undefined;
  const cleaned = Array.from(
    new Set(
      values
        .filter((value): value is string => typeof value === "string")
        .map((value) => value.trim())
        .filter(Boolean),
    ),
  );
  return cleaned.length ? cleaned : undefined;
};

export const sanitizeNoteTitle = (title: unknown): string =>
  typeof title === "string" ? title.trim() : "";

export const sanitizeVaultNote = (note: Partial<VaultNote>): VaultNote => {
  const now = Date.now();
  const createdAt =
    typeof note.createdAt === "number" && Number.isFinite(note.createdAt)
      ? note.createdAt
      : now;
  const updatedAt =
    typeof note.updatedAt === "number" && Number.isFinite(note.updatedAt)
      ? note.updatedAt
      : createdAt;

  return {
    id: typeof note.id === "string" && note.id.trim() ? note.id : crypto.randomUUID(),
    title: sanitizeNoteTitle(note.title),
    content: typeof note.content === "string" ? note.content : "",
    group: typeof note.group === "string" && note.group.trim() ? note.group.trim() : undefined,
    tags: cleanStringArray(note.tags),
    linkedHostIds: cleanStringArray(note.linkedHostIds),
    createdAt,
    updatedAt,
    order: typeof note.order === "number" && Number.isFinite(note.order) ? note.order : undefined,
    isPinned: note.isPinned ? true : undefined,
  };
};

export const normalizeVaultNotes = (notes: Partial<VaultNote>[]): VaultNote[] =>
  normalizeVaultOrder(notes.map(sanitizeVaultNote));

export const normalizeNoteGroups = (groups: unknown): string[] =>
  Array.isArray(groups)
    ? Array.from(
      new Set(
        groups
          .filter((value): value is string => typeof value === "string")
          .map((value) => cleanNoteGroupPath(value))
          .filter(Boolean),
      ),
    )
    : [];

export const cleanNoteGroupPath = (value: string): string =>
  value
    .split("/")
    .map((part) => part.trim())
    .filter(Boolean)
    .join("/");

export const ancestorNoteGroupPaths = (path: string): string[] => {
  const parts = cleanNoteGroupPath(path).split("/").filter(Boolean);
  return parts.map((_, index) => parts.slice(0, index + 1).join("/"));
};

export const getNoteGroupLeafName = (path: string): string =>
  cleanNoteGroupPath(path).split("/").pop() || cleanNoteGroupPath(path);

export const getNoteGroupParentPath = (path: string): string | null => {
  const parts = cleanNoteGroupPath(path).split("/").filter(Boolean);
  if (parts.length <= 1) return null;
  return parts.slice(0, -1).join("/");
};

export const joinNoteGroupPath = (parent: string | null, name: string): string => {
  const cleanName = cleanNoteGroupPath(name);
  if (!cleanName) return "";
  const cleanParent = parent ? cleanNoteGroupPath(parent) : "";
  return cleanParent ? `${cleanParent}/${cleanName}` : cleanName;
};

export const isNoteGroupInside = (path: string | undefined, group: string): boolean => {
  const cleanPath = path ? cleanNoteGroupPath(path) : "";
  const cleanGroup = cleanNoteGroupPath(group);
  return cleanPath === cleanGroup || Boolean(cleanPath.startsWith(`${cleanGroup}/`));
};

export const replaceNoteGroupPrefix = (path: string | undefined, from: string, to: string): string | undefined => {
  if (!path) return path;
  const cleanPath = cleanNoteGroupPath(path);
  const cleanFrom = cleanNoteGroupPath(from);
  const cleanTo = cleanNoteGroupPath(to);
  if (cleanPath === cleanFrom) return cleanTo || undefined;
  if (cleanPath.startsWith(`${cleanFrom}/`)) return cleanTo ? `${cleanTo}/${cleanPath.slice(cleanFrom.length + 1)}` : undefined;
  return cleanPath;
};

export const resolveMovedNoteGroupPath = (
  group: string,
  parent: string | null,
  groups: string[],
): string | null => {
  const source = cleanNoteGroupPath(group);
  const targetParent = parent ? cleanNoteGroupPath(parent) : null;
  if (!source) return null;
  if (targetParent && (targetParent === source || targetParent.startsWith(`${source}/`))) return null;

  const leafName = getNoteGroupLeafName(source);
  const basePath = joinNoteGroupPath(targetParent, leafName);
  if (!basePath || basePath === source) return null;

  const existingGroups = normalizeNoteGroups(groups)
    .filter((item) => !isNoteGroupInside(item, source));
  const hasConflict = (candidate: string) =>
    existingGroups.some((item) => item === candidate || item.startsWith(`${candidate}/`));

  if (!hasConflict(basePath)) return basePath;

  for (let index = 2; index < 1000; index += 1) {
    const candidate = joinNoteGroupPath(targetParent, `${leafName} ${index}`);
    if (!hasConflict(candidate)) return candidate;
  }

  return null;
};

export const remapExpandedNoteGroupPaths = (
  expandedPaths: Set<string>,
  from: string,
  to: string,
): Set<string> => {
  const next = new Set<string>();
  expandedPaths.forEach((item) => {
    const replaced = replaceNoteGroupPrefix(item, from, to);
    if (replaced) next.add(replaced);
  });
  ancestorNoteGroupPaths(to).forEach((path) => next.add(path));
  return next;
};

export const sortVaultNotes = (notes: VaultNote[]): VaultNote[] => sortByVaultOrder(notes);

export type VaultNotesExportScope =
  | { type: "all" }
  | { type: "group"; group: string };

export interface VaultNoteMarkdownExportFile {
  name: string;
  content: string;
}

const NOTE_EXPORT_UNSAFE_FILENAME_CHARS = /[<>:"/\\|?*]/g;
const NOTE_EXPORT_RESERVED_WINDOWS_NAMES = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;

const replaceControlFilenameChars = (value: string): string => {
  let output = "";
  for (const char of value) {
    output += char.charCodeAt(0) < 32 ? "-" : char;
  }
  return output;
};

export const sanitizeNoteExportFileNamePart = (value: string | undefined, fallback: string): string => {
  const cleaned = replaceControlFilenameChars((value ?? "").trim())
    .replace(NOTE_EXPORT_UNSAFE_FILENAME_CHARS, "-")
    .replace(/\s+/g, " ")
    .replace(/\.+$/g, "")
    .trim();
  const safe = cleaned && cleaned !== "." && cleaned !== ".." ? cleaned : fallback;
  const withoutReservedName = NOTE_EXPORT_RESERVED_WINDOWS_NAMES.test(safe) ? `${safe}_` : safe;
  return withoutReservedName.slice(0, 120) || fallback;
};

export const getVaultNotesForExportScope = (
  notes: VaultNote[],
  scope: VaultNotesExportScope = { type: "all" },
): VaultNote[] => {
  const normalized = sortVaultNotes(normalizeVaultNotes(notes));
  if (scope.type === "all") return normalized;

  const group = cleanNoteGroupPath(scope.group);
  if (!group) return [];
  return normalized.filter((note) => isNoteGroupInside(note.group, group));
};

export const buildVaultNoteMarkdownExportFiles = (
  notes: VaultNote[],
  scope: VaultNotesExportScope = { type: "all" },
): VaultNoteMarkdownExportFile[] => {
  const usedNames = new Set<string>();

  return getVaultNotesForExportScope(notes, scope).map((note, index) => {
    const groupSegments = note.group
      ? cleanNoteGroupPath(note.group)
        .split("/")
        .filter(Boolean)
        .map((part) => sanitizeNoteExportFileNamePart(part, "folder"))
      : [];
    const baseName = sanitizeNoteExportFileNamePart(note.title, `note-${index + 1}`);
    const basePath = [...groupSegments, baseName].join("/");
    let candidate = `${basePath}.md`;
    let suffix = 2;

    while (usedNames.has(candidate.toLowerCase())) {
      candidate = `${basePath}-${suffix}.md`;
      suffix += 1;
    }
    usedNames.add(candidate.toLowerCase());

    return {
      name: candidate,
      content: note.content,
    };
  });
};

export const matchesVaultNoteSearch = (
  note: VaultNote,
  query: string,
  hosts: Host[] = [],
): boolean => {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  const linkedHosts = hosts
    .filter((host) => note.linkedHostIds?.includes(host.id))
    .map((host) => `${host.label} ${host.hostname}`)
    .join(" ");

  return [
    note.title,
    note.content,
    note.group ?? "",
    ...(note.tags ?? []),
    linkedHosts,
  ].some((value) => value.toLowerCase().includes(needle));
};

const isSanitizedRenderedLink = (value: string): boolean => {
  try {
    const url = new URL(value);
    return url.protocol === "about:" && url.pathname === "blank";
  } catch {
    return value.trim() === "about:blank";
  }
};

const unescapeMarkdownText = (value: string): string =>
  value.replace(/\\([\\`*_[\]{}()#+\-.!|>])/g, "$1").trim();

const findInlineMarkdownLinkMatches = (markdown: string, label: string): string[] => {
  const matches: string[] = [];
  const targetLabel = label.trim();
  if (!targetLabel) return matches;

  let index = 0;
  while (index < markdown.length) {
    const labelStart = markdown.indexOf("[", index);
    if (labelStart === -1) break;
    if (labelStart > 0 && markdown[labelStart - 1] === "!") {
      index = labelStart + 1;
      continue;
    }

    let cursor = labelStart + 1;
    let escaped = false;
    let labelEnd = -1;
    for (; cursor < markdown.length; cursor += 1) {
      const char = markdown[cursor];
      if (escaped) {
        escaped = false;
        continue;
      }
      if (char === "\\") {
        escaped = true;
        continue;
      }
      if (char === "]") {
        labelEnd = cursor;
        break;
      }
    }

    if (labelEnd === -1 || markdown[labelEnd + 1] !== "(") {
      index = labelStart + 1;
      continue;
    }

    const rawLabel = markdown.slice(labelStart + 1, labelEnd);
    if (unescapeMarkdownText(rawLabel) !== targetLabel) {
      index = labelEnd + 1;
      continue;
    }

    cursor = labelEnd + 2;
    while (cursor < markdown.length && /\s/.test(markdown[cursor])) cursor += 1;

    let href = "";
    if (markdown[cursor] === "<") {
      cursor += 1;
      const hrefStart = cursor;
      while (cursor < markdown.length && markdown[cursor] !== ">") cursor += 1;
      href = markdown.slice(hrefStart, cursor).trim();
    } else {
      const hrefStart = cursor;
      let depth = 0;
      escaped = false;
      for (; cursor < markdown.length; cursor += 1) {
        const char = markdown[cursor];
        if (escaped) {
          escaped = false;
          continue;
        }
        if (char === "\\") {
          escaped = true;
          continue;
        }
        if (char === "(") {
          depth += 1;
          continue;
        }
        if (char === ")") {
          if (depth === 0) break;
          depth -= 1;
          continue;
        }
        if (/\s/.test(char) && depth === 0) break;
      }
      href = markdown.slice(hrefStart, cursor).trim();
    }

    if (href) matches.push(href);
    index = cursor + 1;
  }

  return matches;
};

export const resolveRenderedMarkdownLinkHref = (
  markdown: string,
  label: string,
  renderedHref: string,
): string => {
  if (!isSanitizedRenderedLink(renderedHref)) return renderedHref;

  const matches = findInlineMarkdownLinkMatches(markdown, label);
  const uniqueMatches = Array.from(new Set(matches));
  return uniqueMatches.length === 1 ? uniqueMatches[0] : renderedHref;
};

const NOTE_IMPORT_TITLE_EXTENSIONS = /\.(md|markdown|txt)$/i;

const stripFencedCodeBlocksForImportTitle = (content: string): string => {
  const withoutClosedBlocks = content.replace(/```[\s\S]*?```/g, "");
  const lines = withoutClosedBlocks.split("\n");
  const output: string[] = [];
  let inUnclosedFence = false;

  for (const line of lines) {
    const trimmed = line.trim();
    if (/^```/.test(trimmed)) {
      inUnclosedFence = true;
      continue;
    }
    if (inUnclosedFence) continue;
    output.push(line);
  }

  return output.join("\n");
};

export const deriveNoteImportTitle = (fileName: string, content: string): string => {
  const contentWithoutCodeBlocks = stripFencedCodeBlocksForImportTitle(content);
  const headingMatch = /^#\s+(.+?)\s*$/m.exec(contentWithoutCodeBlocks);
  if (headingMatch?.[1]) return headingMatch[1].trim();

  const baseName = fileName.replace(NOTE_IMPORT_TITLE_EXTENSIONS, "").trim();
  return baseName || "Untitled note";
};

export const buildVaultNoteFromMarkdownImport = ({
  fileName,
  content,
  group,
  order,
}: {
  fileName: string;
  content: string;
  group: string | null;
  order: number;
}): VaultNote => {
  const now = Date.now();
  return sanitizeVaultNote({
    title: deriveNoteImportTitle(fileName, content),
    content,
    group: group || undefined,
    createdAt: now,
    updatedAt: now,
    order,
  });
};

export const importMarkdownPayloadsToVaultNotes = (
  payloads: Array<{ fileName: string; content: string }>,
  existingNotes: VaultNote[],
  targetGroup: string | null,
): { notes: VaultNote[]; importedCount: number } => {
  const imported: VaultNote[] = [];
  let orderBase = existingNotes;

  for (const { fileName, content } of payloads) {
    const note = buildVaultNoteFromMarkdownImport({
      fileName,
      content,
      group: targetGroup,
      order: getNextVaultOrder([...orderBase, ...imported]),
    });
    imported.push(note);
    orderBase = [...orderBase, note];
  }

  return {
    notes: normalizeVaultNotes([...existingNotes, ...imported]),
    importedCount: imported.length,
  };
};

export const importMarkdownFilesToVaultNotes = async (
  files: File[],
  existingNotes: VaultNote[],
  targetGroup: string | null,
  readFile: (file: File) => Promise<string>,
): Promise<{ notes: VaultNote[]; importedCount: number; skippedCount: number }> => {
  const payloads: Array<{ fileName: string; content: string }> = [];
  let skippedCount = 0;

  for (const file of files) {
    if (!/\.(md|markdown|txt)$/i.test(file.name)) {
      skippedCount += 1;
      continue;
    }

    payloads.push({
      fileName: file.name,
      content: await readFile(file),
    });
  }

  const { notes, importedCount } = importMarkdownPayloadsToVaultNotes(
    payloads,
    existingNotes,
    targetGroup,
  );

  return {
    notes,
    importedCount,
    skippedCount,
  };
};

export interface NoteHeadingItem {
  id: string;
  level: number;
  text: string;
  line: number;
}

export const extractNoteHeadings = (content: string): NoteHeadingItem[] => {
  if (!content) return [];
  const lines = content.split("\n");
  const headings: NoteHeadingItem[] = [];
  let inCodeBlock = false;

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    if (line.trim().startsWith("```")) {
      inCodeBlock = !inCodeBlock;
      continue;
    }
    if (inCodeBlock) continue;

    const match = /^(#{1,6})\s+(.+)$/.exec(line.trim());
    if (match) {
      const level = match[1].length;
      const text = match[2].trim();
      headings.push({
        id: `heading-${i}-${text.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/gi, "-")}`,
        level,
        text,
        line: i + 1,
      });
    }
  }
  return headings;
};

export const extractNoteSnippet = (content: string, maxLength = 100): string => {
  if (!content) return "";
  let text = content.replace(/```[\s\S]*?```/g, " ");
  text = text.replace(/`([^`]+)`/g, "$1");
  text = text.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1");
  text = text.replace(/\[([^\]]*)\]\([^)]*\)/g, "$1");
  text = text.replace(/^#{1,6}\s+/gm, "");
  text = text.replace(/^>\s+/gm, "");
  text = text.replace(/^[-*+]\s+(\[[ xX]\]\s+)?/gm, "");
  text = text.replace(/^\d+\.\s+/gm, "");
  text = text.replace(/\s+/g, " ").trim();
  if (text.length <= maxLength) return text;
  return `${text.slice(0, maxLength).trim()}...`;
};

export const extractAllNoteTags = (notes: VaultNote[]): { tag: string; count: number }[] => {
  const map = new Map<string, number>();
  for (const note of notes) {
    if (note.tags) {
      for (const t of note.tags) {
        const clean = t.trim();
        if (clean) {
          map.set(clean, (map.get(clean) || 0) + 1);
        }
      }
    }
  }
  return Array.from(map.entries())
    .map(([tag, count]) => ({ tag, count }))
    .sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag));
};

export type NoteSortOption =
  | "updatedDesc"
  | "updatedAsc"
  | "createdDesc"
  | "createdAsc"
  | "titleAsc"
  | "titleDesc"
  | "custom";

export type NoteFilterMode = "all" | "pinned" | "recent" | "uncategorized";

export const filterAndSortVaultNotes = (
  notes: VaultNote[],
  options: {
    search?: string;
    group?: string | null;
    tag?: string | null;
    filterMode?: NoteFilterMode;
    sort?: NoteSortOption;
    hosts?: Host[];
  } = {},
): VaultNote[] => {
  const {
    search = "",
    group = null,
    tag = null,
    filterMode = "all",
    sort = "updatedDesc",
    hosts = [],
  } = options;

  const filtered = notes.filter((note) => {
    if (filterMode === "pinned" && !note.isPinned) return false;
    if (filterMode === "uncategorized" && note.group) return false;
    if (filterMode === "recent") {
      const sevenDaysAgo = Date.now() - 7 * 24 * 60 * 60 * 1000;
      if (note.updatedAt < sevenDaysAgo) return false;
    }

    if (group) {
      if (!note.group || !isNoteGroupInside(note.group, group)) return false;
    }

    if (tag) {
      if (!note.tags || !note.tags.includes(tag)) return false;
    }

    if (search.trim() && !matchesVaultNoteSearch(note, search, hosts)) {
      return false;
    }

    return true;
  });

  return [...filtered].sort((a, b) => {
    if (sort !== "custom") {
      if (a.isPinned !== b.isPinned) {
        return a.isPinned ? -1 : 1;
      }
    }

    switch (sort) {
      case "updatedDesc":
        return b.updatedAt - a.updatedAt;
      case "updatedAsc":
        return a.updatedAt - b.updatedAt;
      case "createdDesc":
        return b.createdAt - a.createdAt;
      case "createdAsc":
        return a.createdAt - b.createdAt;
      case "titleAsc":
        return (a.title || "").localeCompare(b.title || "");
      case "titleDesc":
        return (b.title || "").localeCompare(a.title || "");
      case "custom":
      default:
        return (a.order ?? 0) - (b.order ?? 0);
    }
  });
};
