import test from 'node:test';
import assert from 'node:assert/strict';
import {
  clearCodebuddyElicitationsForChat,
  completeCodebuddyElicitation,
  onCodebuddyElicitation,
  onCodebuddyElicitationCleared,
  registerCodebuddyElicitation,
  replayPendingCodebuddyElicitations,
  respondCodebuddyElicitation,
  type CodebuddyElicitation,
} from './codebuddyElicitations';

test('CodeBuddy elicitation gate replays, responds, completes, and clears by chat', async () => {
  const previousWindow = globalThis.window;
  const responses: unknown[][] = [];
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      netcatty: {
        aiSdkAgentElicitationResponse: async (...args: unknown[]) => {
          responses.push(args);
          return { ok: true };
        },
      },
    },
  });

  const received: CodebuddyElicitation[] = [];
  const cleared: string[][] = [];
  const unsubscribe = onCodebuddyElicitation((elicitation) => received.push(elicitation));
  const unsubscribeCleared = onCodebuddyElicitationCleared((ids) => cleared.push(ids));

  registerCodebuddyElicitation({
    elicitationId: 'el-1',
    chatSessionId: 'chat-1',
    request: {
      message: 'Confirm deployment?',
      requestedSchema: {
        type: 'object',
        properties: { environment: { type: 'string' } },
        required: ['environment'],
      },
    },
  });
  assert.equal(received[0]?.elicitationId, 'el-1');

  const replayed: CodebuddyElicitation[] = [];
  replayPendingCodebuddyElicitations((elicitation) => replayed.push(elicitation));
  assert.equal(replayed[0]?.request.message, 'Confirm deployment?');

  await respondCodebuddyElicitation('el-1', 'accept', { environment: 'staging' });
  assert.deepEqual(responses[0], ['el-1', 'accept', { environment: 'staging' }]);
  assert.deepEqual(cleared[0], ['el-1']);

  registerCodebuddyElicitation({
    elicitationId: 'el-2',
    chatSessionId: 'chat-1',
    request: { message: 'Wait for completion' },
  });
  completeCodebuddyElicitation({ elicitationId: 'el-2' });
  assert.deepEqual(cleared[1], ['el-2']);

  registerCodebuddyElicitation({
    elicitationId: 'el-3',
    chatSessionId: 'chat-1',
    request: {},
  });
  registerCodebuddyElicitation({
    elicitationId: 'el-4',
    chatSessionId: 'chat-2',
    request: {},
  });
  clearCodebuddyElicitationsForChat('chat-1');
  assert.deepEqual(cleared[2], ['el-3']);

  const remaining: CodebuddyElicitation[] = [];
  replayPendingCodebuddyElicitations((elicitation) => remaining.push(elicitation));
  assert.deepEqual(remaining.map((elicitation) => elicitation.elicitationId), ['el-4']);

  clearCodebuddyElicitationsForChat('chat-2');
  unsubscribe();
  unsubscribeCleared();
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: previousWindow,
  });
});
