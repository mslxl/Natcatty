import type { Snippet } from './models';

export function removeSelectedSnippets(
  snippets: readonly Snippet[],
  selectedSnippetIds: ReadonlySet<string>,
): Snippet[] {
  if (selectedSnippetIds.size === 0) return [...snippets];
  return snippets.filter((snippet) => !selectedSnippetIds.has(snippet.id));
}
