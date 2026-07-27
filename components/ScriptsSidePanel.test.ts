import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { buildScriptsSidePanelRows } from "./ScriptsSidePanel.tsx";
import type { Snippet } from "../types";

const snippet = (overrides: Partial<Snippet>): Snippet => ({
  id: overrides.id ?? "snippet",
  label: overrides.label ?? "Snippet",
  command: overrides.command ?? "echo ok",
  package: overrides.package ?? "",
  order: overrides.order,
});

const source = readFileSync(new URL("./ScriptsSidePanel.tsx", import.meta.url), "utf8");

test("scripts side panel rows keep manual snippet order inside a package", () => {
  const rows = buildScriptsSidePanelRows({
    snippets: [
      snippet({ id: "alpha", label: "Alpha", package: "ops", order: 3000 }),
      snippet({ id: "zulu", label: "Zulu", package: "ops", order: 1000 }),
      snippet({ id: "beta", label: "Beta", package: "ops", order: 2000 }),
    ],
    packages: ["ops"],
    expandedPaths: new Set(["ops"]),
  });

  assert.deepEqual(
    rows.filter((row) => row.type === "snippet").map((row) => row.id),
    ["zulu", "beta", "alpha"],
  );
});

test("scripts side panel active tabs pair the accent background with its foreground", () => {
  assert.equal(source.match(/bg-accent text-accent-foreground/g)?.length, 2);
});

test("scripts side panel offers run-on-all-tabs for every snippet, not only automation scripts", () => {
  // Guard against re-introducing the isScriptSnippet gate on workspace run actions.
  assert.doesNotMatch(
    source,
    /onRunParallel=\{isScriptSnippet\(item\.row\.snippet\) && onRunScriptOnWorkspace/,
  );
  assert.match(source, /onRunParallel=\{onRunScriptOnWorkspace/);
  assert.match(source, /snippets\.action\.newPackage/);
  assert.match(source, /openPackageDialog/);
});

test("scripts side panel add control is a split button with secondary create actions", () => {
  assert.match(source, /ChevronDown/);
  assert.match(source, /snippets\.action\.newScript/);
  assert.match(source, /snippets\.action\.newSnippet/);
});
