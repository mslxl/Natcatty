import assert from 'node:assert/strict';
import test from 'node:test';
import {
  getRunnableHostsForSnippet,
  snippetAppliesToHost,
  snippetAppliesToOutputTrigger,
  snippetHasRunTargets,
} from './snippetTargets.ts';
import type { Host, Snippet } from './models';

const baseSnippet: Snippet = {
  id: 's1',
  label: 'test',
  command: 'echo hi',
  package: '',
};

const hosts: Host[] = [
  {
    id: 'host-a',
    label: 'A',
    hostname: 'a.example',
    username: 'root',
    os: 'linux',
    protocol: 'ssh',
    group: 'Production/Web',
    tags: [],
  },
  {
    id: 'host-b',
    label: 'B',
    hostname: 'b.example',
    username: 'root',
    os: 'linux',
    protocol: 'serial',
    tags: [],
  },
];

test('snippetAppliesToHost returns false when targets are empty', () => {
  assert.equal(snippetAppliesToHost(baseSnippet, 'host-a'), false);
  assert.equal(snippetAppliesToHost({ ...baseSnippet, targets: [] }, 'host-a'), false);
});

test('snippetAppliesToHost matches only listed hosts', () => {
  const snippet = { ...baseSnippet, targets: ['host-a', 'host-b'] };
  assert.equal(snippetAppliesToHost(snippet, 'host-a'), true);
  assert.equal(snippetAppliesToHost(snippet, 'host-c'), false);
  assert.equal(snippetAppliesToHost(snippet, undefined), false);
});

test('snippetAppliesToHost matches all hosts when targetsAllHosts is set', () => {
  const snippet = { ...baseSnippet, targetsAllHosts: true };
  assert.equal(snippetAppliesToHost(snippet, 'host-a'), true);
  assert.equal(snippetAppliesToHost(snippet, 'host-c'), true);
  assert.equal(snippetAppliesToHost(snippet, undefined), false);
});

test('snippetHasRunTargets requires explicit scope', () => {
  assert.equal(snippetHasRunTargets(baseSnippet), false);
  assert.equal(snippetHasRunTargets({ ...baseSnippet, targets: ['host-a'] }), true);
  assert.equal(snippetHasRunTargets({ ...baseSnippet, targetsAllHosts: true }), true);
  assert.equal(snippetHasRunTargets({ ...baseSnippet, targetGroups: ['Production'] }), true);
});

test('group targets resolve current nested membership without storing host ids', () => {
  const grouped = { ...baseSnippet, targetGroups: ['Production'] };
  assert.equal(snippetAppliesToHost(grouped, hosts[0]), true);
  assert.equal(snippetAppliesToHost(grouped, { id: 'host-c', group: 'Staging' }), false);
  assert.deepEqual(getRunnableHostsForSnippet(grouped, hosts), [hosts[0]]);

  const moved = [{ ...hosts[0], group: 'Staging' }, hosts[1]];
  assert.deepEqual(getRunnableHostsForSnippet(grouped, moved), []);
});

test('explicit host and group targets form a deduplicated union', () => {
  const extra = { ...hosts[0], id: 'host-c', group: 'Staging' };
  const snippet = {
    ...baseSnippet,
    targets: ['host-a', 'host-c'],
    targetGroups: ['Production'],
  };
  assert.deepEqual(getRunnableHostsForSnippet(snippet, [...hosts, extra]), [hosts[0], extra]);
});

test('snippetAppliesToOutputTrigger applies to current session when targets are unset', () => {
  const snippet = { ...baseSnippet, trigger: 'onConnect' as const };
  assert.equal(snippetAppliesToOutputTrigger(snippet, 'host-a'), false);

  const output = { ...baseSnippet, trigger: 'onOutput' as const };
  assert.equal(snippetAppliesToOutputTrigger(output, 'host-a'), true);
  assert.equal(snippetAppliesToOutputTrigger(output, undefined), false);
});

test('snippetAppliesToOutputTrigger respects explicit host targets', () => {
  const output = {
    ...baseSnippet,
    trigger: 'onOutput' as const,
    targets: ['host-a'],
  };
  assert.equal(snippetAppliesToOutputTrigger(output, 'host-a'), true);
  assert.equal(snippetAppliesToOutputTrigger(output, 'host-b'), false);
});

test('snippetAppliesToOutputTrigger respects dynamic group targets', () => {
  const output = {
    ...baseSnippet,
    trigger: 'onOutput' as const,
    targetGroups: ['Production'],
  };
  assert.equal(snippetAppliesToOutputTrigger(output, hosts[0]), true);
  assert.equal(snippetAppliesToOutputTrigger(output, { id: 'host-c', group: 'Staging' }), false);
});

test('getRunnableHostsForSnippet excludes serial hosts and respects scope', () => {
  assert.deepEqual(
    getRunnableHostsForSnippet({ ...baseSnippet, targets: ['host-a', 'host-b'] }, hosts),
    [hosts[0]],
  );
  assert.deepEqual(
    getRunnableHostsForSnippet({ ...baseSnippet, targetsAllHosts: true }, hosts),
    [hosts[0]],
  );
  assert.deepEqual(getRunnableHostsForSnippet(baseSnippet, hosts), []);
});
