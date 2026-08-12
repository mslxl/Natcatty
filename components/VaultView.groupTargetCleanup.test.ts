import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./VaultView.tsx", import.meta.url), "utf8");

test("deferred group deletion cleans target paths from the latest snippet snapshot", () => {
  assert.match(
    source,
    /handleDeletedGroupPaths[\s\S]*onUpdateSnippets\(\(currentSnippets\) => \([\s\S]*removeSnippetTargetGroupPaths\(currentSnippets, selectedRoots\)/,
  );
  assert.doesNotMatch(
    source,
    /handleDeletedGroupPaths[\s\S]*removeSnippetTargetGroupPaths\(snippets, selectedRoots\)/,
  );
});

test("group rename and move remap the latest snippet snapshot", () => {
  const functionalRemaps = source.match(
    /onUpdateSnippets\(\(currentSnippets\) => \(\s*remapSnippetTargetGroupPaths\(currentSnippets,/g,
  );
  assert.equal(functionalRemaps?.length, 3);
  assert.doesNotMatch(
    source,
    /remapSnippetTargetGroupPaths\(snippets, (?:renameTargetPath|oldPath|sourcePath),/,
  );
});
