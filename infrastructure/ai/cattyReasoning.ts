import type { ProviderStyle } from './types';
import { resolveProviderStyle, type ProviderConfig } from './types';
import {
  CATTY_REASONING_LEVELS,
  normalizeCattyReasoningLevel,
  type CattyReasoningLevel,
} from './composerPicker';

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
    if (modelId && !openaiModelLikelySupportsReasoning(modelId)) return undefined;
    if (level === 'off') {
      if (modelId && openaiModelLikelySupportsReasoning(modelId)) {
        return { openai: { reasoningEffort: 'none' } };
      }
      return undefined;
    }
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
      const thinkingLevel = gemini3ThinkingLevel(modelId, level);
      if (!thinkingLevel) return undefined;
      return {
        google: {
          thinkingConfig: {
            thinkingLevel,
            includeThoughts: level !== 'off',
          },
        },
      };
    }
    if (level === 'off') {
      if (!googleModelAllowsDisabledThinking(modelId)) return undefined;
      return {
        google: {
          thinkingConfig: {
            thinkingBudget: 0,
            includeThoughts: false,
          },
        },
      };
    }
    return {
      google: {
        thinkingConfig: {
          thinkingBudget: GEMINI_25_THINKING_BUDGET[level],
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

/** OpenAI-compat models that accept `reasoning_effort` (o-series, GPT-5, reasoners). */
export function openaiModelLikelySupportsReasoning(modelId: string): boolean {
  const id = modelId.trim().toLowerCase();
  if (!id) return false;
  return (
    /(^|[^a-z0-9])o[1-4]([^a-z0-9]|$)/.test(id)
    || /gpt-5/.test(id)
    || /gpt-oss/.test(id)
    || /reasoner|reasoning/.test(id)
    || /deepseek-r1/.test(id)
    || /grok-4/.test(id)
  );
}

/** Levels shown on the Catty thinking chip, or empty when the model cannot take them. */
export function cattyReasoningLevelsForSelection(
  provider: Pick<ProviderConfig, 'providerId' | 'style'> | null | undefined,
  modelId?: string,
): readonly CattyReasoningLevel[] {
  if (!provider) return [];
  const style = resolveProviderStyle(provider);
  if (style === 'anthropic') return CATTY_REASONING_LEVELS;
  if (style === 'google') {
    return modelId && googleModelLikelySupportsThinking(modelId) ? CATTY_REASONING_LEVELS : [];
  }
  if (style === 'openai') {
    return modelId && openaiModelLikelySupportsReasoning(modelId) ? CATTY_REASONING_LEVELS : [];
  }
  return [];
}

function isGemini3Model(modelId: string): boolean {
  return modelId.trim().toLowerCase().includes('gemini-3');
}

function gemini3ThinkingLevel(
  modelId: string,
  level: CattyReasoningLevel,
): 'minimal' | 'low' | 'medium' | 'high' | undefined {
  const id = modelId.trim().toLowerCase();
  const isFlash = /flash/.test(id);
  if (level === 'off') return isFlash ? 'minimal' : undefined;
  if (!isFlash && /pro/.test(id) && level === 'medium') return 'high';
  return level;
}

function googleModelAllowsDisabledThinking(modelId: string): boolean {
  const id = modelId.trim().toLowerCase();
  return /flash-lite|flash/.test(id) && !/pro/.test(id);
}
