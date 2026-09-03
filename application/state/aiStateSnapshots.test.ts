import assert from 'node:assert/strict';
import test from 'node:test';
import type { AISession } from '../../infrastructure/ai/types';
import {
  cleanupClosedTerminalSessions,
  cleanupDeletedAIChatSessions,
  cleanupSdkAgentSessions,
} from './aiStateSnapshots';

test('orphan cleanup keeps durable Catty output while explicit deletion removes it', async () => {
  const sdkCleanups: string[] = [];
  const outputCleanups: string[] = [];
  const terminalOutputCleanups: string[] = [];
  const previousWindow = globalThis.window;
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      netcatty: {
        aiSdkAgentCleanup: async (chatSessionId: string) => {
          sdkCleanups.push(chatSessionId);
          return { ok: true };
        },
        deleteChatToolOutputsTemp: async (chatSessionId: string) => {
          outputCleanups.push(chatSessionId);
          return { deletedCount: 1 };
        },
        deleteTerminalToolOutputsEverywhereTemp: async (terminalSessionId: string) => {
          terminalOutputCleanups.push(terminalSessionId);
          return { deletedCount: 1 };
        },
      },
    },
  });

  try {
    cleanupSdkAgentSessions(['history-kept']);
    cleanupDeletedAIChatSessions(['history-deleted']);
    cleanupClosedTerminalSessions(['terminal-closed', 'terminal-closed']);
    await new Promise(resolve => setTimeout(resolve, 0));

    assert.deepEqual(sdkCleanups, ['history-kept', 'history-deleted']);
    assert.deepEqual(outputCleanups, ['history-deleted']);
    assert.deepEqual(terminalOutputCleanups, ['terminal-closed']);
  } finally {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: previousWindow,
    });
  }
});

function makeSession(id: string, updatedAt: number, messages: unknown[]): AISession {
  return {
    id,
    title: id,
    agentId: 'agent',
    scope: { type: 'terminal', targetId: 't' },
    messages: messages as never,
    createdAt: updatedAt,
    updatedAt,
  } as never;
}

test('serializeSessionsForStorage strips oldest ciphertext before dropping visible sessions', async () => {
  const { serializeSessionsForStorage } = await import('./aiStateSnapshots');
  const ciphertext = 'gAAAA'.repeat(50000); // ~250 KB of ciphertext per message
  const messages = () => [{
    id: 'm',
    role: 'assistant' as const,
    content: 'hello',
    timestamp: 0,
    providerContinuation: {
      reasoningParts: [{ text: '', providerOptions: { openai: { reasoningEncryptedContent: ciphertext } } }],
    },
  }];
  const sessions = [
    makeSession('newest', 3, messages()),
    makeSession('older', 2, messages()),
    makeSession('oldest', 1, messages()),
  ];

  const hasCiphertext = (result: { sessions: AISession[] }) =>
    result.sessions.some(s => s.messages.some(m =>
      m.providerContinuation?.reasoningParts?.some(p => typeof p.providerOptions?.openai?.reasoningEncryptedContent === 'string')));

  // Removing replay-only ciphertext from the oldest session fits the budget,
  // so every visible chat remains available after restart.
  const withOldestCiphertextStripped = serializeSessionsForStorage(sessions, 600 * 1024);
  assert.deepEqual(
    withOldestCiphertextStripped.sessions.map(s => s.id),
    ['newest', 'older', 'oldest'],
  );
  assert.equal(hasCiphertext(withOldestCiphertextStripped), true);
  assert.ok(withOldestCiphertextStripped.json.length <= 600 * 1024);

  // Tight budget that even a single session exceeds: ciphertext stripped but
  // the visible conversation content survives.
  const withCiphertextStripped = serializeSessionsForStorage(sessions, 220 * 1024);
  assert.ok(withCiphertextStripped.json.length <= 220 * 1024);
  assert.equal(hasCiphertext(withCiphertextStripped), false);
  assert.deepEqual(withCiphertextStripped.sessions.map(s => s.id), ['newest', 'older', 'oldest']);
  assert.equal(withCiphertextStripped.sessions[0].messages[0].content, 'hello');
});

test('writeSessionsForStorage retries below nominal budgets after a quota failure', async () => {
  const { writeSessionsForStorage } = await import('./aiStateSnapshots');
  const writes: string[] = [];
  let stored: string | undefined;
  const previousLocalStorage = globalThis.localStorage;
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: () => null,
      setItem: (_key: string, value: string) => {
        writes.push(value);
        if (value.length > 350 * 1024) {
          throw new DOMException('quota exceeded', 'QuotaExceededError');
        }
        stored = value;
      },
      removeItem: () => {},
    },
  });
  try {
    // The retry loop must attempt progressively smaller payloads (here the
    // ciphertext stripping at the tighter budget) before reporting failure.
    const huge = 'x'.repeat(260 * 1024);
    const ciphertext = 'gAAAA'.repeat(30 * 1024); // ~150 KB
    const sessions = [makeSession('s', 1, [{
      id: 'm',
      role: 'user' as const,
      content: huge,
      timestamp: 0,
      providerContinuation: {
        reasoningParts: [{ text: '', providerOptions: { openai: { reasoningEncryptedContent: ciphertext } } }],
      },
    }])];
    assert.equal(writeSessionsForStorage(sessions), true);
    assert.equal(writes.length, 2);
    assert.equal(stored, writes[1]);
    assert.ok(writes[writes.length - 1].length < writes[0].length);
  } finally {
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      value: previousLocalStorage,
    });
  }
});
