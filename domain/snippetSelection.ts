import type { Host, Snippet } from './models';
import { deleteSnippetFromVault } from './snippetAgentOps.ts';

export function deleteSelectedSnippetsFromVault(
  snippets: Snippet[],
  hosts: Host[],
  selectedSnippetIds: ReadonlySet<string>,
): { snippets: Snippet[]; hosts: Host[] } {
  let nextSnippets = [...snippets];
  let nextHosts = [...hosts];

  for (const snippet of snippets) {
    if (!snippet.id || !selectedSnippetIds.has(snippet.id)) continue;
    const result = deleteSnippetFromVault(nextSnippets, nextHosts, snippet.id);
    if ('error' in result) continue;
    nextSnippets = result.snippets;
    nextHosts = result.hosts;
  }

  return { snippets: nextSnippets, hosts: nextHosts };
}
