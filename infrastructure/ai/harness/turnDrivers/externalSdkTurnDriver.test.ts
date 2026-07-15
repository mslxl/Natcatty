import test from 'node:test';
import assert from 'node:assert/strict';
import type { AISession, AgentActivity, ChatMessage, ExternalAgentConfig } from '../../types';
import { SessionStateStore } from '../sessionState';
import { ToolOutputStore } from '../toolOutputStore';
import { ToolResultDedup } from '../toolResultDedup';
import { externalSdkTurnDriver } from './externalSdkTurnDriver';
import { resolveEstimatedUsageFallback, upsertAgentActivity } from './externalSdkEventState';
import type { TurnUiCallbacks } from './types';

test('upsertAgentActivity replaces streaming updates by stable item id', () => {
  const running: AgentActivity = {
    id: 'plan-1',
    type: 'plan_update',
    status: 'running',
    items: [{ text: 'Map events', completed: false }],
  };
  const completed: AgentActivity = {
    ...running,
    status: 'completed',
    items: [{ text: 'Map events', completed: true }],
  };

  const first = upsertAgentActivity(undefined, running);
  const second = upsertAgentActivity(first, completed);

  assert.equal(second.length, 1);
  assert.deepEqual(second[0], completed);
});

test('upsertAgentActivity preserves the order of unrelated activities', () => {
  const search: AgentActivity = {
    id: 'search-1',
    type: 'web_search',
    status: 'completed',
    query: 'Codex events',
  };
  const warning: AgentActivity = {
    id: 'warning-1',
    type: 'warning',
    status: 'completed',
    message: 'Search result unavailable',
  };

  assert.deepEqual(upsertAgentActivity([search], warning), [search, warning]);
});

test('estimated usage is used only when the SDK did not report actual usage', () => {
  assert.deepEqual(resolveEstimatedUsageFallback('12345678', false), {
    inputTokens: 2,
    outputTokens: 0,
    totalTokens: 2,
    estimated: true,
  });
  assert.equal(resolveEstimatedUsageFallback('12345678', true), null);
});

test('plan updates replace the same activity across tool message boundaries', async () => {
  let onEvent: ((event: Record<string, unknown>) => void) | undefined;
  let onDone: (() => void) | undefined;
  const bridge: Record<string, (...args: unknown[]) => unknown> = {
    onAiSdkAgentEvent: (_requestId, callback) => {
      onEvent = callback as (event: Record<string, unknown>) => void;
      return () => {};
    },
    onAiSdkAgentDone: (_requestId, callback) => {
      onDone = callback as () => void;
      return () => {};
    },
    onAiSdkAgentError: () => () => {},
    aiSdkAgentCancel: async () => ({ ok: true }),
    aiSdkAgentStream: async () => {
      queueMicrotask(() => {
        onEvent?.({
          type: 'plan-update',
          itemId: 'plan-1',
          status: 'running',
          items: [{ text: 'Run command', completed: false }],
        });
        onEvent?.({
          type: 'tool-call',
          toolName: 'shell',
          toolCallId: 'tool-1',
          args: { command: 'true' },
        });
        onEvent?.({
          type: 'tool-result',
          toolName: 'shell',
          toolCallId: 'tool-1',
          output: 'ok',
        });
        onEvent?.({
          type: 'plan-update',
          itemId: 'plan-1',
          status: 'completed',
          items: [{ text: 'Run command', completed: true }],
        });
        onDone?.();
      });
      return { ok: true };
    },
  };

  const session: AISession = {
    id: 'chat-1',
    title: 'Test',
    agentId: 'codex',
    scope: { type: 'global' },
    messages: [{ id: 'assistant-1', role: 'assistant', content: '', timestamp: 1 }],
    createdAt: 1,
    updatedAt: 1,
  };
  const ui: TurnUiCallbacks = {
    addMessageToSession: (_sessionId, message) => {
      session.messages.push(message);
    },
    updateLastMessage: (_sessionId, updater) => {
      const lastIndex = session.messages.length - 1;
      session.messages[lastIndex] = updater(session.messages[lastIndex]);
    },
    updateMessageById: (_sessionId, messageId, updater) => {
      const messageIndex = session.messages.findIndex((message) => message.id === messageId);
      if (messageIndex >= 0) {
        session.messages[messageIndex] = updater(session.messages[messageIndex]);
      }
    },
    reportStreamError: () => {},
    setStreamingForScope: () => {},
    getLatestSession: () => session,
  };
  const agentConfig: ExternalAgentConfig = {
    id: 'codex',
    name: 'Codex',
    command: 'codex',
    enabled: true,
    sdkBackend: 'codex',
  };
  const controller = new AbortController();

  await externalSdkTurnDriver.run({
    backend: 'external-sdk',
    chatSessionId: session.id,
    assistantMsgId: 'assistant-1',
    userText: 'Run the plan',
    signal: controller.signal,
    agentConfig,
    attachedImages: [],
    context: {
      terminalSessions: [],
      providers: [],
      toolIntegrationMode: 'mcp',
      selectedUserSkillSlugs: [],
      permissionMode: 'confirm',
    },
    bridge,
    ui,
  }, {
    turnId: 'turn-1',
    chatSessionId: session.id,
    sessionId: session.id,
    backend: 'external-sdk',
    signal: controller.signal,
    emit: () => {},
    toolOutputStore: new ToolOutputStore(),
    toolResultDedup: new ToolResultDedup(),
    sessionStateStore: new SessionStateStore(),
  });

  const planActivities = session.messages
    .flatMap((message: ChatMessage) => message.agentActivities ?? [])
    .filter((activity) => activity.id === 'plan-1');

  assert.equal(planActivities.length, 1);
  assert.equal(planActivities[0].status, 'completed');
  assert.deepEqual(planActivities[0].type === 'plan_update' ? planActivities[0].items : [], [
    { text: 'Run command', completed: true },
  ]);
});
