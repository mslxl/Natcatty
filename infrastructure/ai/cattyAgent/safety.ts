import { DEFAULT_COMMAND_BLOCKLIST } from '../types';

/**
 * Check if a command matches any pattern in the blocklist.
 * Returns the matching pattern if blocked, null if safe.
 */
export function checkCommandSafety(
  command: string,
  blocklist: string[] = DEFAULT_COMMAND_BLOCKLIST,
): { blocked: boolean; matchedPattern?: string } {
  for (const pattern of blocklist) {
    try {
      const regex = new RegExp(pattern, 'i');
      if (regex.test(command)) {
        return { blocked: true, matchedPattern: pattern };
      }
    } catch {
      // Invalid regex pattern, skip
    }
  }
  return { blocked: false };
}

/**
 * Detect if the agent is in a doom loop (repeating the same actions).
 */
export function detectDoomLoop(
  recentToolCalls: Array<{ name: string; arguments: Record<string, unknown> }>,
  maxRepeats: number = 3,
): boolean {
  if (recentToolCalls.length < maxRepeats) return false;

  // Check if the last N tool calls are identical
  const lastN = recentToolCalls.slice(-maxRepeats);
  const serialized = lastN.map(tc => `${tc.name}:${JSON.stringify(tc.arguments)}`);
  const allSame = serialized.every(s => s === serialized[0]);

  if (allSame) return true;

  // Check for alternating patterns (A-B-A-B)
  if (recentToolCalls.length >= maxRepeats * 2) {
    const pairs = recentToolCalls.slice(-maxRepeats * 2);
    const pairA = `${pairs[0].name}:${JSON.stringify(pairs[0].arguments)}`;
    const pairB = `${pairs[1].name}:${JSON.stringify(pairs[1].arguments)}`;
    if (pairA !== pairB) {
      const isAlternating = pairs.every((tc, i) => {
        const expected = i % 2 === 0 ? pairA : pairB;
        return `${tc.name}:${JSON.stringify(tc.arguments)}` === expected;
      });
      if (isAlternating) return true;
    }
  }

  return false;
}
