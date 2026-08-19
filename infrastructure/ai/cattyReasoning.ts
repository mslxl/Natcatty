import type { ProviderStyle } from './types';
import { resolveProviderStyle, type ProviderConfig } from './types';
import { normalizeCattyReasoningLevel, type CattyReasoningLevel } from './composerPicker';

const ANTHROPIC_THINKING_BUDGET: Record<Exclude<CattyReasoningLevel, 'off'>, number> = {
  low: 4_000,
  medium: 10_000,
  high: 20_000,
};

export type CattyReasoningProviderOptions = Record<string, Record<string, unknown>>;

const GEMINI_25_THINKING_BUDGET: Record<Exclude<CattyReasoningLevel, 'off'>, number> = {
  low: 1_024,
  medium: 8_192,
  high: 16_384,
};

export function buildCattyReasoningProviderOptions(
  provider: Pick<ProviderConfig, 'providerId' | 'style'> | null | undefined,
  effort: string | null | undefined,
  modelId?: string,
): CattyReasoningProviderOptions | undefined {
  if (!provider) return undefined;
  const level = normalizeCattyReasoningLevel(effort);
  const style: ProviderStyle = resolveProviderStyle(provider);
  if (style === 'openai') {
    if (level === 'off') return undefined;
    return { openai: { reasoningEffort: level } };
  }
  if (style === 'anthropic') {
    if (level === 'off') return undefined;
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
    if (!modelId || !googleModelLikelySupportsThinking(modelId)) return undefined;
    if (isGemini3Model(modelId)) {
      return {
        google: {
          thinkingConfig: {
            thinkingLevel: level === 'off' ? 'minimal' : level,
            includeThoughts: level !== 'off',
          },
        },
      };
    }
    return {
      google: {
        thinkingConfig: {
          thinkingBudget: level === 'off' ? 0 : GEMINI_25_THINKING_BUDGET[level],
          includeThoughts: level !== 'off',
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

function isGemini3Model(modelId: string): boolean {
  return modelId.trim().toLowerCase().includes('gemini-3');
}
