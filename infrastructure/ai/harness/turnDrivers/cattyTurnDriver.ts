import type { ModelMessage } from 'ai';
import type { OpenAIChatAssistantFields } from '../../providerContinuation';
import {
  DEFAULT_CONTEXT_WINDOW_TOKENS,
  estimateUnknownTokens,
  resolveContextWindow,
} from '../../contextCompaction';
import { buildSystemPrompt } from '../../cattyAgent/systemPrompt';
import { isWebSearchReady, normalizeCommandTimeoutSeconds } from '../../types';
import { createModelFromConfig } from '../../sdk/providers';
import { createCattyToolsFromCatalog } from '../capabilityTools';
import { createInitialCattyRuntimeContext } from '../cattyRuntimeContext';
import { prepareStepContext, extractLatestUserGoal } from '../contextManager';
import {
  compactCattyMessages,
  prepareCattyMessagesForStream,
} from '../cattyRuntime';
import { DEFAULT_MAX_OUTPUT_TOKENS } from '../contextBudget';
import { clearChatSessionCancelled } from '../agentStop';
import { isRequestTooLargeError } from '../../errorClassifier';
import { getNetcattyBridge, generateId, resolveUserSkillsContext } from '../../../../components/ai/hooks/aiChatStreamingSupport';
import {
  buildCattySdkMessages,
  collectOpenAIChatAssistantFieldsForMessages,
  collectToolResultsAfterMessage,
  createContinuationContext,
} from './cattyMessageBuilder';
import { hadToolProgressBeforeRequestTooLarge, processCattyStream } from './cattyStreamProcessor';
import type { CattyTurnInput, TurnDriver, TurnDriverContext } from './types';
import { fitLargeUserInputForModel } from '../largeUserInput';
import { buildPromptContextSnapshot } from '../promptContextSnapshot';
import type { SessionStateStore } from '../sessionState';

export async function refreshRememberedTerminalJobs(input: {
  chatSessionId: string;
  sessionStateStore: SessionStateStore;
  poll: (jobId: string, offset: number) => Promise<unknown>;
}): Promise<void> {
  const rememberedJobs = Object.entries(
    input.sessionStateStore.get(input.chatSessionId).activeJobs,
  ).slice(-5);
  for (const [jobId, job] of rememberedJobs) {
    try {
      const refreshed = await input.poll(jobId, job.nextOffset);
      const refreshedRecord = refreshed && typeof refreshed === 'object'
        ? refreshed as Record<string, unknown>
        : undefined;
      const isError = Boolean(
        refreshedRecord
        && (
          refreshedRecord.ok === false
          || (typeof refreshedRecord.error === 'string' && refreshedRecord.error.trim().length > 0)
        ),
      );
      input.sessionStateStore.updateFromToolResult(
        input.chatSessionId,
        'terminal_poll',
        { jobId, offset: job.nextOffset },
        JSON.stringify(
          !isError && refreshed && typeof refreshed === 'object'
            ? { ...refreshed as Record<string, unknown>, nextOffset: job.nextOffset }
            : refreshed,
        ),
        isError,
      );
    } catch {
      // Preserve the unverified job on transient bridge errors.
    }
  }
}

export class CattyTurnDriver implements TurnDriver {
  readonly backend = 'catty' as const;

  async run(input: import('./types').TurnInput, ctx: TurnDriverContext): Promise<void> {
    if (input.backend !== 'catty') {
      throw new Error('CattyTurnDriver received non-catty input');
    }
    await runCattyTurn(input, ctx);
  }

  abort(): void {
    // Abort is handled via AbortSignal on the turn input.
  }
}

async function runCattyTurn(input: CattyTurnInput, ctx: TurnDriverContext): Promise<void> {
  const {
    chatSessionId: sessionId,
    userText: trimmed,
    signal,
    currentSession,
    assistantMsgId,
    context,
    attachments,
    maxIterations,
    bridge,
    ui,
  } = input;

  const netcattyBridge = (bridge ?? getNetcattyBridge()) as NonNullable<ReturnType<typeof getNetcattyBridge>>;
  const toolOutputTempBridge = netcattyBridge as typeof netcattyBridge & {
    writeToolOutputTemp?: (handleId: string, content: string) => Promise<{ ok: boolean; path?: string; error?: string }>;
    readToolOutputTemp?: (
      path: string,
      request: import('../toolOutputStore').ReadToolOutputInput,
    ) => Promise<Omit<import('../toolOutputStore').ToolOutputReadResult, 'handleId' | 'storedChars' | 'sourceTruncated'> | null>;
    deleteToolOutputTemp?: (path: string) => Promise<{ ok: boolean }>;
  };
  if (
    toolOutputTempBridge.writeToolOutputTemp
    && toolOutputTempBridge.readToolOutputTemp
    && toolOutputTempBridge.deleteToolOutputTemp
  ) {
    ctx.toolOutputStore.setPersistence({
      write: async (handleId, content) => {
        const result = await toolOutputTempBridge.writeToolOutputTemp!(handleId, content);
        if (!result.ok || !result.path) {
          throw new Error(result.error || 'Unable to persist tool output.');
        }
        return result.path;
      },
      read: (path, request) => toolOutputTempBridge.readToolOutputTemp!(path, request),
      delete: async path => {
        await toolOutputTempBridge.deleteToolOutputTemp!(path);
      },
    });
  }
  await clearChatSessionCancelled(sessionId, netcattyBridge);
  if (netcattyBridge.aiMcpUpdateSessions) {
    await netcattyBridge.aiMcpUpdateSessions(context.terminalSessions, sessionId);
  }
  if (attachments?.length && netcattyBridge.aiMcpUpdateAttachments) {
    await netcattyBridge.aiMcpUpdateAttachments(attachments, sessionId);
  }
  const userSkillsContext = await resolveUserSkillsContext(
    netcattyBridge,
    trimmed,
    context.selectedUserSkillSlugs,
  );
  const modelUserText = fitLargeUserInputForModel(trimmed, sessionId, ctx.toolOutputStore);
  const getExecutorContext = context.getExecutorContext ?? (() => ({
    sessions: context.terminalSessions,
    workspaceId: context.scopeType === 'workspace' ? context.scopeTargetId : undefined,
    workspaceName: context.scopeType === 'workspace' ? context.scopeLabel : undefined,
  }));
  const toolsBundle = createCattyToolsFromCatalog(
    netcattyBridge,
    getExecutorContext,
    context.commandBlocklist,
    context.globalPermissionMode,
    context.webSearchConfig ?? undefined,
    sessionId,
    ctx.toolOutputStore,
    ctx.toolResultDedup,
  );
  const { tools } = toolsBundle;

  const systemPrompt = buildSystemPrompt({
    scopeType: context.scopeType,
    scopeLabel: context.scopeLabel,
    hosts: context.terminalSessions,
    permissionMode: context.globalPermissionMode,
    webSearchEnabled: isWebSearchReady(context.webSearchConfig),
    userSkillsContext,
  });

  if (!context.activeProvider) {
    ui.reportStreamError(sessionId, signal, 'No AI provider configured. Please configure a provider in Settings → AI.');
    return;
  }

  const activeModelId = context.activeModelId || context.activeProvider.defaultModel || '';
  const promptContext = buildPromptContextSnapshot({
    providerId: context.activeProvider.providerId,
    modelId: activeModelId,
    permissionMode: context.permissionMode ?? context.globalPermissionMode,
    scopeType: context.scopeType,
    scopeLabel: context.scopeLabel,
    toolNames: Object.keys(tools),
    selectedSkillSlugs: context.selectedUserSkillSlugs,
    systemPrompt,
    webSearchEnabled: isWebSearchReady(context.webSearchConfig),
    hostSessionIds: context.terminalSessions.map(session => session.sessionId),
  });
  ctx.emit({
    id: `context-snapshot-${ctx.turnId}`,
    type: 'context_snapshot',
    snapshot: promptContext,
  } as import('../types').AgentEvent);
  const continuationContext = createContinuationContext(
    context.activeProvider.id,
    context.activeProvider.providerId,
    activeModelId,
  );

  ui.setStreamingForScope(sessionId, true);

  try {
    const openAIChatAssistantFieldsByMessage = new Map<ModelMessage, OpenAIChatAssistantFields | undefined>();

    const buildSdkMessages = (
      allMessages: import('../../types').ChatMessage[],
      includeCurrentUserMessage: boolean,
      options: { preserveTerminalToolResults?: ReadonlySet<import('../../types').ToolResult> } = {},
    ) => buildCattySdkMessages({
      allMessages,
      includeCurrentUserMessage,
      trimmed: modelUserText,
      attachments: includeCurrentUserMessage ? attachments : undefined,
      continuationContext,
      preserveTerminalToolResults: options.preserveTerminalToolResults,
      fieldsByMessage: openAIChatAssistantFieldsByMessage,
    });

    let model;
    try {
      model = createModelFromConfig(
        {
          ...context.activeProvider,
          defaultModel: activeModelId,
        },
        {
          getOpenAIChatAssistantFields: () => continuationContext.openAIChatAssistantFields,
        },
      );
    } catch (e) {
      console.error('[Catty] Model creation failed:', e);
      ui.reportStreamError(sessionId, signal, `Model creation failed: ${e instanceof Error ? e.message : String(e)}`);
      return;
    }

    const contextWindow = resolveContextWindow({
      provider: context.activeProvider,
      modelId: activeModelId,
      defaultContextWindow: DEFAULT_CONTEXT_WINDOW_TOKENS,
    });
    const maxOutputTokens = context.activeProvider.advancedParams?.maxTokens ?? DEFAULT_MAX_OUTPUT_TOKENS;
    const providerId = context.activeProvider.providerId;
    const outputReserveTokens = Math.min(maxOutputTokens, Math.ceil(contextWindow * 0.05));
    const getRequestReserveTokens = () => outputReserveTokens + estimateUnknownTokens({
      systemPrompt,
      toolNames: Object.keys(tools),
      openAIChatAssistantFields: Array.from(openAIChatAssistantFieldsByMessage.values()),
    }, providerId);

    const prepareMessagesForStream = (messages: ModelMessage[]): ModelMessage[] => {
      const pruned = prepareCattyMessagesForStream(messages);
      continuationContext.openAIChatAssistantFields = collectOpenAIChatAssistantFieldsForMessages(
        pruned,
        openAIChatAssistantFieldsByMessage,
      );
      return pruned;
    };

    const compactMessages = async (
      messages: ModelMessage[],
      options: {
        force?: boolean;
        compressForRequestTooLargeRetry?: boolean;
      },
    ): Promise<ModelMessage[]> => {
      if (netcattyBridge.aiCapability) {
        await refreshRememberedTerminalJobs({
          chatSessionId: sessionId,
          sessionStateStore: ctx.sessionStateStore,
          poll: (jobId, offset) => netcattyBridge.aiCapability!(
              'netcatty/jobPoll',
              { jobId, offset },
              sessionId,
          ),
        });
      }
      const pendingHandles = ctx.toolOutputStore.listPendingHandles(sessionId);
      const sessionStateText = ctx.sessionStateStore.toReinjectionText(sessionId);
      const result = await compactCattyMessages({
        messages,
        sessionId,
        chatSessionId: sessionId,
        provider: context.activeProvider,
        modelId: activeModelId || context.activeProvider?.defaultModel,
        reservedTokens: getRequestReserveTokens,
        maxOutputTokens,
        model,
        toolOutputStore: ctx.toolOutputStore,
        abortSignal: signal,
        trigger: options.force ? 'force' : options.compressForRequestTooLargeRetry ? '413-retry' : 'pre-turn',
        force: options.force,
        compressForRequestTooLargeRetry: options.compressForRequestTooLargeRetry,
        onCompactionStart: (trigger) => {
          ctx.emit({
            id: `compaction-start-${Date.now()}`,
            type: 'compaction_start',
            sessionId,
            chatSessionId: sessionId,
            backend: 'catty',
            timestamp: Date.now(),
            trigger,
          } as import('../types').AgentEvent);
        },
        onCompaction: (trace) => {
          ctx.emit({
            id: `compaction-${Date.now()}`,
            type: 'compaction',
            trace,
          } as import('../types').AgentEvent);
          if (options.compressForRequestTooLargeRetry && trace.did413Fallback) {
            console.warn('[Catty] Request content compressed after forced context compaction.');
          }
        },
        reinjection: {
          permissionMode: context.permissionMode ?? context.globalPermissionMode,
          sessionStateText,
          sessionScopeSummary: pendingHandles.length
            ? `Pending tool output handles: ${pendingHandles.map(h => h.id).join(', ')}`
            : undefined,
        },
      });
      return result.messages;
    };

    let messagesForStream = buildSdkMessages(currentSession?.messages ?? [], true);
    messagesForStream = await compactMessages(messagesForStream, {});
    messagesForStream = prepareMessagesForStream(messagesForStream);

    const runtimeContext = createInitialCattyRuntimeContext({
      chatSessionId: sessionId,
      turnId: ctx.turnId,
      providerId: context.activeProvider?.providerId,
      modelId: activeModelId,
      permissionMode: context.permissionMode ?? context.globalPermissionMode,
      scopeType: context.scopeType,
      scopeLabel: context.scopeLabel,
      userGoal: extractLatestUserGoal(messagesForStream),
      promptContext,
    });
    const commandTimeoutSeconds =
      Number.isFinite(context.commandTimeout) && context.commandTimeout > 0
        ? normalizeCommandTimeoutSeconds(context.commandTimeout)
        : undefined;
    const commandTimeoutMs =
      commandTimeoutSeconds != null
        ? commandTimeoutSeconds * 1000
        : undefined;

    const runStream = async (streamMessages: ModelMessage[], streamAssistantMsgId: string) => {
      await processCattyStream({
        streamSessionId: sessionId,
        model,
        systemPrompt,
        toolsBundle,
        sdkMessages: streamMessages,
        signal,
        currentAssistantMsgId: streamAssistantMsgId,
        maxIterations,
        advancedParams: context.activeProvider?.advancedParams,
        continuationContext,
        turnId: ctx.turnId,
        commandTimeoutMs,
        runtimeContext,
        onAgentEvent: (event) => ctx.emit(event),
        prepareStep: async ({ stepNumber, messages, runtimeContext: stepRuntimeContext }) => {
          const prepared = await prepareStepContext({
            messages,
            stepNumber,
            sessionId,
            chatSessionId: sessionId,
            providerId: context.activeProvider?.providerId,
            modelId: activeModelId,
            contextWindow,
            reservedTokens: getRequestReserveTokens(),
            maxOutputTokens,
            toolOutputStore: ctx.toolOutputStore,
            runtimeContext: stepRuntimeContext,
            onEvent: (event) => ctx.emit(event),
          });
          return {
            messages: prepared.messages,
            runtimeContext: prepared.runtimeContext,
          };
        },
        ui: {
          addMessageToSession: ui.addMessageToSession,
          updateMessageById: ui.updateMessageById,
        },
      });
    };

    try {
      await runStream(messagesForStream, assistantMsgId);
    } catch (streamErr) {
      if (signal.aborted || !isRequestTooLargeError(streamErr)) {
        throw streamErr;
      }

      console.warn('[Catty] Request hit HTTP 413; forcing context compaction and retrying once.', streamErr);
      ctx.toolResultDedup.enableWriteReplay();
      const hadToolProgress = hadToolProgressBeforeRequestTooLarge(streamErr);
      let retryBaseMessages = messagesForStream;
      let retryAssistantMsgId = assistantMsgId;
      if (hadToolProgress) {
        const latestSession = ui.getLatestSession?.(sessionId);
        if (latestSession) {
          retryBaseMessages = buildSdkMessages(latestSession.messages, false, {
            preserveTerminalToolResults: collectToolResultsAfterMessage(
              latestSession.messages,
              assistantMsgId,
            ),
          });
        }
        retryAssistantMsgId = generateId();
        ui.addMessageToSession(sessionId, {
          id: retryAssistantMsgId,
          role: 'assistant',
          content: '',
          timestamp: Date.now(),
          model: activeModelId || context.activeProvider?.defaultModel || '',
          providerId: context.activeProvider?.providerId,
        });
      } else {
        ui.updateMessageById(sessionId, assistantMsgId, msg => ({
          ...msg,
          content: '',
          thinking: undefined,
          thinkingDurationMs: undefined,
          providerContinuation: undefined,
          toolCalls: undefined,
          errorInfo: undefined,
          executionStatus: undefined,
          pendingApproval: undefined,
        }));
      }
      const retryMessages = prepareMessagesForStream(await compactMessages(retryBaseMessages, {
        force: true,
        compressForRequestTooLargeRetry: true,
      }));
      await runStream(retryMessages, retryAssistantMsgId);
    }
  } catch (err) {
    console.error('[Catty] streamText error:', err);
    ui.reportStreamError(sessionId, signal, err);
  } finally {
    ui.updateLastMessage(sessionId, msg => msg.statusText ? { ...msg, statusText: '' } : msg);
    ui.setStreamingForScope(sessionId, false);
    context.autoTitleSession(sessionId, context.titleText ?? trimmed);
  }
}

export const cattyTurnDriver = new CattyTurnDriver();
