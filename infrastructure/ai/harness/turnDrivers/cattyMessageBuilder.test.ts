import assert from 'node:assert/strict';
import test from 'node:test';
import { ToolOutputStore } from '../toolOutputStore';
import {
  buildCattySdkMessages,
  createContinuationContext,
} from './cattyMessageBuilder';
import type { ChatMessage } from '../../types';

function buildHistory(messages: ChatMessage[]) {
  return buildCattySdkMessages({
    allMessages: messages,
    includeCurrentUserMessage: false,
    trimmed: '',
    continuationContext: createContinuationContext('provider-1', 'openai', 'model-1'),
    chatSessionId: 'chat-1',
    toolOutputStore: new ToolOutputStore(),
    fieldsByMessage: new Map(),
  });
}

test('legacy reasoning parts with an rs_ item id but no encrypted content are dropped from replay', () => {
  const messages: ChatMessage[] = [{
    id: 'assistant-1',
    role: 'assistant',
    content: 'Done.',
    timestamp: 1,
    providerContinuation: {
      source: { providerConfigId: 'provider-1', providerType: 'openai', modelId: 'model-1' },
      reasoningParts: [
        {
          text: 'legacy reasoning',
          providerOptions: { openai: { itemId: 'rs_legacy' } },
        },
      ],
    },
  }];

  const sdkMessages = buildHistory(messages);

  assert.equal(sdkMessages.length, 1);
  // With the legacy reasoning part dropped, only text remains, so the
  // assistant content collapses to a plain string.
  assert.equal(sdkMessages[0].content, 'Done.');
});

test('reasoning parts with encrypted content and non-OpenAI reasoning parts survive replay', () => {
  const messages: ChatMessage[] = [{
    id: 'assistant-1',
    role: 'assistant',
    content: 'Done.',
    timestamp: 1,
    providerContinuation: {
      source: { providerConfigId: 'provider-1', providerType: 'openai', modelId: 'model-1' },
      reasoningParts: [
        {
          text: 'replayable reasoning',
          providerOptions: { openai: { itemId: 'rs_new', reasoningEncryptedContent: 'enc-abc' } },
        },
        {
          text: 'plain reasoning',
        },
      ],
    },
  }];

  const sdkMessages = buildHistory(messages);

  assert.equal(sdkMessages.length, 1);
  const content = sdkMessages[0].content;
  assert.ok(Array.isArray(content));
  assert.deepEqual(
    content.map((part) => (part as { type: string; text?: string }).text),
    ['replayable reasoning', 'plain reasoning', 'Done.'],
  );
});
