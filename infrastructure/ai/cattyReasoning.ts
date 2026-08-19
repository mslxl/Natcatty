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
  return undefined;
}
