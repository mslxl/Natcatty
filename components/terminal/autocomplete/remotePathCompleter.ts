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
  listRemoteDir?: (sessionId: string, path: string, foldersOnly: boolean) => Promise<{ success: boolean; entries: DirEntry[] }>;
  listLocalDir?: (path: string, foldersOnly: boolean) => Promise<{ success: boolean; entries: DirEntry[] }>;
}

function getBridge(): PathBridge | undefined {
  return (window as Window & { netcatty?: PathBridge }).netcatty;
}

// Cache: sessionId:path → entries (5 second TTL)
const dirCache = new Map<string, { entries: DirEntry[]; timestamp: number }>();
const inFlightRequests = new Map<string, Promise<DirEntry[]>>();
const CACHE_TTL_MS = 5000;
const MAX_CACHE_SIZE = 30;

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

    // Resolve directory
    let dirToList: string;
    if (dirPart.startsWith("/")) {
      dirToList = dirPart;
    } else if (dirPart.startsWith("~/")) {
      dirToList = dirPart; // Let remote shell expand ~
    } else if (dirPart.startsWith("./") || dirPart.startsWith("../")) {
      dirToList = cwd ? `${cwd}/${dirPart}` : dirPart;
    } else {
      dirToList = cwd ? `${cwd}/${dirPart}` : dirPart;
    }

    return { dirToList, filterPrefix: filterPart, pathPrefix: dirPart };
  }

  // No slash — filter CWD entries by the typed prefix
  return {
    dirToList: cwd || ".",
    filterPrefix: currentWord,
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

  // List directory entries
  const entries = await listDirectory(dirToList, sessionId, protocol, foldersOnly);

  // Filter by prefix
  const lowerFilter = filterPrefix.toLowerCase();
  const filtered = entries.filter((e) => {
    if (!lowerFilter) return true;
    return e.name.toLowerCase().startsWith(lowerFilter);
  });

  return filtered;
}

/**
 * List directory contents via IPC, with caching and in-flight dedup.
 */
async function listDirectory(
  dirPath: string,
  sessionId: string | undefined,
  protocol: string | undefined,
  foldersOnly: boolean,
): Promise<DirEntry[]> {
  const cacheKey = `${sessionId || "local"}:${dirPath}:${foldersOnly}`;

  // Check cache
  const cached = dirCache.get(cacheKey);
  if (cached && Date.now() - cached.timestamp < CACHE_TTL_MS) {
    return cached.entries;
  }

  // Check in-flight
  const inFlight = inFlightRequests.get(cacheKey);
  if (inFlight) return inFlight;

  // Make IPC call
  const promise = (async (): Promise<DirEntry[]> => {
    try {
      const bridge = getBridge();
      if (!bridge) return [];

      let result: { success: boolean; entries: DirEntry[] };

      if (protocol === "local" || !sessionId) {
        if (!bridge.listLocalDir) return [];
        result = await bridge.listLocalDir(dirPath, foldersOnly);
      } else {
        if (!bridge.listRemoteDir) return [];
        result = await bridge.listRemoteDir(sessionId, dirPath, foldersOnly);
      }

      if (result.success) {
        // Update cache
        dirCache.set(cacheKey, { entries: result.entries, timestamp: Date.now() });
        // Evict old entries
        if (dirCache.size > MAX_CACHE_SIZE) {
          const oldestKey = dirCache.keys().next().value;
          if (oldestKey) dirCache.delete(oldestKey);
        }
        return result.entries;
      }

      return [];
    } catch {
      return [];
    } finally {
      inFlightRequests.delete(cacheKey);
    }
  })();

  inFlightRequests.set(cacheKey, promise);
  return promise;
}
