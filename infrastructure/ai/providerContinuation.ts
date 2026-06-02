export type ProviderContinuationJSONValue =
  | string
  | number
  | boolean
  | null
  | ProviderContinuationJSONValue[]
  | { [key: string]: ProviderContinuationJSONValue };

export type ProviderContinuationOptions = Record<string, Record<string, ProviderContinuationJSONValue>>;

export interface ProviderContinuationReasoningPart {
  text: string;
  providerOptions?: ProviderContinuationOptions;
}

export interface ProviderContinuationSource {
  providerConfigId: string;
  providerType: string;
  modelId?: string;
}

export interface ProviderContinuation {
  source?: ProviderContinuationSource;
  reasoningParts?: ProviderContinuationReasoningPart[];
  textProviderOptions?: ProviderContinuationOptions;
  toolCallProviderOptionsById?: Record<string, ProviderContinuationOptions>;
  openAIChatAssistantFields?: Record<string, unknown>;
}

export type OpenAIChatAssistantFields = Record<string, unknown>;

export interface ProviderContinuationHistoryMessage {
  providerContinuation?: ProviderContinuation;
  thinking?: string;
  model?: string;
  providerId?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isSafeObjectKey(key: string): boolean {
  return key !== '__proto__' && key !== 'prototype' && key !== 'constructor';
}

function parseRawValue(rawValue: unknown): unknown {
  if (typeof rawValue !== 'string') return rawValue;
  try {
    return JSON.parse(rawValue);
  } catch {
    return rawValue;
  }
}

function toContinuationJSONValue(value: unknown): ProviderContinuationJSONValue | undefined {
  if (value === null) return null;
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    return value;
  }
  if (Array.isArray(value)) {
    const values: ProviderContinuationJSONValue[] = [];
    for (const item of value) {
      const converted = toContinuationJSONValue(item);
      if (converted !== undefined) values.push(converted);
    }
    return values;
  }
  if (isRecord(value)) {
    const converted: { [key: string]: ProviderContinuationJSONValue } = {};
    for (const [key, item] of Object.entries(value)) {
      if (!isSafeObjectKey(key)) continue;
      const convertedItem = toContinuationJSONValue(item);
      if (convertedItem !== undefined) converted[key] = convertedItem;
    }
    return converted;
  }
  return undefined;
}

export function normalizeProviderContinuationOptions(value: unknown): ProviderContinuationOptions | undefined {
  if (!isRecord(value)) return undefined;
  const options: ProviderContinuationOptions = {};
  for (const [provider, providerOptions] of Object.entries(value)) {
    if (!isSafeObjectKey(provider)) continue;
    if (!isRecord(providerOptions)) continue;
    const normalizedProviderOptions: Record<string, ProviderContinuationJSONValue> = {};
    for (const [key, optionValue] of Object.entries(providerOptions)) {
      if (!isSafeObjectKey(key)) continue;
      const normalizedValue = toContinuationJSONValue(optionValue);
      if (normalizedValue !== undefined) normalizedProviderOptions[key] = normalizedValue;
    }
    if (Object.keys(normalizedProviderOptions).length) {
      options[provider] = normalizedProviderOptions;
    }
  }
  return Object.keys(options).length ? options : undefined;
}

function cloneProviderOptions(options: ProviderContinuationOptions | undefined): ProviderContinuationOptions | undefined {
  if (!options) return undefined;
  const cloned: ProviderContinuationOptions = {};
  for (const [provider, providerOptions] of Object.entries(options)) {
    if (!isSafeObjectKey(provider)) continue;
    const safeProviderOptions: Record<string, ProviderContinuationJSONValue> = {};
    for (const [key, value] of Object.entries(providerOptions)) {
      if (!isSafeObjectKey(key)) continue;
      const normalizedValue = toContinuationJSONValue(value);
      if (normalizedValue !== undefined) safeProviderOptions[key] = normalizedValue;
    }
    if (Object.keys(safeProviderOptions).length) cloned[provider] = safeProviderOptions;
  }
  return cloned;
}

function cloneReasoningPart(part: ProviderContinuationReasoningPart): ProviderContinuationReasoningPart {
  return {
    text: part.text,
    ...(part.providerOptions ? { providerOptions: cloneProviderOptions(part.providerOptions) } : {}),
  };
}

function mergeProviderOptions(
  current: ProviderContinuationOptions | undefined,
  incoming: ProviderContinuationOptions | undefined,
): ProviderContinuationOptions | undefined {
  if (!current && !incoming) return undefined;
  const merged: ProviderContinuationOptions = cloneProviderOptions(current) ?? {};
  for (const [provider, providerOptions] of Object.entries(incoming ?? {})) {
    if (!isSafeObjectKey(provider)) continue;
    const safeIncoming: Record<string, ProviderContinuationJSONValue> = {};
    for (const [key, value] of Object.entries(providerOptions)) {
      if (!isSafeObjectKey(key)) continue;
      const normalizedValue = toContinuationJSONValue(value);
      if (normalizedValue !== undefined) safeIncoming[key] = normalizedValue;
    }
    const existing = isRecord(merged[provider]) ? merged[provider] : undefined;
    if (!existing && !Object.keys(safeIncoming).length) continue;
    merged[provider] = {
      ...(existing ?? {}),
      ...safeIncoming,
    };
  }
  return Object.keys(merged).length ? merged : undefined;
}

function mergeAssistantFields(
  current: Record<string, unknown> | undefined,
  incoming: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (!current && !incoming) return undefined;
  const merged: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(current ?? {})) {
    if (!isSafeObjectKey(key)) continue;
    merged[key] = value;
  }
  for (const [key, value] of Object.entries(incoming ?? {})) {
    if (!isSafeObjectKey(key)) continue;
    if (value === undefined) continue;
    const safeValue = typeof value === 'string' ? value : toContinuationJSONValue(value);
    if (safeValue === undefined) continue;
    const previous = merged[key];
    merged[key] = typeof previous === 'string' && typeof safeValue === 'string'
      ? previous + safeValue
      : safeValue;
  }
  return Object.keys(merged).length ? merged : undefined;
}

function isSameProviderContinuationSource(
  current: ProviderContinuationSource | undefined,
  incoming: ProviderContinuationSource | undefined,
): boolean {
  if (!current || !incoming) return false;
  return current.providerConfigId === incoming.providerConfigId
    && current.providerType === incoming.providerType
    && current.modelId === incoming.modelId;
}

function stableJSONValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stableJSONValue);
  if (!isRecord(value)) return value;
  const stable: Record<string, unknown> = {};
  for (const key of Object.keys(value).filter(isSafeObjectKey).sort()) {
    stable[key] = stableJSONValue(value[key]);
  }
  return stable;
}

function providerOptionsKey(options: ProviderContinuationOptions | undefined): string {
  return JSON.stringify(stableJSONValue(options ?? {}));
}

function canMergeReasoningPart(
  current: ProviderContinuationReasoningPart,
  incoming: ProviderContinuationReasoningPart,
): boolean {
  if (!incoming.text) return true;
  return providerOptionsKey(current.providerOptions) === providerOptionsKey(incoming.providerOptions);
}

function appendReasoningParts(
  current: ProviderContinuationReasoningPart[] | undefined,
  incoming: ProviderContinuationReasoningPart[] | undefined,
): ProviderContinuationReasoningPart[] | undefined {
  const merged = (current ?? []).map(cloneReasoningPart);

  for (const part of incoming ?? []) {
    if (!part.text && !part.providerOptions) continue;
    const normalizedPart = cloneReasoningPart(part);
    const last = merged.at(-1);
    if (last && canMergeReasoningPart(last, normalizedPart)) {
      last.text += normalizedPart.text;
      const providerOptions = mergeProviderOptions(last.providerOptions, normalizedPart.providerOptions);
      if (providerOptions) {
        last.providerOptions = providerOptions;
      } else {
        delete last.providerOptions;
      }
      continue;
    }
    merged.push(normalizedPart);
  }

  return merged.length ? merged : undefined;
}

export function mergeProviderContinuation(
  current?: ProviderContinuation | null,
  incoming?: ProviderContinuation | null,
): ProviderContinuation | undefined {
  const base = current?.source && incoming?.source && !isSameProviderContinuationSource(current.source, incoming.source)
    ? undefined
    : current;
  const reasoningParts = appendReasoningParts(base?.reasoningParts, incoming?.reasoningParts);
  const textProviderOptions = mergeProviderOptions(base?.textProviderOptions, incoming?.textProviderOptions);
  const toolCallProviderOptionsById = mergeToolCallProviderOptions(
    base?.toolCallProviderOptionsById,
    incoming?.toolCallProviderOptionsById,
  );
  const openAIChatAssistantFields = mergeAssistantFields(
    base?.openAIChatAssistantFields,
    incoming?.openAIChatAssistantFields,
  );
  const source = incoming?.source ?? base?.source;

  if (!reasoningParts && !textProviderOptions && !toolCallProviderOptionsById && !openAIChatAssistantFields) {
    return undefined;
  }
  return {
    ...(source ? { source } : {}),
    ...(reasoningParts ? { reasoningParts } : {}),
    ...(textProviderOptions ? { textProviderOptions } : {}),
    ...(toolCallProviderOptionsById ? { toolCallProviderOptionsById } : {}),
    ...(openAIChatAssistantFields ? { openAIChatAssistantFields } : {}),
  };
}

function mergeToolCallProviderOptions(
  current: Record<string, ProviderContinuationOptions> | undefined,
  incoming: Record<string, ProviderContinuationOptions> | undefined,
): Record<string, ProviderContinuationOptions> | undefined {
  if (!current && !incoming) return undefined;
  const merged: Record<string, ProviderContinuationOptions> = {};
  for (const [toolCallId, providerOptions] of Object.entries(current ?? {})) {
    if (!isSafeObjectKey(toolCallId)) continue;
    const cloned = cloneProviderOptions(providerOptions);
    if (cloned) merged[toolCallId] = cloned;
  }
  for (const [toolCallId, providerOptions] of Object.entries(incoming ?? {})) {
    if (!isSafeObjectKey(toolCallId)) continue;
    const next = mergeProviderOptions(merged[toolCallId], providerOptions);
    if (next) merged[toolCallId] = next;
  }
  return Object.keys(merged).length ? merged : undefined;
}

export function withProviderContinuationSource(
  continuation: ProviderContinuation | undefined,
  source: ProviderContinuationSource | undefined,
): ProviderContinuation | undefined {
  if (!continuation) return undefined;
  return source ? { ...continuation, source } : continuation;
}

export function isProviderContinuationForSource(
  continuation: ProviderContinuation | undefined,
  source: ProviderContinuationSource | undefined,
): boolean {
  if (!continuation?.source || !source) return false;
  return continuation.source.providerConfigId === source.providerConfigId
    && continuation.source.providerType === source.providerType
    && continuation.source.modelId === source.modelId;
}

export function getOpenAIChatAssistantFieldsForHistoryMessage(
  message: ProviderContinuationHistoryMessage,
  source: ProviderContinuationSource | undefined,
): OpenAIChatAssistantFields | undefined {
  const activeContinuation = isProviderContinuationForSource(message.providerContinuation, source)
    ? message.providerContinuation
    : undefined;
  if (activeContinuation?.openAIChatAssistantFields) {
    return activeContinuation.openAIChatAssistantFields;
  }

  if (!source) return undefined;
  if (message.providerId !== source.providerType) return undefined;
  if (source.modelId && message.model !== source.modelId) return undefined;

  const thinking = typeof message.thinking === 'string' ? message.thinking.trim() : '';
  return thinking ? { reasoning_content: thinking } : undefined;
}

export function extractProviderContinuationFromRawChunk(rawValue: unknown): ProviderContinuation | undefined {
  const parsed = parseRawValue(rawValue);
  if (!isRecord(parsed) || !Array.isArray(parsed.choices)) return undefined;

  let reasoningContent = '';
  for (const choice of parsed.choices) {
    if (!isRecord(choice)) continue;
    const delta = isRecord(choice.delta) ? choice.delta : undefined;
    const message = isRecord(choice.message) ? choice.message : undefined;
    const rawReasoning = delta?.reasoning_content ?? message?.reasoning_content;
    if (typeof rawReasoning === 'string' && rawReasoning) {
      reasoningContent += rawReasoning;
    }
  }

  if (!reasoningContent) return undefined;
  return {
    reasoningParts: [{ text: reasoningContent }],
    openAIChatAssistantFields: { reasoning_content: reasoningContent },
  };
}

export function rawOpenAIChatChunkHasToolCalls(rawValue: unknown): boolean {
  const parsed = parseRawValue(rawValue);
  if (!isRecord(parsed) || !Array.isArray(parsed.choices)) return false;

  for (const choice of parsed.choices) {
    if (!isRecord(choice)) continue;
    const delta = isRecord(choice.delta) ? choice.delta : undefined;
    const message = isRecord(choice.message) ? choice.message : undefined;
    if (delta && hasToolCalls(delta)) return true;
    if (message && hasToolCalls(message)) return true;
  }

  return false;
}

function hasToolCalls(message: Record<string, unknown>): boolean {
  return Array.isArray(message.tool_calls) && message.tool_calls.length > 0;
}

function compactAssistantFields(fields: OpenAIChatAssistantFields | undefined): OpenAIChatAssistantFields | undefined {
  const compacted: OpenAIChatAssistantFields = {};
  for (const [key, value] of Object.entries(fields ?? {})) {
    if (!isSafeObjectKey(key)) continue;
    if (value === undefined || value === null || value === '') continue;
    const safeValue = typeof value === 'string' ? value : toContinuationJSONValue(value);
    if (safeValue === undefined || safeValue === null || safeValue === '') continue;
    compacted[key] = safeValue;
  }
  return Object.keys(compacted).length ? compacted : undefined;
}

export function applyOpenAIChatContinuationToBody(
  body: string,
  assistantFieldsByContinuationMessage: Array<OpenAIChatAssistantFields | undefined>,
): string {
  if (!assistantFieldsByContinuationMessage.length) return body;

  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return body;
  }

  if (!isRecord(parsed) || !Array.isArray(parsed.messages)) return body;

  let fieldIndex = 0;
  let changed = false;
  let previousMessageWasTool = false;
  const messages = parsed.messages.map((message) => {
    const isToolMessage = isRecord(message) && message.role === 'tool';
    const needsContinuationFields = isRecord(message)
      && message.role === 'assistant'
      && (hasToolCalls(message) || previousMessageWasTool);
    previousMessageWasTool = isToolMessage;

    if (!needsContinuationFields) {
      return message;
    }

    const fields = compactAssistantFields(assistantFieldsByContinuationMessage[fieldIndex]);
    fieldIndex += 1;
    if (!fields) return message;

    changed = true;
    return {
      ...message,
      ...mergeAssistantFields(message, fields),
    };
  });

  if (!changed) return body;
  return JSON.stringify({ ...parsed, messages });
}

function messageHasAssistantContent(message: Record<string, unknown>): boolean {
  const content = message.content;
  if (typeof content === 'string') return content.trim().length > 0;
  if (Array.isArray(content)) return content.length > 0;
  return content !== undefined && content !== null;
}

/**
 * OpenAI-compatible chat APIs reject a request when an assistant message
 * contains tool_calls that are not immediately answered by matching tool
 * messages. This can happen when a prior Catty tool loop was interrupted
 * mid-flight. Repair the outgoing history instead of letting one broken
 * turn permanently brick the chat session.
 */
export function repairOpenAIChatToolResultPairsInBody(body: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return body;
  }

  if (!isRecord(parsed) || !Array.isArray(parsed.messages)) return body;

  let changed = false;
  const repairedMessages: unknown[] = [];
  const messages = parsed.messages;

  for (let index = 0; index < messages.length; index += 1) {
    const message = messages[index];
    if (!isRecord(message)) {
      repairedMessages.push(message);
      continue;
    }

    if (message.role === 'tool') {
      changed = true;
      continue;
    }

    if (message.role !== 'assistant' || !Array.isArray(message.tool_calls) || message.tool_calls.length === 0) {
      repairedMessages.push(message);
      continue;
    }

    const followingToolMessages: Record<string, unknown>[] = [];
    let lookahead = index + 1;
    while (lookahead < messages.length) {
      const nextMessage = messages[lookahead];
      if (!isRecord(nextMessage) || nextMessage.role !== 'tool') break;
      followingToolMessages.push(nextMessage);
      lookahead += 1;
    }

    const toolMessagesById = new Map<string, Record<string, unknown>>();
    for (const toolMessage of followingToolMessages) {
      const toolCallId = toolMessage.tool_call_id;
      if (typeof toolCallId === 'string' && !toolMessagesById.has(toolCallId)) {
        toolMessagesById.set(toolCallId, toolMessage);
      }
    }

    const validToolCalls = message.tool_calls.filter((toolCall) => {
      if (!isRecord(toolCall)) return false;
      const toolCallId = toolCall.id;
      return typeof toolCallId === 'string' && toolMessagesById.has(toolCallId);
    });

    if (validToolCalls.length !== message.tool_calls.length || validToolCalls.length !== followingToolMessages.length) {
      changed = true;
    }

    if (validToolCalls.length > 0) {
      repairedMessages.push({
        ...message,
        tool_calls: validToolCalls,
      });

      for (const toolCall of validToolCalls) {
        if (!isRecord(toolCall) || typeof toolCall.id !== 'string') continue;
        const toolMessage = toolMessagesById.get(toolCall.id);
        if (toolMessage) repairedMessages.push(toolMessage);
      }
    } else if (messageHasAssistantContent(message)) {
      const { tool_calls: _toolCalls, ...messageWithoutToolCalls } = message;
      void _toolCalls;
      repairedMessages.push(messageWithoutToolCalls);
    }

    index = lookahead - 1;
  }

  if (!changed) return body;
  return JSON.stringify({ ...parsed, messages: repairedMessages });
}
