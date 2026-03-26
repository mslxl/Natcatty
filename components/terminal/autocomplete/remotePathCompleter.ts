/**
 * Remote path completion for terminal autocomplete.
 * Lists files/directories on the remote (or local) machine
 * when the user types commands that expect path arguments.
 */

import type { CompletionContext } from "./completionEngine";
import type { FigArg } from "./figSpecLoader";

/** Directory entry returned from IPC */
export interface DirEntry {
  name: string;
  type: "file" | "directory" | "symlink";
}

/** Bridge interface for directory listing */
interface PathBridge {
  listRemoteDir?: (
    sessionId: string,
    path: string,
    foldersOnly: boolean,
    filterPrefix?: string,
    limit?: number,
  ) => Promise<{ success: boolean; entries: DirEntry[] }>;
  listLocalDir?: (
    path: string,
    foldersOnly: boolean,
    filterPrefix?: string,
    limit?: number,
  ) => Promise<{ success: boolean; entries: DirEntry[] }>;
}

function getBridge(): PathBridge | undefined {
  return (window as Window & { netcatty?: PathBridge }).netcatty;
}

// Cache directory listings for 5 seconds. Full-directory cache is shared between
// popup suggestions and cascading sub-directory panels; filtered cache avoids
// repeated round-trips while the user keeps typing within the same directory.
const fullDirCache = new Map<string, { entries: DirEntry[]; timestamp: number }>();
const filteredDirCache = new Map<string, { entries: DirEntry[]; timestamp: number }>();
const inFlightRequests = new Map<string, Promise<DirEntry[]>>();
const CACHE_TTL_MS = 5000;
const MAX_CACHE_SIZE = 30;
const MAX_FILTERED_CACHE_SIZE = 60;

/** Commands that commonly accept file/directory path arguments */
const PATH_COMMANDS = new Set([
  "cd", "ls", "ll", "la", "dir", "cat", "less", "more", "head", "tail",
  "vim", "vi", "nvim", "nano", "emacs", "code", "subl",
  "cp", "mv", "rm", "mkdir", "rmdir", "touch", "chmod", "chown", "chgrp",
  "stat", "file", "source", ".", "bat", "rg", "find", "tree",
  "tar", "zip", "unzip", "gzip", "gunzip",
  "scp", "rsync", "diff",
  "python", "python3", "node", "ruby", "perl", "bash", "sh", "zsh",
]);

/** Commands that only accept directories (not files) */
const FOLDER_ONLY_COMMANDS = new Set(["cd", "mkdir", "rmdir", "pushd"]);

/**
 * Check if the current command context expects a path argument.
 */
export function shouldDoPathCompletion(
  ctx: CompletionContext,
  resolvedArgs?: FigArg | FigArg[],
): { shouldComplete: boolean; foldersOnly: boolean } {
  const currentWord = ctx.currentWord;

  // 1. Typed path trigger: if current word starts with path-like prefix, always complete
  if (currentWord.startsWith("/") || currentWord.startsWith("./") ||
      currentWord.startsWith("../") || currentWord.startsWith("~/") ||
      currentWord === "." || currentWord === ".." || currentWord === "~") {
    const foldersOnly = FOLDER_ONLY_COMMANDS.has(ctx.commandName);
    return { shouldComplete: true, foldersOnly };
  }

  // 2. Fig spec template check
  if (resolvedArgs) {
    const args = Array.isArray(resolvedArgs) ? resolvedArgs : [resolvedArgs];
    for (const arg of args) {
      const templates = Array.isArray(arg.template) ? arg.template : arg.template ? [arg.template] : [];
      if (templates.includes("filepaths") || templates.includes("folders")) {
        return {
          shouldComplete: true,
          foldersOnly: templates.includes("folders") && !templates.includes("filepaths"),
        };
      }
      // Generators field often indicates path completion (e.g., cd)
      if (arg.generators) {
        const foldersOnly = FOLDER_ONLY_COMMANDS.has(ctx.commandName);
        return { shouldComplete: true, foldersOnly };
      }
    }
  }

  // 3. Hardcoded command list (for commands without fig specs)
  if (ctx.wordIndex >= 1 && PATH_COMMANDS.has(ctx.commandName)) {
    // Only if we're past the command name and not typing an option
    if (!currentWord.startsWith("-")) {
      return {
        shouldComplete: true,
        foldersOnly: FOLDER_ONLY_COMMANDS.has(ctx.commandName),
      };
    }
  }

  return { shouldComplete: false, foldersOnly: false };
}

/**
 * Parse the current word into directory-to-list and filter prefix.
 */
export function resolvePathComponents(
  currentWord: string,
  cwd: string | undefined,
): { dirToList: string; filterPrefix: string; pathPrefix: string } {
  // Handle empty input — list CWD
  if (!currentWord || currentWord === "." || currentWord === "~") {
    const dir = currentWord === "~" ? "~" : (cwd || ".");
    return { dirToList: dir, filterPrefix: "", pathPrefix: currentWord ? currentWord + "/" : "" };
  }

  // Find the last path separator
  const lastSlash = currentWord.lastIndexOf("/");

  if (lastSlash >= 0) {
    const dirPart = currentWord.substring(0, lastSlash + 1); // includes trailing /
    const filterPart = currentWord.substring(lastSlash + 1);
    const decodedDirPart = decodeShellPathFragment(dirPart);
    const decodedFilterPart = decodeShellPathFragment(filterPart);

    // Resolve directory
    let dirToList: string;
    if (decodedDirPart.startsWith("/")) {
      dirToList = decodedDirPart;
    } else if (decodedDirPart.startsWith("~/")) {
      dirToList = decodedDirPart; // Let remote shell expand ~
    } else if (decodedDirPart.startsWith("./") || decodedDirPart.startsWith("../")) {
      dirToList = cwd ? `${cwd}/${decodedDirPart}` : decodedDirPart;
    } else {
      dirToList = cwd ? `${cwd}/${decodedDirPart}` : decodedDirPart;
    }

    return { dirToList, filterPrefix: decodedFilterPart, pathPrefix: dirPart };
  }

  // No slash — filter CWD entries by the typed prefix
  return {
    dirToList: cwd || ".",
    filterPrefix: decodeShellPathFragment(currentWord),
    pathPrefix: "",
  };
}

/**
 * Get path completion suggestions.
 */
export async function getPathSuggestions(
  ctx: CompletionContext,
  options: {
    sessionId?: string;
    protocol?: string;
    cwd?: string;
    foldersOnly: boolean;
  },
): Promise<{ name: string; type: DirEntry["type"] }[]> {
  const { sessionId, protocol, cwd, foldersOnly } = options;
  const { dirToList, filterPrefix } = resolvePathComponents(ctx.currentWord, cwd);

  const entries = await listDirectoryEntries(dirToList, {
    sessionId,
    protocol,
    foldersOnly,
    filterPrefix,
    limit: 100,
  });

  return sortPathEntries(entries);
}

/**
 * List directory contents via IPC, with shared caching and in-flight dedup.
 */
export async function listDirectoryEntries(
  dirPath: string,
  options: {
    sessionId?: string;
    protocol?: string;
    foldersOnly: boolean;
    filterPrefix?: string;
    limit?: number;
  },
): Promise<DirEntry[]> {
  const {
    sessionId,
    protocol,
    foldersOnly,
    filterPrefix = "",
    limit = 100,
  } = options;
  const normalizedPrefix = filterPrefix.toLowerCase();
  const maxEntries = clampLimit(limit);
  const baseKey = `${protocol || "auto"}:${sessionId || "local"}:${dirPath}:${foldersOnly}`;
  const fullCacheKey = `${baseKey}:all`;
  const filteredCacheKey = `${baseKey}:prefix:${normalizedPrefix}:${maxEntries}`;

  // Full directory cache can satisfy both full and filtered lookups.
  const fullCached = fullDirCache.get(fullCacheKey);
  if (isFresh(fullCached)) {
    return filterEntries(fullCached.entries, normalizedPrefix, maxEntries);
  }

  if (normalizedPrefix) {
    const filteredCached = filteredDirCache.get(filteredCacheKey);
    if (isFresh(filteredCached)) {
      return filteredCached.entries;
    }
  }

  const inFlightFull = inFlightRequests.get(fullCacheKey);
  if (inFlightFull) {
    return filterEntries(await inFlightFull, normalizedPrefix, maxEntries);
  }

  const requestKey = normalizedPrefix ? filteredCacheKey : fullCacheKey;
  const inFlight = inFlightRequests.get(requestKey);
  if (inFlight) return inFlight;

  // Make IPC call
  const promise = (async (): Promise<DirEntry[]> => {
    try {
      const bridge = getBridge();
      if (!bridge) return [];

      let result: { success: boolean; entries: DirEntry[] };

      if (protocol === "local" || !sessionId) {
        if (!bridge.listLocalDir) return [];
        result = await bridge.listLocalDir(dirPath, foldersOnly, normalizedPrefix || undefined, maxEntries);
      } else {
        if (!bridge.listRemoteDir) return [];
        result = await bridge.listRemoteDir(sessionId, dirPath, foldersOnly, normalizedPrefix || undefined, maxEntries);
      }

      if (result.success) {
        const timestamp = Date.now();
        if (normalizedPrefix) {
          filteredDirCache.set(requestKey, { entries: result.entries, timestamp });
          evictOldest(filteredDirCache, MAX_FILTERED_CACHE_SIZE);
          return result.entries;
        }

        fullDirCache.set(requestKey, { entries: result.entries, timestamp });
        evictOldest(fullDirCache, MAX_CACHE_SIZE);
        return result.entries;
      }

      return [];
    } catch {
      return [];
    } finally {
      inFlightRequests.delete(requestKey);
    }
  })();

  inFlightRequests.set(requestKey, promise);
  return promise;
}

function clampLimit(limit: number): number {
  if (!Number.isFinite(limit)) return 100;
  return Math.max(1, Math.min(200, Math.floor(limit)));
}

function isFresh(
  cached: { entries: DirEntry[]; timestamp: number } | undefined,
): cached is { entries: DirEntry[]; timestamp: number } {
  return Boolean(cached && Date.now() - cached.timestamp < CACHE_TTL_MS);
}

function filterEntries(entries: DirEntry[], filterPrefix: string, limit: number): DirEntry[] {
  if (!filterPrefix) return entries.slice(0, limit);

  const filtered: DirEntry[] = [];
  for (const entry of entries) {
    if (entry.name.toLowerCase().startsWith(filterPrefix)) {
      filtered.push(entry);
      if (filtered.length >= limit) break;
    }
  }
  return filtered;
}

function evictOldest(
  cache: Map<string, { entries: DirEntry[]; timestamp: number }>,
  maxSize: number,
): void {
  while (cache.size > maxSize) {
    const oldestKey = cache.keys().next().value;
    if (!oldestKey) break;
    cache.delete(oldestKey);
  }
}

function decodeShellPathFragment(value: string): string {
  let result = "";
  let escaped = false;

  for (const ch of value) {
    if (escaped) {
      result += ch;
      escaped = false;
      continue;
    }
    if (ch === "\\") {
      escaped = true;
      continue;
    }
    result += ch;
  }

  if (escaped) result += "\\";
  return result;
}

function sortPathEntries(entries: DirEntry[]): DirEntry[] {
  return [...entries].sort((left, right) => {
    const leftRank = left.type === "directory" ? 0 : left.type === "symlink" ? 1 : 2;
    const rightRank = right.type === "directory" ? 0 : right.type === "symlink" ? 1 : 2;
    if (leftRank !== rightRank) return leftRank - rightRank;
    return left.name.localeCompare(right.name, undefined, { sensitivity: "base" });
  });
}
