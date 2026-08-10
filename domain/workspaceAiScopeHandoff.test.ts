import assert from 'node:assert/strict';
import test from 'node:test';

import {
  handoffDissolvedWorkspaceAIScope,
  seedWorkspaceAIActiveSessionFromMembers,
} from './workspaceAiScopeHandoff.ts';

test('seedWorkspaceAIActiveSessionFromMembers writes focused terminal chat onto a new workspace', () => {
  const next = seedWorkspaceAIActiveSessionFromMembers({
    activeSessionIdMap: {
      'terminal:term-a': 'chat-a',
      'terminal:term-b': 'chat-b',
    },
    workspaceId: 'ws-1',
    memberTerminalIds: ['term-a', 'term-b'],
    preferredTerminalId: 'term-a',
  });

  assert.deepEqual(next, {
    'terminal:term-a': 'chat-a',
    'terminal:term-b': 'chat-b',
    'workspace:ws-1': 'chat-a',
  });
});

test('seedWorkspaceAIActiveSessionFromMembers leaves an existing workspace active alone', () => {
  assert.equal(
    seedWorkspaceAIActiveSessionFromMembers({
      activeSessionIdMap: {
        'workspace:ws-1': 'chat-ws',
        'terminal:term-a': 'chat-a',
      },
      workspaceId: 'ws-1',
      memberTerminalIds: ['term-a'],
      preferredTerminalId: 'term-a',
    }),
    null,
  );
});

test('handoffDissolvedWorkspaceAIScope copies active chat and remints workspace sessions', () => {
  const result = handoffDissolvedWorkspaceAIScope({
    activeSessionIdMap: {
      'workspace:ws-1': 'chat-ws',
      'terminal:term-b': 'chat-b',
    },
    sessions: [
      {
        id: 'chat-ws',
        scope: { type: 'workspace', targetId: 'ws-1', hostIds: ['host-a'] },
        updatedAt: 1,
      },
      {
        id: 'chat-b',
        scope: { type: 'terminal', targetId: 'term-b', hostIds: ['host-b'] },
        updatedAt: 1,
      },
    ],
    workspaceId: 'ws-1',
    terminalIds: ['term-a', 'term-b'],
    preferredTerminalId: 'term-a',
  });

  assert.equal(result.changed, true);
  assert.equal(result.activeSessionIdMap['terminal:term-a'], 'chat-ws');
  assert.equal(result.sessions[0]?.scope.type, 'terminal');
  assert.equal(result.sessions[0]?.scope.targetId, 'term-a');
  assert.equal(result.sessions[1]?.scope.targetId, 'term-b');
});

test('handoffDissolvedWorkspaceAIScope is a no-op without a preferred terminal', () => {
  const sessions = [
    {
      id: 'chat-ws',
      scope: { type: 'workspace', targetId: 'ws-1' },
      updatedAt: 1,
    },
  ];
  const result = handoffDissolvedWorkspaceAIScope({
    activeSessionIdMap: { 'workspace:ws-1': 'chat-ws' },
    sessions,
    workspaceId: 'ws-1',
    terminalIds: [],
  });

  assert.equal(result.changed, false);
  assert.equal(result.sessions, sessions);
});
