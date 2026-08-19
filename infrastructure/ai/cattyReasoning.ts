import type { ProviderStyle } from './types';
import { resolveProviderStyle, type ProviderConfig } from './types';
import { normalizeCattyReasoningLevel, type CattyReasoningLevel } from './composerPicker';

const ANTHROPIC_THINKING_BUDGET: Record<Exclude<CattyReasoningLevel, 'off'>, number> = {
  low: 4_000,
  medium: 10_000,
  high: 20_000,
};

export type CattyReasoningProviderOptions = Record<string, Record<string, unknown>>;

export function buildCattyReasoningProviderOptions(
  provider: Pick<ProviderConfig, 'providerId' | 'style'> | null | undefined,
  effort: string | null | undefined,
  modelId?: string,
): CattyReasoningProviderOptions | undefined {
  const level = normalizeCattyReasoningLevel(effort);
  if (level === 'off' || !provider) return undefined;
  const style: ProviderStyle = resolveProviderStyle(provider);
  if (style === 'openai') {
    return { openai: { reasoningEffort: level } };
  }
  if (style === 'anthropic') {
    return {
      anthropic: {
        thinking: {
          type: 'enabled',
          budgetTokens: ANTHROPIC_THINKING_BUDGET[level],
        },
      },
    };
  }
  if (style === 'google') {
    if (modelId && !googleModelLikelySupportsThinking(modelId)) return undefined;
    return {
      google: {
        thinkingConfig: {
          thinkingLevel: level,
          includeThoughts: true,
        },
      },
    };
  }
  return undefined;
}

export function googleModelLikelySupportsThinking(modelId: string): boolean {
  const id = modelId.trim().toLowerCase();
  return /gemini-3|gemini-2\.5|gemini-2\.0-flash-thinking|thinking/.test(id);
}
