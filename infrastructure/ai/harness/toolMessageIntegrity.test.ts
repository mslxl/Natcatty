import assert from 'node:assert/strict';
import test from 'node:test';
import type { ModelMessage } from 'ai';
import { repairToolMessageIntegrity } from './toolMessageIntegrity';

test('repairToolMessageIntegrity drops orphan and duplicate tool results', () => {
  const messages: ModelMessage[] = [
    {
      role: 'assistant',
      content: [{ type: 'tool-call', toolCallId: 'call-1', toolName: 'terminal_poll', input: {} }],
    },
    {
      role: 'tool',
      content: [
        { type: 'tool-result', toolCallId: 'call-1', toolName: 'terminal_poll', output: { type: 'text', value: 'first' } },
        { type: 'tool-result', toolCallId: 'orphan', toolName: 'terminal_poll', output: { type: 'text', value: 'orphan' } },
      ],
    },
    {
      role: 'tool',
      content: [{ type: 'tool-result', toolCallId: 'call-1', toolName: 'terminal_poll', output: { type: 'text', value: 'duplicate' } }],
    },
  ];

  const result = repairToolMessageIntegrity(messages);
  const serialized = JSON.stringify(result.messages);
  assert.equal(result.didAdjust, true);
  assert.match(serialized, /first/);
  assert.doesNotMatch(serialized, /orphan|duplicate/);
});

test('repairToolMessageIntegrity completes interrupted tool calls with a synthetic result', () => {
  const messages: ModelMessage[] = [{
    role: 'assistant',
    content: [{ type: 'tool-call', toolCallId: 'call-1', toolName: 'terminal_execute', input: { command: 'deploy' } }],
  }];

  const result = repairToolMessageIntegrity(messages);
  assert.equal(result.messages.length, 2);
  assert.match(JSON.stringify(result.messages[1]), /interrupted before a result was recorded/);
});
