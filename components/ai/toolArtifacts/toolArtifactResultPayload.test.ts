import test from 'node:test';
import assert from 'node:assert/strict';

import { parseResultPayload } from './toolArtifactResultPayload.ts';

test('parseResultPayload unwraps MCP text content arrays', () => {
  assert.deepEqual(parseResultPayload(JSON.stringify([
    {
      type: 'text',
      text: JSON.stringify({ ok: true, value: 42 }),
    },
  ])), {
    ok: true,
    value: 42,
  });
});

test('parseResultPayload unwraps MCP content objects', () => {
  assert.deepEqual(parseResultPayload({
    content: [
      {
        type: 'text',
        text: JSON.stringify({ ok: true, label: 'prod' }),
      },
    ],
  }), {
    ok: true,
    label: 'prod',
  });
});

test('parseResultPayload preserves plain result objects', () => {
  const payload = { ok: true, name: 'plain' };
  assert.equal(parseResultPayload(payload), payload);
});
