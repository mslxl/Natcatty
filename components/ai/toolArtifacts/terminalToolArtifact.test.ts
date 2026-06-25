import test from 'node:test';
import assert from 'node:assert/strict';

import { parseTerminalToolArtifact } from './terminalToolArtifact.ts';

test('parseTerminalToolArtifact maps terminal context reads', () => {
  const artifact = parseTerminalToolArtifact('terminal_read_context', {
    ok: true,
    sessionId: 'session-1',
    label: 'prod',
    range: 'tail',
    content: 'alpha\nbeta\ngamma',
    totalLines: 120,
    startLine: 100,
    endLine: 102,
    returnedLines: 3,
    hasMoreBefore: true,
    hasMoreAfter: true,
    source: 'live',
  });

  assert.deepEqual(artifact, {
    kind: 'terminal.context',
    sessionId: 'session-1',
    label: 'prod',
    range: 'tail',
    totalLines: 120,
    startLine: 100,
    endLine: 102,
    returnedLines: 3,
    hasMoreBefore: true,
    hasMoreAfter: true,
    source: 'live',
    preview: 'alpha\nbeta\ngamma',
  });
});

test('parseTerminalToolArtifact maps errors', () => {
  const artifact = parseTerminalToolArtifact('terminal_read_context', {
    ok: false,
    error: 'Terminal session not found.',
  });

  assert.deepEqual(artifact, {
    kind: 'error',
    message: 'Terminal session not found.',
  });
});
