import assert from 'node:assert/strict';
import test from 'node:test';
import { ToolOutputStore } from './toolOutputStore';
import { fitLargeUserInputForModel } from './largeUserInput';

test('fitLargeUserInputForModel keeps both ends and stores the full prompt', () => {
  const store = new ToolOutputStore();
  const input = `START-${'middle '.repeat(8_000)}-FINAL QUESTION`;
  const fitted = fitLargeUserInputForModel(input, 'chat-1', store);

  assert.match(fitted, /^START-/);
  assert.match(fitted, /FINAL QUESTION/);
  assert.match(fitted, /handleId=tool-output-/);
  assert.ok(fitted.length < input.length);
  const handle = store.listPendingHandles('chat-1')[0];
  assert.equal(handle.fullContent, input);
});
