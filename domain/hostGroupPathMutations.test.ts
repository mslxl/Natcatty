import assert from 'node:assert/strict';
import test from 'node:test';
import {
  remapSnippetTargetGroupPaths,
  removeSnippetTargetGroupPaths,
} from './hostGroupPathMutations.ts';
import type { Snippet } from './models';

const snippets: Snippet[] = [{
  id: 'script-a',
  label: 'A',
  command: 'echo a',
  kind: 'script',
  targetGroups: ['Production', 'Production/Web', 'Staging'],
}];

test('remapSnippetTargetGroupPaths follows group rename and descendants', () => {
  const next = remapSnippetTargetGroupPaths(snippets, 'Production', 'Platform');
  assert.deepEqual(next[0].targetGroups, ['Platform', 'Platform/Web', 'Staging']);
});

test('removeSnippetTargetGroupPaths removes a deleted group subtree', () => {
  const next = removeSnippetTargetGroupPaths(snippets, ['Production']);
  assert.deepEqual(next[0].targetGroups, ['Staging']);
});
