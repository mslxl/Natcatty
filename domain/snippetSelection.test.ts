import assert from 'node:assert/strict';
import test from 'node:test';
import type { Host, Snippet } from './models';
import { deleteSelectedSnippetsFromVault } from './snippetSelection.ts';

test('deleteSelectedSnippetsFromVault removes host bindings for every selected snippet', () => {
  const snippets: Snippet[] = [
    { id: 'login', label: 'Login', command: 'echo login', kind: 'script' },
    {
      id: 'connect',
      label: 'Connect',
      command: 'echo connect',
      kind: 'script',
      trigger: 'onConnect',
      targets: ['host-a', 'host-b'],
    },
    {
      id: 'keep',
      label: 'Keep',
      command: 'echo keep',
      kind: 'script',
      trigger: 'onConnect',
      targets: ['host-a'],
    },
  ];
  const hosts: Host[] = [
    {
      id: 'host-a',
      name: 'Host A',
      host: 'host-a.example.com',
      port: 22,
      username: 'root',
      loginScriptId: 'login',
      connectScriptIds: ['connect', 'keep'],
    },
    {
      id: 'host-b',
      name: 'Host B',
      host: 'host-b.example.com',
      port: 22,
      username: 'root',
      connectScriptIds: ['connect'],
    },
  ];

  const result = deleteSelectedSnippetsFromVault(
    snippets,
    hosts,
    new Set(['login', 'connect', 'missing']),
  );

  assert.deepEqual(result.snippets.map((snippet) => snippet.id), ['keep']);
  assert.equal(result.hosts[0].loginScriptId, undefined);
  assert.deepEqual(result.hosts[0].connectScriptIds, ['keep']);
  assert.deepEqual(result.hosts[1].connectScriptIds, []);
  assert.equal(hosts[0].loginScriptId, 'login');
  assert.deepEqual(hosts[0].connectScriptIds, ['connect', 'keep']);
});
