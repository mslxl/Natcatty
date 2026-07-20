export const MAX_PLUGIN_COMPLETION_ITEMS = 100;
export const MAX_PLUGIN_DECORATION_RULES = 64;

export interface PluginTerminalCompletionItem {
  readonly text: string;
  readonly displayText: string;
  readonly description?: string;
  readonly score: number;
  readonly providerId: string;
}

export interface PluginTerminalDecorationRule {
  readonly id: string;
  readonly label: string;
  readonly patterns: readonly string[];
  readonly color: string;
  readonly enabled: true;
  readonly providerId: string;
}

function hasUnsafeTextControl(value: string): boolean {
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0;
    if (codePoint <= 0x1f
      || (codePoint >= 0x7f && codePoint <= 0x9f)
      || (codePoint >= 0x202a && codePoint <= 0x202e)
      || (codePoint >= 0x2066 && codePoint <= 0x2069)) return true;
  }
  return false;
}

function boundedString(value: unknown, maximum: number, allowEmpty = false): string | null {
  if (typeof value !== 'string'
    || value.length > maximum
    || (!allowEmpty && value.length < 1)
    || hasUnsafeTextControl(value)) {
    return null;
  }
  return value;
}

function finiteScore(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(-1_000_000, Math.min(1_000_000, value))
    : 0;
}

function freezeArray<T extends object>(values: T[]): readonly Readonly<T>[] {
  for (const value of values) Object.freeze(value);
  return Object.freeze(values);
}

export function normalizePluginCompletionResult(
  providerId: string,
  value: unknown,
): readonly PluginTerminalCompletionItem[] {
  const source = value && typeof value === 'object' && !Array.isArray(value)
    ? value as { items?: unknown }
    : null;
  if (!Array.isArray(source?.items) || source.items.length > MAX_PLUGIN_COMPLETION_ITEMS) return Object.freeze([]);
  const items: PluginTerminalCompletionItem[] = [];
  for (const candidate of source.items) {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) continue;
    const item = candidate as Record<string, unknown>;
    const text = boundedString(item.text, 4_096);
    if (!text) continue;
    const displayText = boundedString(item.displayText, 1_024) ?? text;
    const description = item.description == null ? undefined : boundedString(item.description, 2_048, true);
    if (item.description != null && description == null) continue;
    items.push({
      text,
      displayText,
      ...(description === undefined ? {} : { description }),
      score: finiteScore(item.score),
      providerId,
    });
  }
  return freezeArray(items);
}

export function mergePluginCompletionItems(
  groups: readonly (readonly PluginTerminalCompletionItem[])[],
  maximum: number,
): readonly PluginTerminalCompletionItem[] {
  const seen = new Set<string>();
  const merged = groups.flatMap((group, providerRank) => group.map((item, itemRank) => ({
    item,
    providerRank,
    itemRank,
  })));
  merged.sort((left, right) => right.item.score - left.item.score
    || left.providerRank - right.providerRank
    || left.itemRank - right.itemRank
    || left.item.text.localeCompare(right.item.text));
  const result: PluginTerminalCompletionItem[] = [];
  for (const { item } of merged) {
    if (seen.has(item.text)) continue;
    seen.add(item.text);
    result.push(item);
    if (result.length >= maximum) break;
  }
  return freezeArray(result);
}

export function isSafePluginDecorationPattern(source: string): boolean {
  if (!(source.length > 0
    && source.length <= 512
    && !/\(\?/u.test(source)
    && !/\\(?:[1-9]|k<)/u.test(source)
    && !/\)(?:[*+?]|\{\d+(?:,\d*)?\})/u.test(source))) return false;
  try {
    void new RegExp(source, 'u');
    return true;
  } catch {
    return false;
  }
}

export function normalizePluginDecorationResult(
  providerId: string,
  value: unknown,
): readonly PluginTerminalDecorationRule[] {
  const source = value && typeof value === 'object' && !Array.isArray(value)
    ? value as { rules?: unknown }
    : null;
  if (!Array.isArray(source?.rules) || source.rules.length > MAX_PLUGIN_DECORATION_RULES) return Object.freeze([]);
  const result: PluginTerminalDecorationRule[] = [];
  const seen = new Set<string>();
  for (const candidate of source.rules) {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) continue;
    const rule = candidate as Record<string, unknown>;
    const localId = boundedString(rule.id, 128);
    const label = boundedString(rule.label, 256);
    const color = boundedString(rule.color, 32);
    if (!localId || !label || !color || !/^#[0-9A-Fa-f]{6}(?:[0-9A-Fa-f]{2})?$/u.test(color)) continue;
    if (!Array.isArray(rule.patterns) || rule.patterns.length < 1 || rule.patterns.length > 16) continue;
    const patterns = rule.patterns.filter((pattern): pattern is string => (
      typeof pattern === 'string' && isSafePluginDecorationPattern(pattern)
    ));
    if (patterns.length !== rule.patterns.length) continue;
    const id = `${providerId}:${localId}`;
    if (seen.has(id)) continue;
    seen.add(id);
    result.push({
      id,
      label,
      patterns: Object.freeze([...patterns]),
      color,
      enabled: true,
      providerId,
    });
  }
  return freezeArray(result);
}

export function mergePluginDecorationRules(
  groups: readonly (readonly PluginTerminalDecorationRule[])[],
  maximum = MAX_PLUGIN_DECORATION_RULES,
): readonly PluginTerminalDecorationRule[] {
  const result: PluginTerminalDecorationRule[] = [];
  const seen = new Set<string>();
  for (const group of groups) {
    for (const rule of group) {
      if (seen.has(rule.id)) continue;
      seen.add(rule.id);
      result.push(rule);
      if (result.length >= maximum) return freezeArray(result);
    }
  }
  return freezeArray(result);
}
