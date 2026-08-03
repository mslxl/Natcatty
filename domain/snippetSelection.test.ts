import assert from 'node:assert/strict';
import test from 'node:test';
import type { Snippet } from './models';
import { removeSelectedSnippets } from './snippetSelection.ts';

test('removeSelectedSnippets removes all selected snippets in one pass', () => {
  const snippets: Snippet[] = [
    { id: 'keep-1', label: 'Keep first', command: 'pwd' },
    { id: 'delete-1', label: 'Delete first', command: 'uptime' },
    { id: 'delete-2', label: 'Delete second', command: 'whoami', kind: 'script' },
    { id: 'keep-2', label: 'Keep second', command: 'date' },
  ];

  const result = removeSelectedSnippets(snippets, new Set(['delete-1', 'delete-2']));

  assert.deepEqual(result.map((snippet) => snippet.id), ['keep-1', 'keep-2']);
  assert.equal(snippets.length, 4);
});

test('removeSelectedSnippets ignores stale selection ids', () => {
  const snippets: Snippet[] = [
    { id: 'keep', label: 'Keep', command: 'pwd' },
  ];

  const result = removeSelectedSnippets(snippets, new Set(['missing']));

  assert.deepEqual(result, snippets);
  assert.notEqual(result, snippets);
});
