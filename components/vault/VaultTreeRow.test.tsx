import test from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, create, type ReactTestRenderer } from "react-test-renderer";

import {
  VaultTreeGroupRow,
  VaultTreeInlineRenameInput,
  VaultTreeItemRow,
} from "./VaultTreeRow.tsx";

test("VaultTreeGroupRow exposes shared selected and expanded tree row state", () => {
  const markup = renderToStaticMarkup(
    <VaultTreeGroupRow
      name="Production"
      depth={1}
      expanded={true}
      selected={true}
      count={3}
      onClick={() => undefined}
      onToggle={() => undefined}
    />,
  );

  assert.match(markup, /data-vault-tree-row="group"/);
  assert.match(markup, /data-selected="true"/);
  assert.match(markup, /data-expanded="true"/);
  assert.match(markup, /Production/);
  assert.match(markup, /3/);
});

test("VaultTreeGroupRow can render an action beside the group label", () => {
  const markup = renderToStaticMarkup(
    <VaultTreeGroupRow
      name="Production"
      depth={1}
      count={3}
      labelActions={<button data-label-action="edit">Edit</button>}
      actions={<span data-row-action="count">Row action</span>}
    />,
  );

  const labelIndex = markup.indexOf("Production");
  const labelActionIndex = markup.indexOf('data-label-action="edit"');
  const countIndex = markup.indexOf(">3<", labelActionIndex);
  const rowActionIndex = markup.indexOf('data-row-action="count"', countIndex);

  assert.ok(labelIndex >= 0);
  assert.ok(labelActionIndex > labelIndex);
  assert.ok(countIndex > labelActionIndex);
  assert.ok(rowActionIndex > countIndex);
});


test("VaultTreeItemRow exposes shared selected item state", () => {
  const markup = renderToStaticMarkup(
    <VaultTreeItemRow
      label="Failover checklist"
      depth={2}
      selected={true}
      onClick={() => undefined}
    />,
  );

  assert.match(markup, /data-vault-tree-row="item"/);
  assert.match(markup, /data-selected="true"/);
  assert.match(markup, /Failover checklist/);
});

test("VaultTree labels use CJK-safe line-height under truncate", () => {
  const groupMarkup = renderToStaticMarkup(
    <VaultTreeGroupRow name="服务器配置" depth={0} count={1} />,
  );
  const itemMarkup = renderToStaticMarkup(
    <VaultTreeItemRow label="机器安全检查报告" depth={0} />,
  );

  // leading-none clips CJK (PingFang etc.) when truncate applies overflow:hidden.
  assert.match(groupMarkup, /leading-5/);
  assert.doesNotMatch(groupMarkup, /leading-none/);
  assert.match(itemMarkup, /leading-5/);
  assert.doesNotMatch(itemMarkup, /leading-none/);
});

test("VaultTreeInlineRenameInput uses shared inline edit marker", () => {
  const markup = renderToStaticMarkup(
    <VaultTreeInlineRenameInput
      initialName="Ops"
      onCommit={() => undefined}
      onCancel={() => undefined}
    />,
  );

  assert.match(markup, /data-vault-tree-inline-edit="true"/);
  assert.match(markup, /value="Ops"/);
});

test("VaultTreeInlineRenameInput can retry after an asynchronous commit failure", async () => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean;
  };
  const previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT;
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
  let renderer: ReactTestRenderer | null = null;
  let attempts = 0;

  try {
    await act(async () => {
      renderer = create(
        <VaultTreeInlineRenameInput
          initialName="Ops"
          onCommit={async () => {
            attempts += 1;
            return attempts > 1;
          }}
          onCancel={() => undefined}
        />,
      );
    });
    const input = renderer!.root.findByType("input");
    const pressEnter = async () => {
      input.props.onKeyDown({
        key: "Enter",
        preventDefault: () => undefined,
        stopPropagation: () => undefined,
      });
      await Promise.resolve();
    };
    await act(pressEnter);
    await act(pressEnter);
    assert.equal(attempts, 2);
  } finally {
    await act(async () => {
      renderer?.unmount();
    });
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
  }
});
