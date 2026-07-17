import { compressVerboseText } from '../requestPayloadCompression';

const MONITOR_LINE_MAX_CHARS = 500;
const MONITOR_BATCH_MAX_CHARS = 3_000;
const MONITOR_BURST = 10;
const MONITOR_REFILL_MS = 2_000;
const MONITOR_OVERLOAD_STOP_MS = 30_000;

interface MonitorState {
  tokens: number;
  lastRefillAt: number;
  overloadedAt?: number;
  lastSuppressedAt?: number;
  suppressedCount: number;
}

export type MonitorGuardResult =
  | { action: 'deliver'; content: string; suppressedCount: number }
  | { action: 'suppress'; suppressedCount: number }
  | { action: 'stop'; suppressedCount: number };

export class TerminalMonitorGuard {
  private readonly states = new Map<string, MonitorState>();
  private readonly now: () => number;

  constructor(options: { now?: () => number } = {}) {
    this.now = options.now ?? Date.now;
  }

  process(key: string, output: string): MonitorGuardResult {
    const now = this.now();
    const state = this.states.get(key) ?? {
      tokens: MONITOR_BURST,
      lastRefillAt: now,
      suppressedCount: 0,
    };
    if (state.lastSuppressedAt != null && now - state.lastSuppressedAt > MONITOR_REFILL_MS * 2) {
      state.overloadedAt = undefined;
      state.lastSuppressedAt = undefined;
      state.suppressedCount = 0;
    }
    if (state.overloadedAt != null && now - state.overloadedAt >= MONITOR_OVERLOAD_STOP_MS) {
      this.states.delete(key);
      return { action: 'stop', suppressedCount: state.suppressedCount + 1 };
    }

    const refill = Math.floor((now - state.lastRefillAt) / MONITOR_REFILL_MS);
    if (refill > 0) {
      state.tokens = Math.min(MONITOR_BURST, state.tokens + refill);
      state.lastRefillAt += refill * MONITOR_REFILL_MS;
    }
    if (state.tokens <= 0) {
      state.suppressedCount += 1;
      state.overloadedAt ??= now;
      state.lastSuppressedAt = now;
      this.states.set(key, state);
      return { action: 'suppress', suppressedCount: state.suppressedCount };
    }

    state.tokens -= 1;
    const suppressedCount = state.suppressedCount;
    state.suppressedCount = 0;
    this.states.set(key, state);
    const prefix = suppressedCount > 0 ? `[${suppressedCount} monitor batches suppressed]\n` : '';
    return {
      action: 'deliver',
      content: fitMonitorBatch(`${prefix}${output}`),
      suppressedCount,
    };
  }

  clear(key: string): void {
    this.states.delete(key);
  }

  clearPrefix(prefix: string): void {
    for (const key of this.states.keys()) {
      if (key.startsWith(prefix)) this.states.delete(key);
    }
  }
}

export function isStreamingMonitorCommand(command: unknown): boolean {
  if (typeof command !== 'string') return false;
  return command.split(/[;&|]+/).some((rawSegment) => {
    const tokens = unwrapMonitorCommandPrefixes(tokenizeCommandSegment(rawSegment));
    if (tokens.length === 0) return false;
    if (tokens[0]?.toLowerCase() === 'watch') return true;

    const commandTokens = tokens.map(token => token.toLowerCase());
    const argsStart = commandTokens[0] === 'tail' || commandTokens[0] === 'journalctl'
      ? 1
      : commandTokens[0] === 'docker' && commandTokens[1] === 'logs'
        ? 2
        : commandTokens[0] === 'docker' && commandTokens[1] === 'compose' && commandTokens[2] === 'logs'
          ? 3
          : commandTokens[0] === 'kubectl' && commandTokens[1] === 'logs'
            ? 2
            : -1;
    if (argsStart < 0) return false;
    return tokens.slice(argsStart).some(arg => /^--follow(?:=.+)?$/i.test(arg) || /^-[^-\s]*[fF]/.test(arg));
  });
}

function tokenizeCommandSegment(segment: string): string[] {
  return segment.trim().match(/(?:[^\s"'\\]+|"(?:\\.|[^"])*"|'[^']*')+/g) ?? [];
}

function unwrapMonitorCommandPrefixes(input: string[]): string[] {
  const tokens = [...input];
  for (let pass = 0; pass < 8 && tokens.length > 0; pass += 1) {
    const wrapper = tokens[0]?.toLowerCase();
    if (wrapper === 'sudo') {
      tokens.shift();
      consumeWrapperOptions(tokens, new Set([
        '-u', '--user', '-g', '--group', '-h', '--host', '-p', '--prompt',
        '-c', '--close-from', '-r', '--chroot', '-d', '--chdir', '-t', '--command-timeout',
        '-r', '--role', '-t', '--type',
      ]));
      continue;
    }
    if (wrapper === 'env') {
      tokens.shift();
      consumeWrapperOptions(tokens, new Set(['-u', '--unset', '-c', '--chdir', '-s', '--split-string']));
      while (tokens[0] && /^[A-Za-z_][A-Za-z0-9_]*=/.test(tokens[0])) tokens.shift();
      continue;
    }
    if (wrapper === 'stdbuf') {
      tokens.shift();
      consumeWrapperOptions(tokens, new Set(['-i', '--input', '-o', '--output', '-e', '--error']));
      continue;
    }
    if (wrapper === 'timeout') {
      tokens.shift();
      consumeWrapperOptions(tokens, new Set(['-k', '--kill-after', '-s', '--signal']));
      if (tokens.length > 0) tokens.shift();
      continue;
    }
    break;
  }
  return tokens;
}

function consumeWrapperOptions(tokens: string[], optionsWithValues: ReadonlySet<string>): void {
  while (tokens[0]?.startsWith('-')) {
    const option = tokens.shift()!;
    if (option === '--') break;
    const optionName = option.split('=', 1)[0]!.toLowerCase();
    if (optionsWithValues.has(optionName) && !option.includes('=') && tokens.length > 0) tokens.shift();
  }
}

function fitMonitorBatch(output: string): string {
  const normalized = compressVerboseText(output);
  const lines = normalized.split('\n').map(line => {
    if (line.length <= MONITOR_LINE_MAX_CHARS) return line;
    return `${line.slice(0, MONITOR_LINE_MAX_CHARS - 28)}[... line shortened ...]`;
  });
  const content = lines.join('\n');
  if (content.length <= MONITOR_BATCH_MAX_CHARS) return content;
  const marker = '\n[... monitor batch shortened ...]';
  return `${content.slice(0, MONITOR_BATCH_MAX_CHARS - marker.length)}${marker}`;
}

export const globalTerminalMonitorGuard = new TerminalMonitorGuard();
