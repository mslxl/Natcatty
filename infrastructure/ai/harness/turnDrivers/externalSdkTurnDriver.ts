import { getExternalAgentSdkBackend } from '../../managedAgents';
import { runSdkAgentTurn, type SdkAgentCallbacks } from '../../sdkAgentAdapter';
import { getNetcattyBridge, generateId, resolveUserSkillsContext, isToolResultError } from '../../../../components/ai/hooks/aiChatStreamingSupport';
import type { AgentActivity, AgentUsage } from '../../types';
import type { ExternalTurnInput, TurnDriver, TurnDriverContext } from './types';
import { resolveEstimatedUsageFallback, upsertAgentActivity } from './externalSdkEventState';

export class ExternalSdkTurnDriver implements TurnDriver {
  readonly backend = 'external-sdk' as const;

  async run(input: import('./types').TurnInput, ctx: TurnDriverContext): Promise<void> {
    if (input.backend !== 'external-sdk') {
      throw new Error('ExternalSdkTurnDriver received non-external input');
    }
    await runExternalTurn(input, ctx);
  }

  abort(): void {
    // Abort is handled via AbortSignal on the turn input.
  }
}

async function runExternalTurn(input: ExternalTurnInput, ctx: TurnDriverContext): Promise<void> {
  const {
    chatSessionId: sessionId,
    assistantMsgId,
    userText: trimmed,
    signal,
    agentConfig,
    attachedImages,
    context,
    bridge,
    ui,
  } = input;

  const netcattyBridge = bridge ?? getNetcattyBridge();
  const sdkBackend = getExternalAgentSdkBackend(agentConfig);

  if (!sdkBackend || !netcattyBridge) {
    ui.reportStreamError(
      sessionId,
      signal,
      'This agent has no SDK backend configured. Re-discover it in Settings -> AI.',
    );
    ui.setStreamingForScope(sessionId, false);
    return;
  }

  const userSkillsContext = await resolveUserSkillsContext(
    netcattyBridge,
    trimmed,
    context.selectedUserSkillSlugs,
  );

  const requestId = ctx.turnId;
  ui.setStreamingForScope(sessionId, true);

  if (netcattyBridge.aiMcpUpdateSessions) {
    await netcattyBridge.aiMcpUpdateSessions(context.terminalSessions, sessionId);
  }

  let needsNewAssistantMsg = false;
  let activeAssistantMessageId = assistantMsgId;
  const maybeCreateAssistantMsg = () => {
    if (needsNewAssistantMsg) {
      needsNewAssistantMsg = false;
      activeAssistantMessageId = generateId();
      ui.addMessageToSession(sessionId, {
        id: activeAssistantMessageId,
        role: 'assistant',
        content: '',
        timestamp: Date.now(),
        model: agentConfig.name || 'external',
      });
    }
  };

  const toolNamesByCallId = new Map<string, string>();
  const activityMessageIds = new Map<string, string>();
  let actualUsageReported = false;
  const updateActivity = (activity: AgentActivity) => {
    const activityMessageId = activityMessageIds.get(activity.id);
    if (activityMessageId) {
      ui.updateMessageById(sessionId, activityMessageId, msg => ({
        ...msg,
        agentActivities: upsertAgentActivity(msg.agentActivities, activity),
        statusText: undefined,
      }));
      return;
    }

    maybeCreateAssistantMsg();
    if (activeAssistantMessageId) {
      activityMessageIds.set(activity.id, activeAssistantMessageId);
      ui.updateMessageById(sessionId, activeAssistantMessageId, msg => ({
        ...msg,
        agentActivities: upsertAgentActivity(msg.agentActivities, activity),
        statusText: undefined,
      }));
      return;
    }

    ui.updateLastMessage(sessionId, msg => ({
      ...msg,
      agentActivities: upsertAgentActivity(msg.agentActivities, activity),
      statusText: undefined,
    }));
  };
  const updateUsage = (usage: AgentUsage) => {
    maybeCreateAssistantMsg();
    ui.updateLastMessage(sessionId, msg => ({ ...msg, usage }));
  };
  const callbacks: SdkAgentCallbacks = {
    onTextDelta: (text: string) => {
      maybeCreateAssistantMsg();
      ui.updateLastMessage(sessionId, msg => ({
        ...msg,
        content: msg.content + text,
        statusText: undefined,
        thinkingDurationMs: msg.thinking && !msg.thinkingDurationMs
          ? Date.now() - msg.timestamp : msg.thinkingDurationMs,
      }));
    },
    onThinkingDelta: (text: string) => {
      maybeCreateAssistantMsg();
      ui.updateLastMessage(sessionId, msg => ({
        ...msg,
        thinking: (msg.thinking || '') + text,
      }));
    },
    onThinkingDone: () => {
      ui.updateLastMessage(sessionId, msg => ({
        ...msg,
        thinkingDurationMs: msg.thinkingDurationMs || (Date.now() - msg.timestamp),
      }));
    },
    onToolCall: (toolName: string, args: Record<string, unknown>, toolCallId?: string) => {
      maybeCreateAssistantMsg();
      const id = toolCallId || `tc_${Date.now()}`;
      toolNamesByCallId.set(id, toolName);
      ui.updateLastMessage(sessionId, msg => ({
        ...msg,
        toolCalls: [...(msg.toolCalls || []), { id, name: toolName, arguments: args }],
        executionStatus: 'running',
        statusText: undefined,
      }));
    },
    onToolResult: (toolCallId: string, result: string, toolName?: string) => {
      const effectiveToolName = toolName ?? toolNamesByCallId.get(toolCallId);
      ui.updateLastMessage(sessionId, msg => {
        if (msg.role !== 'assistant' || msg.executionStatus !== 'running') return msg;
        const updatedToolCalls = effectiveToolName && !effectiveToolName.includes('sdk_agent_dynamic_tool') && msg.toolCalls
          ? msg.toolCalls.map(tc => tc.id === toolCallId && !tc.name ? { ...tc, name: effectiveToolName } : tc)
          : msg.toolCalls;
        return { ...msg, toolCalls: updatedToolCalls, executionStatus: 'completed', statusText: undefined };
      });
      ui.addMessageToSession(sessionId, {
        id: generateId(),
        role: 'tool',
        content: '',
        toolResults: [{
          toolCallId,
          toolName: effectiveToolName,
          content: result,
          isError: isToolResultError(result),
        }],
        timestamp: Date.now(),
        executionStatus: 'completed',
      });
      needsNewAssistantMsg = true;
    },
    onFileChange: updateActivity,
    onWebSearch: updateActivity,
    onPlanUpdate: updateActivity,
    onWarning: updateActivity,
    onUsage: (usage: AgentUsage) => {
      actualUsageReported = true;
      updateUsage(usage);
    },
    onStatus: (message: string) => {
      maybeCreateAssistantMsg();
      ui.updateLastMessage(sessionId, msg => ({ ...msg, statusText: message }));
    },
    onSessionId: (externalSessionId: string) => {
      context.updateExternalSessionId?.(sessionId, externalSessionId);
    },
    onError: (error: string) => {
      ui.reportStreamError(sessionId, signal, error);
      ui.setStreamingForScope(sessionId, false);
    },
    onDone: () => {},
  };

  try {
    await runSdkAgentTurn(
      netcattyBridge,
      requestId,
      sessionId,
      agentConfig,
      trimmed,
      callbacks,
      signal,
      undefined,
      context.selectedAgentModel,
      context.existingSessionId,
      context.historyMessages,
      attachedImages.length > 0 ? attachedImages : undefined,
      context.toolIntegrationMode,
      context.defaultTargetSession,
      userSkillsContext,
      context.permissionMode,
      {
        traceSink: (event) => ctx.emit(event),
        skipHarnessTrace: true,
      },
    );

    const estimatedUsage = resolveEstimatedUsageFallback(trimmed, actualUsageReported);
    if (estimatedUsage) {
      updateUsage(estimatedUsage);
      ctx.emit({
        id: `usage-${ctx.turnId}`,
        type: 'usage',
        promptTokens: estimatedUsage.inputTokens,
        completionTokens: estimatedUsage.outputTokens,
        totalTokens: estimatedUsage.totalTokens,
        estimated: true,
      } as import('../types').AgentEvent);
    }
  } finally {
    ui.setStreamingForScope(sessionId, false);
  }
}

export const externalSdkTurnDriver = new ExternalSdkTurnDriver();
