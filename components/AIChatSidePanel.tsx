/**
 * AIChatSidePanel - Main AI chat interface side panel
 *
 * Zed-style agent panel with agent selector, scoped chat sessions,
 * message list, input area, and session history drawer.
 */

import {
  History,
  Plus,
  Trash2,
  X,
} from 'lucide-react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { streamText, stepCountIs, type ModelMessage } from 'ai';
import { cn } from '../lib/utils';
import { useI18n } from '../application/i18n/I18nProvider';
import { useWindowControls } from '../application/state/useWindowControls';
import { useImageUpload } from '../application/state/useImageUpload';
import type {
  AIPermissionMode,
  AISession,
  AISessionScope,
  ChatMessage,
  DiscoveredAgent,
  ExternalAgentConfig,
  ProviderConfig,
} from '../infrastructure/ai/types';
import { getAgentModelPresets } from '../infrastructure/ai/types';
import { buildSystemPrompt } from '../infrastructure/ai/cattyAgent/systemPrompt';
import { createModelFromConfig } from '../infrastructure/ai/sdk/providers';
import { createCattyTools } from '../infrastructure/ai/sdk/tools';
import type { NetcattyBridge } from '../infrastructure/ai/cattyAgent/executor';
import { exportAsMarkdown, exportAsJSON, exportAsPlainText, getExportFilename } from '../infrastructure/ai/conversationExport';
import { runExternalAgentTurn } from '../infrastructure/ai/externalAgentAdapter';
import { runAcpAgentTurn } from '../infrastructure/ai/acpAgentAdapter';
import { classifyError } from '../infrastructure/ai/errorClassifier';
import { useAgentDiscovery } from '../application/state/useAgentDiscovery';
import { Button } from './ui/button';
import { ScrollArea } from './ui/scroll-area';
import AgentSelector from './ai/AgentSelector';
import ChatInput from './ai/ChatInput';
import ChatMessageList from './ai/ChatMessageList';
import ConversationExport from './ai/ConversationExport';

// -------------------------------------------------------------------
// Stream chunk type interfaces (Issue #13: replace unsafe casts)
// -------------------------------------------------------------------

/** Shape of a text/text-delta chunk from the Vercel AI SDK fullStream. */
interface TextDeltaChunk {
  type: 'text' | 'text-delta';
  text?: string;
  textDelta?: string;
}

/** Shape of a reasoning chunk from the Vercel AI SDK fullStream. */
interface ReasoningChunk {
  type: 'reasoning' | 'reasoning-start' | 'reasoning-delta';
  text?: string;
}

/** Shape of a tool-call chunk from the Vercel AI SDK fullStream. */
interface ToolCallChunk {
  type: 'tool-call';
  toolCallId: string;
  toolName: string;
  input?: unknown;
  args?: unknown;
}

/** Shape of a tool-result chunk from the Vercel AI SDK fullStream. */
interface ToolResultChunk {
  type: 'tool-result';
  toolCallId: string;
  output?: unknown;
  result?: unknown;
}

/** Shape of a tool-approval-request chunk from the Vercel AI SDK fullStream. */
interface ToolApprovalRequestChunk {
  type: 'tool-approval-request';
  approvalId: string;
  toolCall: {
    toolCallId: string;
    toolName: string;
    args?: Record<string, unknown>;
    input?: Record<string, unknown>;
  };
}

/** Shape of an error chunk from the Vercel AI SDK fullStream. */
interface ErrorChunk {
  type: 'error';
  error: unknown;
}

/** Union of all stream chunk shapes we handle. */
type StreamChunk =
  | TextDeltaChunk
  | ReasoningChunk
  | ToolCallChunk
  | ToolResultChunk
  | ToolApprovalRequestChunk
  | ErrorChunk
  | { type: 'reasoning-end' | 'text-start' | 'text-end' | 'start' | 'finish' | 'start-step' | 'finish-step' };

/** Shape of the netcatty bridge exposed on `window` (panel-specific subset). */
interface PanelBridge extends NetcattyBridge {
  credentialsDecrypt?: (value: string) => Promise<string>;
  aiMcpUpdateSessions?: (sessions: AIChatSidePanelProps['terminalSessions'], chatSessionId?: string) => Promise<unknown>;
  aiAcpCleanup?: (chatSessionId: string) => Promise<{ ok: boolean }>;
  [key: string]: ((...args: unknown[]) => unknown) | undefined;
}

/** Typed accessor for the netcatty bridge on the window object. */
function getNetcattyBridge(): PanelBridge | undefined {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (window as any).netcatty as PanelBridge | undefined;
}

// -------------------------------------------------------------------
// Props
// -------------------------------------------------------------------

interface AIChatSidePanelProps {
  // Session state (per-scope)
  sessions: AISession[];
  activeSessionIdMap: Record<string, string | null>;
  setActiveSessionId: (scopeKey: string, id: string | null) => void;
  createSession: (scope: AISessionScope, agentId?: string) => AISession;
  deleteSession: (sessionId: string, scopeKey?: string) => void;
  updateSessionTitle: (sessionId: string, title: string) => void;
  addMessageToSession: (sessionId: string, message: ChatMessage) => void;
  updateLastMessage: (
    sessionId: string,
    updater: (msg: ChatMessage) => ChatMessage,
  ) => void;
  updateMessageById: (
    sessionId: string,
    messageId: string,
    updater: (msg: ChatMessage) => ChatMessage,
  ) => void;
  // Provider config
  providers: ProviderConfig[];
  activeProviderId: string;
  activeModelId: string;

  // Agent info
  defaultAgentId: string;
  externalAgents: ExternalAgentConfig[];
  setExternalAgents?: (value: ExternalAgentConfig[] | ((prev: ExternalAgentConfig[]) => ExternalAgentConfig[])) => void;
  agentModelMap: Record<string, string>;
  setAgentModel: (agentId: string, modelId: string) => void;

  // Safety
  globalPermissionMode: AIPermissionMode;
  setGlobalPermissionMode?: (mode: AIPermissionMode) => void;
  commandBlocklist?: string[];
  maxIterations?: number;

  // Context
  scopeType: 'terminal' | 'workspace';
  scopeTargetId?: string;
  scopeHostIds?: string[];
  scopeLabel?: string;

  // Terminal session context (from parent)
  terminalSessions?: Array<{
    sessionId: string;
    hostId: string;
    hostname: string;
    label: string;
    os?: string;
    username?: string;
    connected: boolean;
  }>;

  // Visibility
  isVisible?: boolean;
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

function generateId(): string {
  return `msg-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

// -------------------------------------------------------------------
// Component
// -------------------------------------------------------------------

const AIChatSidePanelInner: React.FC<AIChatSidePanelProps> = ({
  sessions,
  activeSessionIdMap,
  setActiveSessionId: setActiveSessionIdForScope,
  createSession,
  deleteSession,
  updateSessionTitle,
  addMessageToSession,
  updateLastMessage,
  updateMessageById,
  providers,
  activeProviderId,
  activeModelId,
  defaultAgentId,
  externalAgents,
  setExternalAgents,
  agentModelMap,
  setAgentModel,
  globalPermissionMode,
  setGlobalPermissionMode,
  commandBlocklist,
  maxIterations = 20,
  scopeType,
  scopeTargetId,
  scopeHostIds,
  scopeLabel,
  terminalSessions = [],
  isVisible = true,
}) => {
  const { t } = useI18n();
  // ── Per-scope state ──
  // Derive scope key for per-scope isolation
  const scopeKey = `${scopeType}:${scopeTargetId ?? ''}`;

  // Per-scope input values
  const [inputValueMap, setInputValueMap] = useState<Record<string, string>>({});
  const inputValue = inputValueMap[scopeKey] ?? '';
  const setInputValue = useCallback((val: string) => {
    setInputValueMap(prev => ({ ...prev, [scopeKey]: val }));
  }, [scopeKey]);

  // Per-session streaming state (keyed by sessionId)
  const [streamingSessions, setStreamingSessions] = useState<Set<string>>(new Set());
  const setStreamingForScope = useCallback((key: string, val: boolean) => {
    setStreamingSessions(prev => {
      const next = new Set(prev);
      if (val) next.add(key); else next.delete(key);
      return next;
    });
  }, []);

  const [showHistory, setShowHistory] = useState(false);
  const [currentAgentId, setCurrentAgentId] = useState(defaultAgentId);

  // Per-scope abort controllers
  const abortControllersRef = useRef<Map<string, AbortController>>(new Map());

  // Pending approval context — stores SDK state needed to resume after user approves/rejects
  const pendingApprovalContextRef = useRef<{
    sessionId: string;
    scopeKey: string;
    sdkMessages: Array<ModelMessage>;
    approvalInfo: {
      approvalId: string;
      toolCallId: string;
      toolName: string;
      toolArgs: Record<string, unknown>;
    };
    model: ReturnType<typeof createModelFromConfig>;
    systemPrompt: string;
    tools: ReturnType<typeof createCattyTools>;
  } | null>(null);

  // Timeout ID for auto-clearing stale pending approval (Issue #14)
  const pendingApprovalTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  /** Set pending approval context with a 5-minute auto-clear timeout. */
  const setPendingApproval = useCallback((ctx: typeof pendingApprovalContextRef.current) => {
    // Clear any existing timeout
    if (pendingApprovalTimeoutRef.current) {
      clearTimeout(pendingApprovalTimeoutRef.current);
      pendingApprovalTimeoutRef.current = null;
    }
    pendingApprovalContextRef.current = ctx;
    if (ctx) {
      pendingApprovalTimeoutRef.current = setTimeout(() => {
        // Auto-clear after 5 minutes if user never responds
        if (pendingApprovalContextRef.current?.sessionId === ctx.sessionId) {
          pendingApprovalContextRef.current = null;
          setStreamingForScope(ctx.sessionId, false);
          abortControllersRef.current.get(ctx.sessionId)?.abort();
          abortControllersRef.current.delete(ctx.sessionId);
        }
        pendingApprovalTimeoutRef.current = null;
      }, 5 * 60 * 1000); // 5 minutes
    }
  }, [setStreamingForScope]);

  // Ref to track active object URLs for cleanup on unmount (Issue #19)
  const activeObjectUrlsRef = useRef<Set<string>>(new Set());

  const { images, addImages, removeImage, clearImages } = useImageUpload();
  const { openSettingsWindow } = useWindowControls();

  // Per-scope active session ID
  const activeSessionId = activeSessionIdMap[scopeKey] ?? null;
  const isStreaming = activeSessionId ? streamingSessions.has(activeSessionId) : false;
  const setActiveSessionId = useCallback((id: string | null) => {
    setActiveSessionIdForScope(scopeKey, id);
  }, [scopeKey, setActiveSessionIdForScope]);

  // Restore agent selector from active session when scope changes
  useEffect(() => {
    if (activeSessionId) {
      const session = sessions.find((s) => s.id === activeSessionId);
      if (session) {
        setCurrentAgentId(session.agentId);
      }
    }
  }, [scopeKey, activeSessionId, sessions]);

  // Proactively sync terminal session metadata to main process whenever scope or sessions change
  useEffect(() => {
    const bridge = getNetcattyBridge();
    if (bridge?.aiMcpUpdateSessions && terminalSessions.length > 0) {
      void bridge.aiMcpUpdateSessions(terminalSessions, activeSessionId ?? undefined);
    }
  }, [terminalSessions, scopeKey, activeSessionId]);

  // Abort all active streams and clean up on unmount
  useEffect(() => {
    const controllers = abortControllersRef.current;
    return () => {
      controllers.forEach(c => c.abort());
      controllers.clear();
      // Clear pending approval timeout (Issue #14)
      if (pendingApprovalTimeoutRef.current) {
        clearTimeout(pendingApprovalTimeoutRef.current);
        pendingApprovalTimeoutRef.current = null;
      }
      pendingApprovalContextRef.current = null;
      // Revoke any outstanding object URLs (Issue #19)
      activeObjectUrlsRef.current.forEach(url => URL.revokeObjectURL(url));
      activeObjectUrlsRef.current.clear();
    };
  }, []);

  // Agent discovery
  const {
    discoveredAgents,
    isDiscovering,
    rediscover,
    enableAgent,
  } = useAgentDiscovery(externalAgents, setExternalAgents);

  const handleEnableDiscoveredAgent = useCallback(
    (agent: DiscoveredAgent) => {
      const config = enableAgent(agent);
      setExternalAgents?.((prev) => [...prev, config]);
    },
    [enableAgent, setExternalAgents],
  );

  // Active session (scoped)
  const activeSession = useMemo(
    () => sessions.find((s) => s.id === activeSessionId) ?? null,
    [sessions, activeSessionId],
  );

  const messages = activeSession?.messages ?? [];

  // Active provider info
  const activeProvider = useMemo(
    () => providers.find((p) => p.id === activeProviderId),
    [providers, activeProviderId],
  );

  const providerDisplayName = activeProvider?.name ?? '';
  const modelDisplayName = activeModelId || activeProvider?.defaultModel || '';

  // Agent model presets for the current external agent
  const currentAgentConfig = useMemo(
    () => currentAgentId !== 'catty' ? externalAgents.find(a => a.id === currentAgentId) : undefined,
    [currentAgentId, externalAgents],
  );
  const agentModelPresets = useMemo(
    () => getAgentModelPresets(currentAgentConfig?.command),
    [currentAgentConfig?.command],
  );

  // Per-agent model: recall last selection or use first preset as default
  const selectedAgentModel = useMemo(() => {
    const stored = agentModelMap[currentAgentId];
    if (stored && agentModelPresets.some(p => stored === p.id || stored.startsWith(p.id + '/'))) {
      return stored;
    }
    // Default to first preset; for models with thinking levels, use the default level
    if (agentModelPresets.length > 0) {
      const first = agentModelPresets[0];
      if (first.thinkingLevels?.length) {
        return `${first.id}/${first.thinkingLevels[first.thinkingLevels.length - 1]}`;
      }
      return first.id;
    }
    return undefined;
  }, [currentAgentId, agentModelMap, agentModelPresets]);

  const handleAgentModelSelect = useCallback((modelId: string) => {
    setAgentModel(currentAgentId, modelId);
  }, [currentAgentId, setAgentModel]);

  // Filtered sessions for history (matching current scope type)
  const historySessions = useMemo(
    () =>
      sessions
        .filter((s) => s.scope.type === scopeType && s.scope.targetId === scopeTargetId)
        .sort((a, b) => b.updatedAt - a.updatedAt),
    [sessions, scopeType, scopeTargetId],
  );

  // -------------------------------------------------------------------
  // Shared Catty stream processor
  // -------------------------------------------------------------------
  // Processes a Vercel AI SDK streamText response, dispatching chunks to
  // the chat message list.  Returns approval info when the stream ends
  // with a tool-approval-request, or null if it completed normally.
  const processCattyStream = useCallback(async (
    streamSessionId: string,
    model: ReturnType<typeof createModelFromConfig>,
    systemPrompt: string,
    tools: ReturnType<typeof createCattyTools>,
    sdkMessages: Array<ModelMessage>,
    signal: AbortSignal,
    /** The message ID of the current assistant message to update via updateMessageById. */
    currentAssistantMsgId: string,
  ): Promise<{
    approvalId: string;
    toolCallId: string;
    toolName: string;
    toolArgs: Record<string, unknown>;
  } | null> => {
    const result = streamText({
      model,
      messages: sdkMessages,
      system: systemPrompt,
      tools,
      stopWhen: stepCountIs(maxIterations),
      abortSignal: signal,
    });

    // Track the current assistant message ID so updates target the correct message
    let activeMsgId = currentAssistantMsgId;
    let lastAddedRole: 'assistant' | 'tool' = 'assistant';
    const reader = result.fullStream.getReader();
    let pendingApprovalInfo: {
      approvalId: string;
      toolCallId: string;
      toolName: string;
      toolArgs: Record<string, unknown>;
    } | null = null;

    try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      // Use the StreamChunk union for type narrowing instead of unsafe casts
      const chunk = value as StreamChunk;
      switch (chunk.type) {
        case 'text':
        case 'text-delta': {
          const typedChunk = chunk as TextDeltaChunk;
          const text = typedChunk.text ?? typedChunk.textDelta;
          if (text) {
            if (lastAddedRole === 'tool') {
              const newId = generateId();
              addMessageToSession(streamSessionId, {
                id: newId,
                role: 'assistant',
                content: text,
                timestamp: Date.now(),
              });
              activeMsgId = newId;
              lastAddedRole = 'assistant';
            } else {
              updateMessageById(streamSessionId, activeMsgId, msg => ({
                ...msg,
                content: msg.content + text,
              }));
            }
          }
          break;
        }
        case 'reasoning':
        case 'reasoning-start':
        case 'reasoning-delta': {
          const typedChunk = chunk as ReasoningChunk;
          const rText = typedChunk.text;
          if (rText) {
            if (lastAddedRole === 'tool') {
              const newId = generateId();
              addMessageToSession(streamSessionId, {
                id: newId,
                role: 'assistant',
                content: '',
                thinking: rText,
                timestamp: Date.now(),
              });
              activeMsgId = newId;
              lastAddedRole = 'assistant';
            } else {
              updateMessageById(streamSessionId, activeMsgId, msg => ({
                ...msg,
                thinking: (msg.thinking || '') + rText,
              }));
            }
          }
          break;
        }
        case 'reasoning-end':
        case 'text-start':
        case 'text-end':
        case 'start':
        case 'finish':
        case 'start-step':
        case 'finish-step':
          break;
        case 'tool-call': {
          const typedChunk = chunk as ToolCallChunk;
          updateMessageById(streamSessionId, activeMsgId, msg => ({
            ...msg,
            toolCalls: [...(msg.toolCalls || []), {
              id: typedChunk.toolCallId,
              name: typedChunk.toolName,
              arguments: typedChunk.input ?? typedChunk.args,
            }],
            executionStatus: 'running',
            statusText: undefined,
          }));
          break;
        }
        case 'tool-result': {
          const typedChunk = chunk as ToolResultChunk;
          // Mark the assistant message's tool execution as completed
          updateMessageById(streamSessionId, activeMsgId, msg =>
            msg.role === 'assistant' && msg.executionStatus === 'running'
              ? { ...msg, executionStatus: 'completed', statusText: undefined } : msg,
          );
          const toolOutput = typedChunk.output ?? typedChunk.result;
          addMessageToSession(streamSessionId, {
            id: generateId(),
            role: 'tool',
            content: '',
            toolResults: [{
              toolCallId: typedChunk.toolCallId,
              content: typeof toolOutput === 'string'
                ? toolOutput
                : JSON.stringify(toolOutput),
              isError: false,
            }],
            timestamp: Date.now(),
            executionStatus: 'completed',
          });
          lastAddedRole = 'tool';
          break;
        }
        case 'tool-approval-request': {
          const typedChunk = chunk as ToolApprovalRequestChunk;
          pendingApprovalInfo = {
            approvalId: typedChunk.approvalId,
            toolCallId: typedChunk.toolCall.toolCallId,
            toolName: typedChunk.toolCall.toolName,
            toolArgs: typedChunk.toolCall.args ?? typedChunk.toolCall.input ?? {},
          };
          updateMessageById(streamSessionId, activeMsgId, msg => ({
            ...msg,
            pendingApproval: {
              ...pendingApprovalInfo!,
              status: 'pending' as const,
            },
          }));
          break;
        }
        case 'error': {
          const typedChunk = chunk as ErrorChunk;
          updateMessageById(streamSessionId, activeMsgId, msg => ({
            ...msg,
            executionStatus: msg.executionStatus === 'running' ? 'failed' : msg.executionStatus,
          }));
          addMessageToSession(streamSessionId, {
            id: generateId(),
            role: 'assistant',
            content: '',
            errorInfo: classifyError(String(typedChunk.error)),
            timestamp: Date.now(),
          });
          break;
        }
        default:
          break;
      }
    }
    } finally {
      reader.releaseLock();
    }
    return pendingApprovalInfo;
  }, [maxIterations, addMessageToSession, updateMessageById]);

  // -------------------------------------------------------------------
  // Handlers
  // -------------------------------------------------------------------

  const handleNewChat = useCallback(() => {
    const scope: AISessionScope = {
      type: scopeType,
      targetId: scopeTargetId,
      hostIds: scopeHostIds,
    };
    const session = createSession(scope, currentAgentId);
    setActiveSessionId(session.id);
    setShowHistory(false);
    setInputValue('');
  }, [
    scopeType,
    scopeTargetId,
    scopeHostIds,
    currentAgentId,
    createSession,
    setActiveSessionId,
    setInputValue,
  ]);

  const handleOpenSettings = useCallback(() => {
    void openSettingsWindow();
  }, [openSettingsWindow]);

  // -------------------------------------------------------------------
  // Shared helpers for handleSend sub-flows
  // -------------------------------------------------------------------

  /** Report a streaming error to the chat (shared by all agent paths). */
  const reportStreamError = useCallback((
    sessionId: string,
    abortSignal: AbortSignal,
    err: unknown,
  ) => {
    if (abortSignal.aborted) return;
    const errorStr = err instanceof Error ? err.message : String(err);
    updateLastMessage(sessionId, msg => ({
      ...msg,
      executionStatus: msg.executionStatus === 'running' ? 'failed' : msg.executionStatus,
    }));
    addMessageToSession(sessionId, {
      id: generateId(),
      role: 'assistant',
      content: '',
      errorInfo: classifyError(errorStr),
      timestamp: Date.now(),
    });
  }, [updateLastMessage, addMessageToSession]);

  /** Ref to always access latest sessions (avoids stale closure in autoTitleSession). */
  const sessionsRef = useRef(sessions);
  sessionsRef.current = sessions;

  /** Refs to avoid re-creating handleSend on every keystroke / image change. */
  const inputValueRef = useRef(inputValue);
  inputValueRef.current = inputValue;
  const imagesRef = useRef(images);
  imagesRef.current = images;

  /** Auto-title a session from the first user message if untitled. */
  const autoTitleSession = useCallback((sessionId: string, text: string) => {
    const s = sessionsRef.current.find(x => x.id === sessionId);
    if (s && (!s.title || s.title === 'New Chat')) {
      updateSessionTitle(sessionId, text.length > 50 ? text.slice(0, 50) + '...' : text);
    }
  }, [updateSessionTitle]);

  /** Ensure a session exists for the current scope and return its ID. */
  const ensureSession = useCallback((): string => {
    if (activeSessionId) return activeSessionId;
    const scope: AISessionScope = { type: scopeType, targetId: scopeTargetId, hostIds: scopeHostIds };
    const session = createSession(scope, currentAgentId);
    setActiveSessionId(session.id);
    return session.id;
  }, [activeSessionId, scopeType, scopeTargetId, scopeHostIds, currentAgentId, createSession, setActiveSessionId]);

  /** Get the netcatty bridge from the window. */
  const getBridge = useCallback(() => getNetcattyBridge(), []);

  // -------------------------------------------------------------------
  // External agent sub-flow (ACP or raw process)
  // -------------------------------------------------------------------

  const sendToExternalAgent = useCallback(async (
    sessionId: string,
    trimmed: string,
    agentConfig: ExternalAgentConfig,
    abortController: AbortController,
    attachedImages: Array<{ base64Data: string; mediaType: string; filename?: string }>,
  ) => {
    const bridge = getBridge();

    if (agentConfig.acpCommand && bridge) {
      const requestId = `acp_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;

      // Push terminal session metadata to MCP bridge
      if (bridge?.aiMcpUpdateSessions) {
        await bridge.aiMcpUpdateSessions(terminalSessions, sessionId);
      }

      // TODO(security): agentApiKey is passed as plaintext over IPC to the main process via
      // aiAcpStream. Ideally, pass only the provider ID and let the main process retrieve and
      // decrypt the key itself, avoiding plaintext key transit across the IPC boundary.
      const openaiProvider = providers.find(p => p.providerId === 'openai' && p.enabled && p.apiKey);
      const agentApiKey = openaiProvider?.apiKey;

      // Mutable flag: set after tool-result, cleared when new assistant msg is created
      let needsNewAssistantMsg = false;
      const maybeCreateAssistantMsg = () => {
        if (needsNewAssistantMsg) {
          needsNewAssistantMsg = false;
          addMessageToSession(sessionId, {
            id: generateId(), role: 'assistant', content: '', timestamp: Date.now(),
            model: agentConfig.name || 'external',
          });
        }
      };

      await runAcpAgentTurn(
        bridge,
        requestId,
        sessionId,
        agentConfig,
        trimmed,
        {
          onTextDelta: (text: string) => {
            maybeCreateAssistantMsg();
            updateLastMessage(sessionId, msg => ({
              ...msg,
              content: msg.content + text,
              statusText: undefined,
              thinkingDurationMs: msg.thinking && !msg.thinkingDurationMs
                ? Date.now() - msg.timestamp : msg.thinkingDurationMs,
            }));
          },
          onThinkingDelta: (text: string) => {
            maybeCreateAssistantMsg();
            updateLastMessage(sessionId, msg => ({
              ...msg, thinking: (msg.thinking || '') + text,
            }));
          },
          onThinkingDone: () => {
            updateLastMessage(sessionId, msg => ({
              ...msg, thinkingDurationMs: msg.thinkingDurationMs || (Date.now() - msg.timestamp),
            }));
          },
          onToolCall: (toolName: string, args: Record<string, unknown>) => {
            maybeCreateAssistantMsg();
            updateLastMessage(sessionId, msg => ({
              ...msg,
              toolCalls: [...(msg.toolCalls || []), { id: `tc_${Date.now()}`, name: toolName, arguments: args }],
              executionStatus: 'running',
              statusText: undefined,
            }));
          },
          onToolResult: (toolCallId: string, result: string) => {
            updateLastMessage(sessionId, msg =>
              msg.role === 'assistant' && msg.executionStatus === 'running'
                ? { ...msg, executionStatus: 'completed', statusText: undefined } : msg,
            );
            addMessageToSession(sessionId, {
              id: generateId(), role: 'tool', content: '',
              toolResults: [{ toolCallId, content: result, isError: false }],
              timestamp: Date.now(), executionStatus: 'completed',
            });
            needsNewAssistantMsg = true;
          },
          onStatus: (message: string) => {
            maybeCreateAssistantMsg();
            updateLastMessage(sessionId, msg => ({ ...msg, statusText: message }));
          },
          onError: (error: string) => {
            reportStreamError(sessionId, abortController.signal, error);
            setStreamingForScope(sessionId, false);
          },
          onDone: () => {},
        },
        abortController.signal,
        agentApiKey,
        selectedAgentModel,
        attachedImages.length > 0 ? attachedImages : undefined,
      );
    } else {
      // Fallback: spawn as raw process
      await runExternalAgentTurn(
        agentConfig,
        trimmed,
        {
          onTextDelta: (text: string) => {
            updateLastMessage(sessionId, msg => ({ ...msg, content: msg.content + text }));
          },
          onError: (error: string) => {
            reportStreamError(sessionId, abortController.signal, error);
            setStreamingForScope(sessionId, false);
          },
          onDone: () => {},
        },
        bridge as Parameters<typeof runExternalAgentTurn>[3],
        abortController.signal,
      );
    }
  }, [
    getBridge, terminalSessions, providers, selectedAgentModel,
    addMessageToSession, updateLastMessage, setStreamingForScope, reportStreamError,
  ]);

  // -------------------------------------------------------------------
  // Catty Agent sub-flow (Vercel AI SDK streamText)
  // -------------------------------------------------------------------

  const sendToCattyAgent = useCallback(async (
    sessionId: string,
    sendScopeKey: string,
    trimmed: string,
    abortController: AbortController,
    currentSession: AISession | undefined,
    assistantMsgId: string,
  ) => {
    const bridge = getNetcattyBridge();
    const tools = createCattyTools(bridge, {
      sessions: terminalSessions,
      workspaceId: scopeTargetId,
      workspaceName: scopeLabel,
    }, commandBlocklist, globalPermissionMode);

    const systemPrompt = buildSystemPrompt({
      scopeType, scopeLabel,
      hosts: terminalSessions.map(s => ({
        sessionId: s.sessionId, hostname: s.hostname, label: s.label,
        os: s.os, username: s.username, connected: s.connected,
      })),
      permissionMode: globalPermissionMode,
    });

    // Guard: activeProvider must exist for Catty agent path
    if (!activeProvider) {
      reportStreamError(sessionId, abortController.signal, 'No AI provider configured. Please configure a provider in Settings → AI.');
      return;
    }

    // Decrypt API key and create model in a short-lived scope to minimize
    // plaintext key exposure in memory
    let model;
    try {
      const decryptedKey = activeProvider.apiKey && bridge?.credentialsDecrypt
        ? await bridge.credentialsDecrypt(activeProvider.apiKey)
        : activeProvider.apiKey;
      if (activeProvider.apiKey && bridge?.credentialsDecrypt && !decryptedKey) {
        reportStreamError(sessionId, abortController.signal, 'API key decryption returned empty result. Please re-enter the API key in Settings → AI.');
        return;
      }
      model = createModelFromConfig({
        ...activeProvider,
        apiKey: decryptedKey,
        defaultModel: activeModelId || activeProvider.defaultModel || '',
      });
    } catch (e) {
      console.error('[Catty] API key decryption failed:', e);
      reportStreamError(sessionId, abortController.signal, `API key decryption failed: ${e instanceof Error ? e.message : String(e)}`);
      return;
    }

    try {
      // Issue #5: Build SDK messages including tool-call and tool-result messages
      // so the LLM maintains full conversation context
      const sdkMessages: Array<ModelMessage> = [];
      for (const m of (currentSession?.messages ?? [])) {
        if (m.role === 'user') {
          sdkMessages.push({ role: 'user', content: m.content });
        } else if (m.role === 'assistant') {
          if (m.toolCalls?.length) {
            // Build assistant content parts: text + tool calls
            const contentParts: Array<
              { type: 'text'; text: string } |
              { type: 'tool-call'; toolCallId: string; toolName: string; input: unknown }
            > = [];
            if (m.content) {
              contentParts.push({ type: 'text' as const, text: m.content });
            }
            for (const tc of m.toolCalls) {
              contentParts.push({
                type: 'tool-call' as const,
                toolCallId: tc.id,
                toolName: tc.name,
                input: tc.arguments ?? {},
              });
            }
            sdkMessages.push({ role: 'assistant', content: contentParts });
          } else if (m.content) {
            sdkMessages.push({ role: 'assistant', content: m.content });
          }
        } else if (m.role === 'tool' && m.toolResults?.length) {
          // Map tool results to SDK tool message format
          sdkMessages.push({
            role: 'tool',
            content: m.toolResults.map(tr => ({
              type: 'tool-result' as const,
              toolCallId: tr.toolCallId,
              toolName: '',
              output: { type: 'text' as const, value: tr.content },
            })),
          });
        }
      }
      sdkMessages.push({ role: 'user', content: trimmed });

      const approvalInfo = await processCattyStream(sessionId, model, systemPrompt, tools, sdkMessages, abortController.signal, assistantMsgId);

      if (approvalInfo) {
        setPendingApproval({
          sessionId, scopeKey: sendScopeKey, sdkMessages, approvalInfo, model, systemPrompt, tools,
        });
        return; // Keep streaming flag — waiting for user approval
      }
    } catch (err) {
      console.error('[Catty] streamText error:', err);
      reportStreamError(sessionId, abortController.signal, err);
    } finally {
      if (!pendingApprovalContextRef.current || pendingApprovalContextRef.current.sessionId !== sessionId) {
        setStreamingForScope(sessionId, false);
        abortControllersRef.current.delete(sessionId);
      }
      autoTitleSession(sessionId, trimmed);
    }
  }, [
    activeProvider, activeModelId, scopeType, scopeTargetId, scopeLabel,
    globalPermissionMode, commandBlocklist, terminalSessions,
    processCattyStream, reportStreamError, setStreamingForScope, autoTitleSession, setPendingApproval,
  ]);

  // -------------------------------------------------------------------
  // Main send handler (thin orchestrator)
  // -------------------------------------------------------------------

  const handleSend = useCallback(async () => {
    const trimmed = inputValueRef.current.trim();
    const sendScopeKey = scopeKey;
    if (!trimmed || isStreaming) return;

    const isExternalAgent = currentAgentId !== 'catty';

    // No provider configured for built-in agent
    if (!isExternalAgent && !activeProvider) {
      const errSessionId = ensureSession();
      addMessageToSession(errSessionId, { id: generateId(), role: 'user', content: trimmed, timestamp: Date.now() });
      addMessageToSession(errSessionId, { id: generateId(), role: 'assistant', content: t('ai.chat.noProvider'), timestamp: Date.now() });
      setInputValue('');
      return;
    }

    // Ensure session exists
    const sessionId = ensureSession();

    // Capture images before clearing
    const attachedImages = imagesRef.current.map(img => ({ base64Data: img.base64Data, mediaType: img.mediaType, filename: img.filename }));

    // Add user message
    addMessageToSession(sessionId, {
      id: generateId(), role: 'user', content: trimmed,
      ...(attachedImages.length > 0 ? { images: attachedImages } : {}),
      timestamp: Date.now(),
    });
    setInputValue('');
    clearImages();
    setStreamingForScope(sessionId, true);

    // Create assistant message placeholder with a tracked ID
    const agentConfig = isExternalAgent ? externalAgents.find(a => a.id === currentAgentId) : undefined;
    const assistantMsgId = generateId();
    addMessageToSession(sessionId, {
      id: assistantMsgId, role: 'assistant', content: '', timestamp: Date.now(),
      model: isExternalAgent ? (agentConfig?.name || 'external') : (activeModelId || activeProvider?.defaultModel || ''),
      providerId: isExternalAgent ? undefined : activeProvider?.providerId,
    });

    const abortController = new AbortController();
    abortControllersRef.current.set(sessionId, abortController);
    const currentSession = sessionsRef.current.find(s => s.id === sessionId);

    if (isExternalAgent) {
      if (!agentConfig) {
        updateMessageById(sessionId, assistantMsgId, msg => ({ ...msg, content: 'External agent not found. Please check settings.', executionStatus: 'failed' }));
        setStreamingForScope(sessionId, false);
        return;
      }
      try {
        await sendToExternalAgent(sessionId, trimmed, agentConfig, abortController, attachedImages);
      } catch (err) {
        reportStreamError(sessionId, abortController.signal, err);
      }
      setStreamingForScope(sessionId, false);
      abortControllersRef.current.delete(sessionId);
      autoTitleSession(sessionId, trimmed);
    } else {
      await sendToCattyAgent(sessionId, sendScopeKey, trimmed, abortController, currentSession ?? undefined, assistantMsgId);
    }
  }, [
    isStreaming, activeProvider, scopeKey, currentAgentId,
    activeModelId, externalAgents,
    ensureSession, addMessageToSession, updateMessageById,
    setStreamingForScope, setInputValue, clearImages,
    sendToExternalAgent, sendToCattyAgent, reportStreamError, autoTitleSession, t,
  ]);

  const handleStop = useCallback(() => {
    if (!activeSessionId) return;
    const controller = abortControllersRef.current.get(activeSessionId);
    controller?.abort();
    abortControllersRef.current.delete(activeSessionId);
    setStreamingForScope(activeSessionId, false);
    // Also clear any pending approval (clears timeout too via setPendingApproval)
    if (pendingApprovalContextRef.current?.sessionId === activeSessionId) {
      setPendingApproval(null);
    }
  }, [activeSessionId, setStreamingForScope, setPendingApproval]);

  // Handle inline approval response (approve/reject from InlineApprovalCard)
  const handleApprovalResponse = useCallback(async (messageId: string, approved: boolean) => {
    const ctx = pendingApprovalContextRef.current;
    if (!ctx) return;
    // Destructure all needed values BEFORE clearing the ref to avoid race conditions
    const { sessionId: sid, scopeKey: sk, sdkMessages, approvalInfo, model: ctxModel } = ctx;
    // Clear pending approval (and its timeout) via setPendingApproval
    setPendingApproval(null);

    // Update the message's pendingApproval status using message ID
    updateMessageById(sid, messageId, msg => ({
      ...msg,
      pendingApproval: msg.pendingApproval
        ? { ...msg.pendingApproval, status: approved ? 'approved' as const : 'denied' as const }
        : undefined,
    }));

    if (!approved) {
      // User rejected — add denial text and stop
      updateMessageById(sid, messageId, msg => ({
        ...msg,
        content: msg.content + (msg.content ? '\n\n' : '') + t('ai.chat.toolDenied'),
        executionStatus: 'completed',
      }));
      setStreamingForScope(sid, false);
      abortControllersRef.current.delete(sid);
      return;
    }

    // User approved — construct SDK messages with approval response and resume
    const resumeMessages: Array<Record<string, unknown>> = [
      ...sdkMessages,
      // The assistant message that contained the tool call + approval request
      {
        role: 'assistant',
        content: [
          {
            type: 'tool-call',
            toolCallId: approvalInfo.toolCallId,
            toolName: approvalInfo.toolName,
            input: approvalInfo.toolArgs,
          },
          {
            type: 'tool-approval-request',
            approvalId: approvalInfo.approvalId,
            toolCallId: approvalInfo.toolCallId,
          },
        ],
      },
      // The user's approval response
      {
        role: 'tool',
        content: [
          {
            type: 'tool-approval-response',
            approvalId: approvalInfo.approvalId,
            approved: true,
          },
        ],
      },
    ];

    // Create a new assistant message placeholder for the continuation
    const newAssistantMsgId = generateId();
    addMessageToSession(sid, {
      id: newAssistantMsgId,
      role: 'assistant',
      content: '',
      timestamp: Date.now(),
    });

    const abortController = new AbortController();
    abortControllersRef.current.set(sid, abortController);

    try {
      // Rebuild tools and system prompt with the latest permission mode to prevent
      // stale closure issues (e.g. user changed permission mode during approval wait)
      const bridge = getNetcattyBridge();
      const freshTools = createCattyTools(bridge, {
        sessions: terminalSessions,
        workspaceId: scopeTargetId,
        workspaceName: scopeLabel,
      }, commandBlocklist, globalPermissionMode);
      const freshSystemPrompt = buildSystemPrompt({
        scopeType, scopeLabel,
        hosts: terminalSessions.map(s => ({
          sessionId: s.sessionId, hostname: s.hostname, label: s.label,
          os: s.os, username: s.username, connected: s.connected,
        })),
        permissionMode: globalPermissionMode,
      });
      const newApprovalInfo = await processCattyStream(sid, ctxModel, freshSystemPrompt, freshTools, resumeMessages, abortController.signal, newAssistantMsgId);

      if (newApprovalInfo) {
        // Another approval needed — save context for the next round (with timeout)
        setPendingApproval({
          sessionId: sid,
          scopeKey: sk,
          sdkMessages: resumeMessages,
          approvalInfo: newApprovalInfo,
          model: ctxModel,
          systemPrompt: freshSystemPrompt,
          tools: freshTools,
        });
        return;
      }
    } catch (err) {
      console.error('[Catty resume] streamText error:', err);
      if (!abortController.signal.aborted) {
        const errorStr = err instanceof Error ? err.message : String(err);
        updateMessageById(sid, newAssistantMsgId, msg => ({
          ...msg,
          executionStatus: msg.executionStatus === 'running' ? 'failed' : msg.executionStatus,
        }));
        addMessageToSession(sid, {
          id: generateId(),
          role: 'assistant',
          content: '',
          errorInfo: classifyError(errorStr),
          timestamp: Date.now(),
        });
      }
    } finally {
      if (!pendingApprovalContextRef.current || pendingApprovalContextRef.current.sessionId !== sid) {
        setStreamingForScope(sid, false);
        abortControllersRef.current.delete(sid);
      }
    }
  }, [
    processCattyStream, addMessageToSession, updateMessageById, setStreamingForScope, t,
    terminalSessions, scopeType, scopeTargetId, scopeLabel,
    globalPermissionMode, commandBlocklist, setPendingApproval,
  ]);

  const handleSelectSession = useCallback(
    (sessionId: string) => {
      setActiveSessionId(sessionId);
      // Restore agent selector to match the session's bound agent
      const session = sessions.find((s) => s.id === sessionId);
      if (session) {
        setCurrentAgentId(session.agentId);
      }
      setShowHistory(false);
    },
    [setActiveSessionId, sessions],
  );

  const handleDeleteSession = useCallback(
    (e: React.MouseEvent, sessionId: string) => {
      e.stopPropagation();
      const bridge = getNetcattyBridge();
      void bridge?.aiAcpCleanup?.(sessionId).catch(() => {});
      deleteSession(sessionId, scopeKey);
      // Active session clearing is handled by deleteSession with scopeKey
    },
    [deleteSession, scopeKey],
  );

  const handleAgentChange = useCallback((agentId: string) => {
    setCurrentAgentId(agentId);
    // Switching agent deactivates current session; a new one is created on next send
    setActiveSessionId(null);
  }, [setActiveSessionId]);

  const handleExport = useCallback((format: 'md' | 'json' | 'txt') => {
    if (!activeSession) return;
    let content: string;
    switch (format) {
      case 'md': content = exportAsMarkdown(activeSession); break;
      case 'json': content = exportAsJSON(activeSession); break;
      case 'txt': content = exportAsPlainText(activeSession); break;
    }
    const filename = getExportFilename(activeSession, format);
    // Create a download blob
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    // Track URL for cleanup on unmount (Issue #19)
    activeObjectUrlsRef.current.add(url);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    // Revoke after a generous delay to ensure download completes, then remove from tracking set
    const revokeTimeout = setTimeout(() => {
      URL.revokeObjectURL(url);
      activeObjectUrlsRef.current.delete(url);
    }, 60_000); // 60 seconds to be safe for large files
    // If component unmounts before timeout, cleanup effect will revoke it
    void revokeTimeout; // suppress unused warning
  }, [activeSession]);

  // -------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------

  if (!isVisible) return null;

  return (
    <div className="flex flex-col h-full bg-background">
      {/* ── Header ── */}
      <div className="px-2.5 py-1.5 flex items-center justify-between border-b border-border/50 shrink-0">
        <AgentSelector
          currentAgentId={currentAgentId}
          externalAgents={externalAgents}
          discoveredAgents={discoveredAgents}
          isDiscovering={isDiscovering}
          onSelectAgent={handleAgentChange}
          onEnableDiscoveredAgent={handleEnableDiscoveredAgent}
          onRediscover={rediscover}
          onManageAgents={handleOpenSettings}
        />
        <div className="flex items-center gap-0.5">
          <ConversationExport
            session={activeSession}
            onExport={handleExport}
          />
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 rounded-md text-muted-foreground/62 hover:bg-white/[0.05] hover:text-foreground"
            onClick={() => setShowHistory(!showHistory)}
            title="Session history"
          >
            <History size={14} />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 rounded-md text-primary/82 hover:bg-primary/[0.10] hover:text-primary"
            onClick={handleNewChat}
            title="New chat"
          >
            <Plus size={15} />
          </Button>
        </div>
      </div>

      {/* ── Main content ── */}
      {showHistory ? (
        <SessionHistoryDrawer
          sessions={historySessions}
          activeSessionId={activeSessionId}
          onSelect={handleSelectSession}
          onDelete={handleDeleteSession}
          onClose={() => setShowHistory(false)}
        />
      ) : (
        <>
          {/* Chat messages */}
          <ChatMessageList
            messages={messages}
            isStreaming={isStreaming}
            onApprove={(messageId) => void handleApprovalResponse(messageId, true)}
            onReject={(messageId) => void handleApprovalResponse(messageId, false)}
          />

          {/* Recent sessions (Zed-style, shown when no messages) */}
          {messages.length === 0 && historySessions.length > 0 && (
            <div className="shrink-0 px-4 pb-1">
              <div className="flex items-baseline justify-between mb-2">
                <span className="text-[11px] text-muted-foreground/30 tracking-wide">{t('ai.chat.recent')}</span>
                <button
                  onClick={() => setShowHistory(true)}
                  className="text-[11px] text-muted-foreground/30 hover:text-muted-foreground/50 transition-colors cursor-pointer"
                >
                  {t('ai.chat.viewAll')}
                </button>
              </div>
              {historySessions.slice(0, 3).map((session) => (
                <button
                  key={session.id}
                  onClick={() => handleSelectSession(session.id)}
                  className="w-full flex items-baseline justify-between py-1.5 text-left hover:text-foreground transition-colors cursor-pointer"
                >
                  <span className="text-[13px] text-foreground/60 truncate pr-4">
                    {session.title || t('ai.chat.untitled')}
                  </span>
                  <span className="text-[11px] text-muted-foreground/25 shrink-0">
                    {formatRelativeTime(new Date(session.updatedAt), t)}
                  </span>
                </button>
              ))}
            </div>
          )}

          {/* Input area */}
          <ChatInput
            value={inputValue}
            onChange={setInputValue}
            onSend={handleSend}
            onStop={handleStop}
            isStreaming={isStreaming}
            providerName={providerDisplayName}
            modelName={modelDisplayName}
            agentName={currentAgentId === 'catty' ? 'Catty Agent' : externalAgents.find(a => a.id === currentAgentId)?.name}
            modelPresets={agentModelPresets}
            selectedModelId={selectedAgentModel}
            onModelSelect={handleAgentModelSelect}
            images={images}
            onAddImages={addImages}
            onRemoveImage={removeImage}
            hosts={terminalSessions.map(s => ({ sessionId: s.sessionId, hostname: s.hostname, label: s.label, connected: s.connected }))}
            permissionMode={globalPermissionMode}
            onPermissionModeChange={setGlobalPermissionMode}
          />
        </>
      )}

    </div>
  );
};

// -------------------------------------------------------------------
// Session History Drawer
// -------------------------------------------------------------------

interface SessionHistoryDrawerProps {
  sessions: AISession[];
  activeSessionId: string | null;
  onSelect: (sessionId: string) => void;
  onDelete: (e: React.MouseEvent, sessionId: string) => void;
  onClose: () => void;
}

const SessionHistoryDrawer: React.FC<SessionHistoryDrawerProps> = ({
  sessions,
  activeSessionId,
  onSelect,
  onDelete,
  onClose,
}) => {
  const { t } = useI18n();
  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div className="px-4 py-2.5 flex items-center justify-between shrink-0 border-b border-border/30">
        <span className="text-[13px] font-medium text-foreground/80">{t('ai.chat.allSessions')}</span>
        <button
          onClick={onClose}
          className="text-[12px] text-muted-foreground/60 hover:text-muted-foreground transition-colors cursor-pointer"
        >
          <X size={14} />
        </button>
      </div>
      <ScrollArea className="flex-1">
        <div className="px-3">
          {sessions.length === 0 ? (
            <div className="py-12 text-center">
              <p className="text-[13px] text-muted-foreground/40">
                {t('ai.chat.noSessions')}
              </p>
            </div>
          ) : (
            sessions.map((session) => {
              const isActive = session.id === activeSessionId;
              const time = new Date(session.updatedAt);
              const timeStr = formatRelativeTime(time, t);

              return (
                <button
                  key={session.id}
                  onClick={() => onSelect(session.id)}
                  className={cn(
                    'w-full flex items-center justify-between py-2.5 border-b border-border/20 text-left transition-colors cursor-pointer group',
                    isActive ? 'text-foreground' : 'text-foreground/70 hover:text-foreground',
                  )}
                >
                  <span className="text-[13px] truncate pr-3 flex-1 min-w-0">
                    {session.title || t('ai.chat.untitled')}
                  </span>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-[12px] text-muted-foreground/50">
                      {timeStr}
                    </span>
                    <button
                      onClick={(e) => onDelete(e, session.id)}
                      className="opacity-0 group-hover:opacity-100 p-0.5 hover:text-destructive transition-all cursor-pointer"
                      title="Delete"
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                </button>
              );
            })
          )}
        </div>
      </ScrollArea>
    </div>
  );
};

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

function formatRelativeTime(date: Date, t: (key: string) => string): string {
  const now = Date.now();
  const diff = now - date.getTime();
  const minutes = Math.floor(diff / 60_000);
  const hours = Math.floor(diff / 3_600_000);
  const days = Math.floor(diff / 86_400_000);

  if (minutes < 1) return t('ai.chat.justNow');
  if (minutes < 60) return t('ai.chat.minutesAgo').replace('{n}', String(minutes));
  if (hours < 24) return t('ai.chat.hoursAgo').replace('{n}', String(hours));
  if (days < 7) return t('ai.chat.daysAgo').replace('{n}', String(days));
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// -------------------------------------------------------------------
// Export
// -------------------------------------------------------------------

const AIChatSidePanel = React.memo(AIChatSidePanelInner);
AIChatSidePanel.displayName = 'AIChatSidePanel';

export default AIChatSidePanel;
export { AIChatSidePanel };
export type { AIChatSidePanelProps };
