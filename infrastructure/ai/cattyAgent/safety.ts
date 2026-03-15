import { DEFAULT_COMMAND_BLOCKLIST } from '../types';

/**
 * Pre-compiled RegExp cache for command blocklist patterns.
 *
 * The blocklist is a best-effort defense-in-depth measure. It is NOT a
 * security boundary — determined users or sophisticated prompt injection
 * can bypass regex-based filtering. The primary security boundary is the
 * permission / confirmation system and OS-level sandboxing.
 */
const compiledDefaultBlocklist: RegExp[] = DEFAULT_COMMAND_BLOCKLIST.flatMap(
  (pattern) => {
    try {
      return [new RegExp(pattern, 'i')];
    } catch {
      return [];
    }
  },
);

/** Cache for user-provided (non-default) blocklist patterns. */
const userPatternCache = new Map<string, RegExp | null>();

function getCompiledPattern(pattern: string): RegExp | null {
  if (userPatternCache.has(pattern)) {
    return userPatternCache.get(pattern)!;
  }
  try {
    const regex = new RegExp(pattern, 'i');
    userPatternCache.set(pattern, regex);
    return regex;
  } catch {
    userPatternCache.set(pattern, null);
    return null;
  }
}

/**
 * Check if a command matches any pattern in the blocklist.
 * Returns the matching pattern if blocked, null if safe.
 *
 * Default blocklist patterns are pre-compiled at module load time.
 * User-provided patterns are compiled once and cached.
 */
export function checkCommandSafety(
  command: string,
  blocklist: string[] = DEFAULT_COMMAND_BLOCKLIST,
): { blocked: boolean; matchedPattern?: string } {
  // Fast path: use pre-compiled regexes for the default blocklist
  if (blocklist === DEFAULT_COMMAND_BLOCKLIST) {
    for (let i = 0; i < compiledDefaultBlocklist.length; i++) {
      if (compiledDefaultBlocklist[i].test(command)) {
        return { blocked: true, matchedPattern: DEFAULT_COMMAND_BLOCKLIST[i] };
      }
    }
    return { blocked: false };
  }

  // User-provided blocklist: compile once and cache each pattern
  for (const pattern of blocklist) {
    const regex = getCompiledPattern(pattern);
    if (regex && regex.test(command)) {
      return { blocked: true, matchedPattern: pattern };
    }
  }
  return { blocked: false };
}
