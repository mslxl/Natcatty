import assert from 'node:assert/strict';
import test from 'node:test';
import { TOOL_OUTPUT_READ_MAX_CHARS, ToolOutputStore } from './toolOutputStore';
import { ToolResultDedup } from './toolResultDedup';

test('ToolOutputStore stores and reads truncated output by handle', () => {
  const store = new ToolOutputStore();
  const handle = store.store({
    chatSessionId: 'chat-1',
    capabilityId: 'terminal.execute',
    sessionId: 'sess-1',
    content: 'A'.repeat(50_000),
  });

  assert.ok(handle.id.startsWith('tool-output-'));
  assert.equal(handle.totalChars, 50_000);

  const head = store.read({ handleId: handle.id, mode: 'head', maxChars: 100 }, 'chat-1');
  assert.equal(head?.length, 100);

  const tail = store.read({ handleId: handle.id, mode: 'tail', maxChars: 50 }, 'chat-1');
  assert.equal(tail?.length, 50);
  assert.equal(tail, 'A'.repeat(50));

  store.prune('chat-1');
  assert.equal(store.read({ handleId: handle.id }, 'chat-1'), null);
});

test('ToolOutputStore pages large output with a hard per-read cap', () => {
  const store = new ToolOutputStore();
  const content = `${'0123456789'.repeat(3_000)}END`;
  const handle = store.store({
    chatSessionId: 'chat-1',
    capabilityId: 'terminal.execute',
    content,
  });

  const first = store.readChunk({
    handleId: handle.id,
    mode: 'range',
    maxChars: content.length,
  }, 'chat-1');
  assert.equal(first?.content.length, TOOL_OUTPUT_READ_MAX_CHARS);
  assert.equal(first?.nextOffset, TOOL_OUTPUT_READ_MAX_CHARS);
  assert.equal(first?.hasMore, true);

  const second = store.readChunk({
    handleId: handle.id,
    mode: 'range',
    offset: first?.nextOffset,
  }, 'chat-1');
  assert.equal(second?.startOffset, first?.nextOffset);
});

test('ToolOutputStore searches stored output without returning the whole body', () => {
  const store = new ToolOutputStore();
  const handle = store.store({
    chatSessionId: 'chat-1',
    capabilityId: 'terminal.execute',
    content: `${'noise\n'.repeat(10_000)}Unique Failure Marker\n${'more noise\n'.repeat(10_000)}`,
  });

  const result = store.readChunk({
    handleId: handle.id,
    mode: 'search',
    query: 'unique failure marker',
  }, 'chat-1');
  assert.deepEqual(result?.matchOffsets.length, 1);
  assert.match(result?.content ?? '', /Unique Failure Marker/);
  assert.ok((result?.content.length ?? Infinity) < TOOL_OUTPUT_READ_MAX_CHARS);
});

test('ToolOutputStore never splits a Unicode surrogate pair at page boundaries', () => {
  const store = new ToolOutputStore();
  const content = `${'a'.repeat(11_999)}😀中文结尾`;
  const handle = store.store({
    chatSessionId: 'chat-1',
    capabilityId: 'terminal.execute',
    content,
  });

  const first = store.readChunk({ handleId: handle.id, mode: 'range' }, 'chat-1');
  assert.equal(first?.content.endsWith('\ud83d'), false);
  const second = store.readChunk({
    handleId: handle.id,
    mode: 'range',
    offset: first?.nextOffset,
  }, 'chat-1');
  assert.equal(`${first?.content}${second?.content}`, content);
});

test('ToolOutputStore enforces per-handle, session count, and TTL limits', () => {
  let now = 1_000;
  const store = new ToolOutputStore({
    maxHandleChars: 20,
    maxHandlesPerSession: 2,
    maxCharsPerSession: 30,
    ttlMs: 100,
    now: () => now,
  });
  const first = store.store({ chatSessionId: 'chat-1', capabilityId: 'test', content: 'a'.repeat(15) });
  const second = store.store({ chatSessionId: 'chat-1', capabilityId: 'test', content: 'b'.repeat(15) });
  const third = store.store({ chatSessionId: 'chat-1', capabilityId: 'test', content: 'c'.repeat(100) });

  assert.equal(store.get(first.id, 'chat-1'), undefined);
  assert.equal(store.get(second.id, 'chat-1'), undefined);
  assert.equal(store.get(third.id, 'chat-1')?.storedChars, 20);
  assert.equal(store.get(third.id, 'chat-1')?.sourceTruncated, true);

  now += 101;
  assert.equal(store.get(third.id, 'chat-1'), undefined);
});

test('ToolOutputStore spills retained output through its persistence adapter', async () => {
  const files = new Map<string, string>();
  const deleted: string[] = [];
  const store = new ToolOutputStore({
    spillThresholdChars: 10,
    persistence: {
      write: async (_handleId, content) => {
        files.set('/netcatty/tool-output.log', content);
        return '/netcatty/tool-output.log';
      },
      read: async (path, input) => {
        const content = files.get(path);
        if (content == null) return null;
        const startOffset = input.mode === 'tail'
          ? Math.max(0, content.length - (input.maxChars ?? 12_000))
          : Math.max(0, input.offset ?? 0);
        const selected = content.slice(startOffset, startOffset + (input.maxChars ?? 12_000));
        const endOffset = startOffset + selected.length;
        return {
          mode: input.mode ?? 'head',
          content: selected,
          totalChars: content.length,
          startOffset,
          endOffset,
          nextOffset: endOffset,
          hasMore: endOffset < content.length,
        };
      },
      delete: async path => {
        deleted.push(path);
        files.delete(path);
      },
    },
  });
  const handle = store.store({
    chatSessionId: 'chat-1',
    capabilityId: 'terminal.execute',
    content: 'persist this terminal output',
  });

  const result = await store.readChunkAsync({ handleId: handle.id, mode: 'full' }, 'chat-1');
  assert.equal(result?.content, 'persist this terminal output');
  assert.equal(store.get(handle.id, 'chat-1')?.fullContent, undefined);
  store.prune('chat-1');
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(deleted, ['/netcatty/tool-output.log']);
});

test('ToolOutputStore enforces a shared quota across chat sessions', () => {
  const store = new ToolOutputStore({
    maxCharsGlobal: 25,
    maxHandlesGlobal: 2,
  });
  const first = store.store({ chatSessionId: 'chat-1', capabilityId: 'test', content: 'a'.repeat(15) });
  const second = store.store({ chatSessionId: 'chat-2', capabilityId: 'test', content: 'b'.repeat(15) });

  assert.equal(store.get(first.id, 'chat-1'), undefined);
  assert.ok(store.get(second.id, 'chat-2'));
});

test('ToolOutputStore rejects cross-chat handle reads', async () => {
  const store = new ToolOutputStore();
  const handle = store.store({
    chatSessionId: 'chat-owner',
    capabilityId: 'terminal.execute',
    content: 'private output',
  });

  assert.equal(await store.readChunkAsync({ handleId: handle.id }, 'chat-other'), null);
});

test('saved-output read budgets reset at the start of each turn', () => {
  const dedup = new ToolResultDedup();
  dedup.beginTurn();
  assert.equal(dedup.takeBudget('read', 24_000, 24_000), 24_000);
  assert.equal(dedup.takeBudget('read', 1, 24_000), 0);
  dedup.beginTurn();
  assert.equal(dedup.takeBudget('read', 24_000, 24_000), 24_000);
});
