/**
 * Claude Agent SDK Adapter
 *
 * Bridges Claude Code via the @anthropic-ai/claude-agent-sdk through IPC.
 * The main process runs `query()` and forwards SDK events to the renderer.
 */

import type { ExternalAgentConfig } from './types';

export interface ClaudeAgentCallbacks {
  onTextDelta: (text: string) => void;
  onThinkingDelta: (text: string) => void;
  onThinkingDone: () => void;
  onToolCall: (toolName: string, args: Record<string, unknown>) => void;
  onToolResult: (toolCallId: string, result: string) => void;
  onError: (error: string) => void;
  onDone: () => void;
}

interface ClaudeBridge {
  aiClaudeStream(
    requestId: string,
    chatSessionId: string,
    prompt: string,
    model?: string,
  ): Promise<{ ok: boolean; error?: string }>;
  aiClaudeCancel(requestId: string): Promise<{ ok: boolean }>;
  onAiClaudeEvent(requestId: string, cb: (event: ClaudeSDKEvent) => void): () => void;
  onAiClaudeDone(requestId: string, cb: () => void): () => void;
  onAiClaudeError(requestId: string, cb: (error: string) => void): () => void;
}

interface ClaudeSDKEvent {
  type: string;
  [key: string]: unknown;
}

/**
 * Run a Claude Agent SDK turn.
 * Sends the prompt to the main process which runs query() with the Claude Agent SDK.
 * SDK events are forwarded back via IPC.
 */
export async function runClaudeAgentTurn(
  bridge: Record<string, (...args: unknown[]) => unknown>,
  requestId: string,
  chatSessionId: string,
  _config: ExternalAgentConfig,
  prompt: string,
  callbacks: ClaudeAgentCallbacks,
  signal?: AbortSignal,
  model?: string,
): Promise<void> {
  const claudeBridge = bridge as unknown as ClaudeBridge;

  const cleanupFns: (() => void)[] = [];

  // Set up event listeners before starting stream
  const unsubEvent = claudeBridge.onAiClaudeEvent(requestId, (event: ClaudeSDKEvent) => {
    handleClaudeEvent(event, callbacks);
  });
  cleanupFns.push(unsubEvent);

  const donePromise = new Promise<void>((resolve) => {
    const unsubDone = claudeBridge.onAiClaudeDone(requestId, () => {
      callbacks.onDone();
      resolve();
    });
    cleanupFns.push(unsubDone);

    const unsubError = claudeBridge.onAiClaudeError(requestId, (error: string) => {
      callbacks.onError(error);
      resolve();
    });
    cleanupFns.push(unsubError);
  });

  // Handle abort
  if (signal) {
    if (signal.aborted) {
      cleanup(cleanupFns);
      return;
    }
    const onAbort = () => {
      claudeBridge.aiClaudeCancel(requestId).catch(() => {});
    };
    signal.addEventListener('abort', onAbort, { once: true });
    cleanupFns.push(() => signal.removeEventListener('abort', onAbort));
  }

  // Start the Claude stream in the main process
  claudeBridge.aiClaudeStream(
    requestId,
    chatSessionId,
    prompt,
    model,
  ).catch((err: Error) => {
    callbacks.onError(err.message);
  });

  // Wait for done or error
  await donePromise;
  cleanup(cleanupFns);
}

function cleanup(fns: (() => void)[]) {
  for (const fn of fns) {
    try { fn(); } catch { /* */ }
  }
}

/**
 * Handle a single event from the Claude Agent SDK.
 */
function handleClaudeEvent(event: ClaudeSDKEvent, callbacks: ClaudeAgentCallbacks) {
  switch (event.type) {
    case 'text-delta': {
      const text = (event.delta as string) || '';
      if (text) callbacks.onTextDelta(text);
      break;
    }
    case 'thinking-delta': {
      const text = (event.delta as string) || '';
      if (text) callbacks.onThinkingDelta(text);
      break;
    }
    case 'thinking-done': {
      callbacks.onThinkingDone();
      break;
    }
    case 'tool-call': {
      const toolName = (event.toolName as string) || 'unknown';
      const input = (event.input as Record<string, unknown>) || {};
      callbacks.onToolCall(toolName, input);
      break;
    }
    case 'tool-result': {
      const toolCallId = (event.toolCallId as string) || '';
      const output = event.output ?? event.result;
      const result = typeof output === 'string'
        ? output
        : JSON.stringify(output);
      callbacks.onToolResult(toolCallId, result);
      break;
    }
    case 'error': {
      callbacks.onError(String(event.error || 'Unknown error'));
      break;
    }
    // stream_event, result, tool_progress, tool_use_summary — ignore silently
  }
}
