import {
  mergePluginCompletionItems,
  normalizePluginCompletionResult,
} from '../../../domain/pluginTerminalProviders';
import type { PluginTerminalProviderRegistry } from '../../../application/state/pluginTerminalProviderRegistry';
import {
  getCompletions,
  type CompletionSuggestion,
} from './completionEngine';
import type { AutocompleteCwdSource } from './terminalAutocompleteLayout';
import type { Snippet } from '../../../domain/models';

export interface TerminalCompletionProviderRequest {
  input: string;
  session: NetcattyTerminalSessionSnapshot;
  hostOs: 'linux' | 'windows' | 'macos';
  cwdSource?: AutocompleteCwdSource;
  snippets?: Snippet[];
  maximum: number;
}

export async function provideTerminalCompletions(
  registry: PluginTerminalProviderRegistry | null,
  request: TerminalCompletionProviderRequest,
): Promise<CompletionSuggestion[]> {
  const builtInPromise = getCompletions(request.input, {
    hostId: request.session.hostId,
    os: request.hostOs,
    maxResults: request.maximum,
    sessionId: request.session.sessionId,
    protocol: request.session.protocol,
    cwd: request.session.cwd,
    cwdSource: request.cwdSource,
    snippets: request.snippets,
  });
  const pluginPromise = registry?.request({
    kind: 'terminal.completion',
    operation: 'provideCompletions',
    session: request.session,
    payload: {
      input: request.input,
      cursor: request.input.length,
      hostOs: request.hostOs,
      cwdSource: request.cwdSource ?? null,
      maximum: request.maximum,
    },
    deadlineMs: 750,
  }).catch(() => ({ requestId: '', stale: false, results: [] as const }))
    ?? Promise.resolve({ requestId: '', stale: false, results: [] as const });
  const [builtIn, pluginResponse] = await Promise.all([builtInPromise, pluginPromise]);
  if (pluginResponse.stale) return builtIn;
  const pluginGroups = pluginResponse.results.map((result) => result.status === 'ok'
    ? normalizePluginCompletionResult(result.providerId, result.result)
    : Object.freeze([]));
  const pluginItems = mergePluginCompletionItems(pluginGroups, request.maximum);
  const combined: CompletionSuggestion[] = [
    ...builtIn,
    ...pluginItems.map((item) => ({
      text: item.text,
      displayText: item.displayText,
      ...(item.description === undefined ? {} : { description: item.description }),
      source: 'plugin' as const,
      score: item.score,
      providerId: item.providerId,
    })),
  ];
  combined.sort((left, right) => right.score - left.score || left.text.localeCompare(right.text));
  const seen = new Set<string>();
  return combined.filter((item) => {
    if (seen.has(item.text)) return false;
    seen.add(item.text);
    return true;
  }).slice(0, request.maximum);
}
