import assert from 'node:assert/strict';
import test from 'node:test';

import {
  exactScopeAISessionsEqual,
  filterAISessionsForScope,
  retainStableAISessionsForScope,
  sessionMatchesAIScope,
} from './aiSessionsForScope.ts';

const session = (
  id: string,
  scopeType: string,
  targetId?: string,
) => ({
  id,
  scope: { type: scopeType, targetId },
});

test('filterAISessionsForScope keeps only matching scope', () => {
  const a = session('a', 'terminal', 't1');
  const b = session('b', 'terminal', 't2');
  const c = session('c', 'workspace', 'w1');
  const all = [a, b, c];
  assert.deepEqual(filterAISessionsForScope(all, 'terminal', 't1'), [a]);
  assert.deepEqual(filterAISessionsForScope(all, 'workspace', 'w1'), [c]);
  assert.equal(sessionMatchesAIScope(a, 'terminal', 't1'), true);
  assert.equal(sessionMatchesAIScope(a, 'terminal', 't2'), false);
});

test('retainStableAISessionsForScope keeps identity when session refs match', () => {
  const a = session('a', 'terminal', 't1');
  const prev = [a];
  const next = [a];
  assert.equal(retainStableAISessionsForScope(prev, next), prev);
  const replaced = [session('a', 'terminal', 't1')];
  assert.notEqual(retainStableAISessionsForScope(prev, replaced), prev);
});

test('exactScopeAISessionsEqual ignores sibling session object churn', () => {
  const a = session('a', 'terminal', 't1');
  const b1 = session('b', 'terminal', 't2');
  const b2 = session('b', 'terminal', 't2'); // new object, sibling stream
  const prev = [a, b1];
  const next = [a, b2];
  assert.equal(exactScopeAISessionsEqual(prev, next, 'terminal', 't1'), true);
  const a2 = session('a', 'terminal', 't1');
  assert.equal(exactScopeAISessionsEqual(prev, [a2, b1], 'terminal', 't1'), false);
});
