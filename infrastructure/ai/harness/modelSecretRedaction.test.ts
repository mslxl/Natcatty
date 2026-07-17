import assert from 'node:assert/strict';
import test from 'node:test';
import { fitTerminalExecuteResultForModel } from './terminalCompression';
import { fitLargeToolResultForModel } from './toolResultFitting';
import { ToolOutputStore } from './toolOutputStore';
import { redactSecretsForModel, redactSecretsInValueForModel } from './modelSecretRedaction';

test('redactSecretsForModel removes common terminal secrets', () => {
  const input = [
    'API_TOKEN=tok_live_1234567890',
    'Authorization: Bearer abc.def.very-secret',
    'postgres://admin:p4ssw0rd@db.internal/app',
    '-----BEGIN PRIVATE KEY-----',
    'super-secret-private-key-body',
    '-----END PRIVATE KEY-----',
  ].join('\n');
  const output = redactSecretsForModel(input);

  assert.doesNotMatch(output, /tok_live|abc\.def|p4ssw0rd|private-key-body/);
  assert.match(output, /\[REDACTED\]/);
});

test('redactSecretsInValueForModel recursively redacts tool arguments', () => {
  const value = redactSecretsInValueForModel({
    command: 'curl --password swordfish',
    nested: ['Authorization: Bearer secret_token_123456'],
  });
  assert.doesNotMatch(JSON.stringify(value), /swordfish|secret_token/);
});

test('terminal fitting keeps raw output in the local handle but redacts model-visible text', () => {
  const store = new ToolOutputStore();
  const secret = 'API_TOKEN=tok_live_1234567890';
  const fitted = fitTerminalExecuteResultForModel({
    stdout: `${secret}\n${'build line\n'.repeat(10_000)}`,
    stderr: '',
    exitCode: 1,
    command: `deploy --token tok_live_1234567890`,
    sessionId: 'session-1',
  }, {
    chatSessionId: 'chat-1',
    toolOutputStore: store,
  });

  assert.doesNotMatch(fitted.stdout, /tok_live/);
  assert.doesNotMatch(fitted.command ?? '', /tok_live/);
  const handle = store.listPendingHandles('chat-1')[0];
  assert.match(handle.fullContent, /tok_live/);
});

test('generic tool fitting redacts short strings even when truncation is unnecessary', () => {
  const fitted = fitLargeToolResultForModel({
    result: { message: 'password=hunter2' },
    capabilityId: 'example.read',
  }) as { message: string };

  assert.equal(fitted.message, 'password=[REDACTED]');
});
