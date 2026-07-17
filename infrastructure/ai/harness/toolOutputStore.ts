export interface ToolOutputHandle {
  id: string;
  capabilityId: string;
  sessionId?: string;
  totalChars: number;
  storedChars: number;
  sourceTruncated: boolean;
  preview: string;
  storedAt: number;
  accessedAt: number;
  fullContent?: string;
  filePath?: string;
  spillPromise?: Promise<void>;
  evicted?: boolean;
}

export interface StoreToolOutputInput {
  chatSessionId: string;
  capabilityId: string;
  content: string;
  sessionId?: string;
  previewChars?: number;
}

export interface ReadToolOutputInput {
  handleId: string;
  mode?: 'head' | 'tail' | 'full' | 'range' | 'search';
  maxChars?: number;
  offset?: number;
  query?: string;
}

export interface ToolOutputReadResult {
  handleId: string;
  mode: NonNullable<ReadToolOutputInput['mode']>;
  content: string;
  totalChars: number;
  storedChars: number;
  sourceTruncated: boolean;
  startOffset: number;
  endOffset: number;
  nextOffset: number;
  hasMore: boolean;
  matchOffsets?: number[];
}

export const TOOL_OUTPUT_READ_MAX_CHARS = 12_000;
export const TOOL_OUTPUT_MAX_HANDLE_CHARS = 4_000_000;
export const TOOL_OUTPUT_MAX_HANDLES_PER_SESSION = 64;
export const TOOL_OUTPUT_MAX_CHARS_PER_SESSION = 8_000_000;
export const TOOL_OUTPUT_MAX_HANDLES_GLOBAL = 256;
export const TOOL_OUTPUT_MAX_CHARS_GLOBAL = 32_000_000;
export const TOOL_OUTPUT_TTL_MS = 30 * 60 * 1_000;
export const TOOL_OUTPUT_SPILL_THRESHOLD_CHARS = 128_000;
const TOOL_OUTPUT_SEARCH_CONTEXT_CHARS = 320;
const TOOL_OUTPUT_SEARCH_MAX_MATCHES = 20;

export interface ToolOutputPersistence {
  write(handleId: string, content: string): Promise<string>;
  read(path: string, input: ReadToolOutputInput): Promise<Omit<ToolOutputReadResult, 'handleId' | 'storedChars' | 'sourceTruncated'> | null>;
  delete(path: string): Promise<void>;
}

export interface ToolOutputStoreOptions {
  maxHandleChars?: number;
  maxHandlesPerSession?: number;
  maxCharsPerSession?: number;
  maxHandlesGlobal?: number;
  maxCharsGlobal?: number;
  ttlMs?: number;
  spillThresholdChars?: number;
  now?: () => number;
  persistence?: ToolOutputPersistence;
}

function nextHandleId(): string {
  const randomId = globalThis.crypto?.randomUUID?.()
    ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `tool-output-${randomId}`;
}

function isHighSurrogate(value: number): boolean {
  return value >= 0xd800 && value <= 0xdbff;
}

function isLowSurrogate(value: number): boolean {
  return value >= 0xdc00 && value <= 0xdfff;
}

function safeSliceBounds(content: string, requestedStart: number, requestedEnd: number): [number, number] {
  let start = Math.min(content.length, Math.max(0, requestedStart));
  let end = Math.min(content.length, Math.max(start, requestedEnd));
  if (start > 0 && start < content.length && isLowSurrogate(content.charCodeAt(start))) {
    start -= 1;
  }
  if (end > start && end < content.length && isHighSurrogate(content.charCodeAt(end - 1))) {
    end -= 1;
  }
  return [start, end];
}

export class ToolOutputStore {
  private readonly bySession = new Map<string, Map<string, ToolOutputHandle>>();
  private readonly maxHandleChars: number;
  private readonly maxHandlesPerSession: number;
  private readonly maxCharsPerSession: number;
  private readonly maxHandlesGlobal: number;
  private readonly maxCharsGlobal: number;
  private readonly ttlMs: number;
  private readonly spillThresholdChars: number;
  private readonly now: () => number;
  private persistence?: ToolOutputPersistence;

  constructor(options: ToolOutputStoreOptions = {}) {
    this.maxHandleChars = options.maxHandleChars ?? TOOL_OUTPUT_MAX_HANDLE_CHARS;
    this.maxHandlesPerSession = options.maxHandlesPerSession ?? TOOL_OUTPUT_MAX_HANDLES_PER_SESSION;
    this.maxCharsPerSession = options.maxCharsPerSession ?? TOOL_OUTPUT_MAX_CHARS_PER_SESSION;
    this.maxHandlesGlobal = options.maxHandlesGlobal ?? TOOL_OUTPUT_MAX_HANDLES_GLOBAL;
    this.maxCharsGlobal = options.maxCharsGlobal ?? TOOL_OUTPUT_MAX_CHARS_GLOBAL;
    this.ttlMs = options.ttlMs ?? TOOL_OUTPUT_TTL_MS;
    this.spillThresholdChars = options.spillThresholdChars ?? TOOL_OUTPUT_SPILL_THRESHOLD_CHARS;
    this.now = options.now ?? Date.now;
    this.persistence = options.persistence;
  }

  setPersistence(persistence: ToolOutputPersistence | undefined): void {
    this.persistence = persistence;
  }

  store(input: StoreToolOutputInput): ToolOutputHandle {
    const previewChars = input.previewChars ?? 240;
    const retainedContent = retainBoundedContent(input.content, this.maxHandleChars);
    const now = this.now();
    const handle: ToolOutputHandle = {
      id: nextHandleId(),
      capabilityId: input.capabilityId,
      sessionId: input.sessionId,
      totalChars: input.content.length,
      storedChars: retainedContent.length,
      sourceTruncated: retainedContent.length < input.content.length,
      preview: retainedContent.slice(0, previewChars),
      storedAt: now,
      accessedAt: now,
      fullContent: retainedContent,
    };
    const sessionMap = this.bySession.get(input.chatSessionId) ?? new Map<string, ToolOutputHandle>();
    sessionMap.set(handle.id, handle);
    this.bySession.set(input.chatSessionId, sessionMap);
    this.enforceSessionLimits(input.chatSessionId, sessionMap);
    this.enforceGlobalLimits();
    if (sessionMap.has(handle.id)) this.startSpill(handle);
    return handle;
  }

  get(handleId: string, chatSessionId?: string): ToolOutputHandle | undefined {
    this.pruneExpired();
    if (chatSessionId) {
      const handle = this.bySession.get(chatSessionId)?.get(handleId);
      if (handle) handle.accessedAt = this.now();
      return handle;
    }
    for (const sessionMap of this.bySession.values()) {
      const handle = sessionMap.get(handleId);
      if (handle) {
        handle.accessedAt = this.now();
        return handle;
      }
    }
    return undefined;
  }

  listPendingHandles(chatSessionId: string): ToolOutputHandle[] {
    this.pruneExpired();
    return [...(this.bySession.get(chatSessionId)?.values() ?? [])];
  }

  read(input: ReadToolOutputInput, chatSessionId?: string): string | null {
    return this.readChunk(input, chatSessionId)?.content ?? null;
  }

  readChunk(input: ReadToolOutputInput, chatSessionId?: string): ToolOutputReadResult | null {
    const handle = this.get(input.handleId, chatSessionId);
    if (!handle) return null;
    if (handle.fullContent == null) return null;
    return buildReadResult(handle, handle.fullContent, input);
  }

  async readChunkAsync(input: ReadToolOutputInput, chatSessionId?: string): Promise<ToolOutputReadResult | null> {
    const handle = this.get(input.handleId, chatSessionId);
    if (!handle) return null;
    await handle.spillPromise;
    if (handle.fullContent != null) return buildReadResult(handle, handle.fullContent, input);
    if (!handle.filePath || !this.persistence) return null;
    const persisted = await this.persistence.read(handle.filePath, input);
    if (!persisted) return null;
    return {
      ...persisted,
      handleId: handle.id,
      totalChars: handle.totalChars,
      storedChars: handle.storedChars,
      sourceTruncated: handle.sourceTruncated,
    };
  }

  prune(chatSessionId: string): void {
    const sessionMap = this.bySession.get(chatSessionId);
    if (sessionMap) {
      for (const handle of sessionMap.values()) this.evictHandle(handle);
    }
    this.bySession.delete(chatSessionId);
  }

  pruneTerminalSession(chatSessionId: string, terminalSessionId: string): void {
    const sessionMap = this.bySession.get(chatSessionId);
    if (!sessionMap) return;
    for (const [handleId, handle] of sessionMap) {
      if (handle.sessionId !== terminalSessionId) continue;
      sessionMap.delete(handleId);
      this.evictHandle(handle);
    }
    if (sessionMap.size === 0) this.bySession.delete(chatSessionId);
  }

  private startSpill(handle: ToolOutputHandle): void {
    if (!this.persistence || (handle.fullContent?.length ?? 0) < this.spillThresholdChars) return;
    const persistence = this.persistence;
    const content = handle.fullContent!;
    handle.spillPromise = persistence.write(handle.id, content).then(async path => {
      if (handle.evicted) {
        await persistence.delete(path);
        return;
      }
      handle.filePath = path;
      handle.fullContent = undefined;
    }).catch(() => {
      // Keep the in-memory copy if persistence is temporarily unavailable.
    });
  }

  private enforceSessionLimits(chatSessionId: string, sessionMap: Map<string, ToolOutputHandle>): void {
    const totalChars = () => [...sessionMap.values()].reduce((sum, item) => sum + item.storedChars, 0);
    while (
      sessionMap.size > this.maxHandlesPerSession
      || totalChars() > this.maxCharsPerSession
    ) {
      const oldest = [...sessionMap.values()].sort((a, b) => a.accessedAt - b.accessedAt)[0];
      if (!oldest) break;
      sessionMap.delete(oldest.id);
      this.evictHandle(oldest);
    }
    if (sessionMap.size === 0) this.bySession.delete(chatSessionId);
  }

  private pruneExpired(): void {
    const cutoff = this.now() - this.ttlMs;
    for (const [chatSessionId, sessionMap] of this.bySession) {
      for (const [handleId, handle] of sessionMap) {
        if (handle.accessedAt > cutoff) continue;
        sessionMap.delete(handleId);
        this.evictHandle(handle);
      }
      if (sessionMap.size === 0) this.bySession.delete(chatSessionId);
    }
  }

  private enforceGlobalLimits(): void {
    const allHandles = () => [...this.bySession.entries()].flatMap(([chatSessionId, sessionMap]) => (
      [...sessionMap.values()].map(handle => ({ chatSessionId, sessionMap, handle }))
    ));
    while (true) {
      const entries = allHandles();
      const totalChars = entries.reduce((sum, entry) => sum + entry.handle.storedChars, 0);
      if (entries.length <= this.maxHandlesGlobal && totalChars <= this.maxCharsGlobal) break;
      const oldest = entries.sort((a, b) => a.handle.accessedAt - b.handle.accessedAt)[0];
      if (!oldest) break;
      oldest.sessionMap.delete(oldest.handle.id);
      this.evictHandle(oldest.handle);
      if (oldest.sessionMap.size === 0) this.bySession.delete(oldest.chatSessionId);
    }
  }

  private evictHandle(handle: ToolOutputHandle): void {
    handle.evicted = true;
    if (handle.filePath && this.persistence) {
      void this.persistence.delete(handle.filePath).catch(() => {});
    }
  }
}

function retainBoundedContent(content: string, maxChars: number): string {
  if (content.length <= maxChars) return content;
  const marker = `\n\n[... source output exceeded local handle limit; ${content.length - maxChars} chars omitted ...]\n\n`;
  if (marker.length >= maxChars) return content.slice(0, maxChars);
  const budget = Math.max(0, maxChars - marker.length);
  const head = Math.floor(budget / 2);
  const tail = budget - head;
  return `${content.slice(0, head)}${marker}${content.slice(-tail)}`;
}

function buildReadResult(
  handle: ToolOutputHandle,
  content: string,
  input: ReadToolOutputInput,
): ToolOutputReadResult {
    const requestedMax = Number.isFinite(input.maxChars)
      ? Math.floor(input.maxChars!)
      : TOOL_OUTPUT_READ_MAX_CHARS;
    const maxChars = Math.min(TOOL_OUTPUT_READ_MAX_CHARS, Math.max(1, requestedMax));
    const mode = input.mode ?? 'head';

    if (mode === 'search') {
      const query = input.query ?? '';
      if (!query) {
        return {
          handleId: handle.id,
          mode,
          content: 'Search query is required.',
          totalChars: handle.totalChars,
          storedChars: handle.storedChars,
          sourceTruncated: handle.sourceTruncated,
          startOffset: 0,
          endOffset: 0,
          nextOffset: 0,
          hasMore: false,
          matchOffsets: [],
        };
      }
      const haystack = content.toLocaleLowerCase();
      const needle = query.toLocaleLowerCase();
      const offsets: number[] = [];
      let cursor = Math.max(0, Math.floor(input.offset ?? 0));
      while (offsets.length < TOOL_OUTPUT_SEARCH_MAX_MATCHES) {
        const match = haystack.indexOf(needle, cursor);
        if (match < 0) break;
        offsets.push(match);
        cursor = match + Math.max(1, needle.length);
      }
      const excerpts: string[] = [];
      for (const match of offsets) {
        const [start, end] = safeSliceBounds(
          content,
          match - TOOL_OUTPUT_SEARCH_CONTEXT_CHARS,
          match + query.length + TOOL_OUTPUT_SEARCH_CONTEXT_CHARS,
        );
        const excerpt = `[match offset=${match}]\n${content.slice(start, end)}`;
        if (excerpts.join('\n\n').length + excerpt.length > maxChars) break;
        excerpts.push(excerpt);
      }
      const rendered = excerpts.join('\n\n');
      const nextOffset = offsets.length > 0
        ? offsets[offsets.length - 1] + Math.max(1, query.length)
        : content.length;
      return {
        handleId: handle.id,
        mode,
        content: rendered || `No matches found for "${query}".`,
        totalChars: handle.totalChars,
        storedChars: handle.storedChars,
        sourceTruncated: handle.sourceTruncated,
        startOffset: Math.max(0, Math.floor(input.offset ?? 0)),
        endOffset: nextOffset,
        nextOffset,
        hasMore: haystack.indexOf(needle, nextOffset) >= 0,
        matchOffsets: offsets,
      };
    }

    let startOffset = 0;
    if (mode === 'tail') {
      startOffset = Math.max(0, content.length - maxChars);
    } else if (mode === 'range') {
      startOffset = Math.min(content.length, Math.max(0, Math.floor(input.offset ?? 0)));
    }
    const [safeStartOffset, safeEndOffset] = safeSliceBounds(
      content,
      startOffset,
      startOffset + maxChars,
    );
    startOffset = safeStartOffset;
    const selected = content.slice(startOffset, safeEndOffset);
    const endOffset = safeEndOffset;
    return {
      handleId: handle.id,
      mode,
      content: selected,
      totalChars: handle.totalChars,
      storedChars: handle.storedChars,
      sourceTruncated: handle.sourceTruncated,
      startOffset,
      endOffset,
      nextOffset: endOffset,
      hasMore: endOffset < content.length,
    };
}

export const globalToolOutputStore = new ToolOutputStore();
