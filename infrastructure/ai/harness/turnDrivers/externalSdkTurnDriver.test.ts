import test from 'node:test';
import assert from 'node:assert/strict';
import type { AgentActivity } from '../../types';
import { resolveEstimatedUsageFallback, upsertAgentActivity } from './externalSdkEventState';

test('upsertAgentActivity replaces streaming updates by stable item id', () => {
  const running: AgentActivity = {
    id: 'plan-1',
    type: 'plan_update',
    status: 'running',
    items: [{ text: 'Map events', completed: false }],
  };
  const completed: AgentActivity = {
    ...running,
    status: 'completed',
    items: [{ text: 'Map events', completed: true }],
  };

  const first = upsertAgentActivity(undefined, running);
  const second = upsertAgentActivity(first, completed);

  assert.equal(second.length, 1);
  assert.deepEqual(second[0], completed);
});

test('upsertAgentActivity preserves the order of unrelated activities', () => {
  const search: AgentActivity = {
    id: 'search-1',
    type: 'web_search',
    status: 'completed',
    query: 'Codex events',
  };
  const warning: AgentActivity = {
    id: 'warning-1',
    type: 'warning',
    status: 'completed',
    message: 'Search result unavailable',
  };

  assert.deepEqual(upsertAgentActivity([search], warning), [search, warning]);
});

test('estimated usage is used only when the SDK did not report actual usage', () => {
  assert.deepEqual(resolveEstimatedUsageFallback('12345678', false), {
    inputTokens: 2,
    outputTokens: 0,
    totalTokens: 2,
    estimated: true,
  });
  assert.equal(resolveEstimatedUsageFallback('12345678', true), null);
});
