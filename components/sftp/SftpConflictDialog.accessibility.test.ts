import test from "node:test";
import assert from "node:assert/strict";
import React from "react";

import {
  createDomRenderer,
  dispatchDomEvent,
  flushEffects,
  installDomEnvironment,
} from "../test-support/renderReactDom.tsx";

test("announces folder replacement risk and refocuses Merge for each queued conflict", async (t) => {
  const env = installDomEnvironment();
  const previousMutationObserver = globalThis.MutationObserver;
  const previousNodeFilter = globalThis.NodeFilter;
  const previousHTMLInputElement = globalThis.HTMLInputElement;
  Object.defineProperty(globalThis, "MutationObserver", {
    configurable: true,
    writable: true,
    value: env.window.MutationObserver,
  });
  Object.defineProperty(globalThis, "NodeFilter", {
    configurable: true,
    writable: true,
    value: env.window.NodeFilter,
  });
  Object.defineProperty(globalThis, "HTMLInputElement", {
    configurable: true,
    writable: true,
    value: env.window.HTMLInputElement,
  });
  const { I18nProvider } = await import("../../application/i18n/I18nProvider.tsx");
  const { SftpConflictDialog } = await import("./SftpConflictDialog.tsx");
  const renderer = await createDomRenderer(env.document);
  const resolvedActions: string[] = [];
  t.after(async () => {
    await renderer.unmount();
    await new Promise((resolve) => setTimeout(resolve, 20));
    Object.defineProperty(globalThis, "MutationObserver", {
      configurable: true,
      writable: true,
      value: previousMutationObserver,
    });
    Object.defineProperty(globalThis, "NodeFilter", {
      configurable: true,
      writable: true,
      value: previousNodeFilter,
    });
    Object.defineProperty(globalThis, "HTMLInputElement", {
      configurable: true,
      writable: true,
      value: previousHTMLInputElement,
    });
    env.cleanup();
  });

  const queuedConflicts = [
    {
        transferId: "folder-conflict",
        fileName: "docs",
        sourcePath: "/source/docs",
        targetPath: "/destination/docs",
        isDirectory: true,
        existingType: "directory",
        existingSize: 4096,
        newSize: 4096,
        existingModified: 1,
        newModified: 2,
      },
      {
        transferId: "next-folder-conflict",
        fileName: "photos",
        sourcePath: "/source/photos",
        targetPath: "/destination/photos",
        isDirectory: true,
        existingType: "directory",
        existingSize: 4096,
        newSize: 4096,
        existingModified: 3,
        newModified: 4,
      },
  ] satisfies React.ComponentProps<typeof SftpConflictDialog>["conflicts"];
  const QueueHarness = () => {
    const [conflicts, setConflicts] = React.useState(queuedConflicts);
    return React.createElement(SftpConflictDialog, {
      conflicts,
      onResolve: (_id, action) => {
        resolvedActions.push(action);
        setConflicts((current) => current.slice(1));
      },
      formatFileSize: (size: number) => `${size} B`,
    });
  };

  await renderer.render(React.createElement(
    I18nProvider,
    { locale: "en" },
    React.createElement(QueueHarness),
  ));
  await flushEffects();

  const dialog = env.document.querySelector<HTMLElement>("[role=dialog]");
  assert.ok(dialog, "folder conflict dialog should render");
  const describedBy = dialog.getAttribute("aria-describedby");
  assert.ok(describedBy, "dialog should expose its safety description");
  const describedText = describedBy
    .split(/\s+/)
    .map((id) => env.document.getElementById(id)?.textContent ?? "")
    .join(" ");
  assert.match(describedText, /A folder with the same name already exists/);
  assert.match(describedText, /Replace deletes all destination files and sub-folders/);

  const replaceButton = Array.from(env.document.querySelectorAll("button"))
    .find((button) => button.textContent === "Replace");
  assert.ok(replaceButton, "folder replacement action should render");
  await dispatchDomEvent(replaceButton, new env.window.MouseEvent("click", { bubbles: true }));
  await flushEffects();

  assert.deepEqual(resolvedActions, ["replace"]);
  assert.match(env.document.body.textContent ?? "", /photos/);
  assert.equal(env.document.activeElement?.textContent, "Merge");

  const mergeButton = env.document.activeElement;
  assert.ok(mergeButton, "next conflict should focus Merge");
  await dispatchDomEvent(mergeButton, new env.window.MouseEvent("click", { bubbles: true }));
  await flushEffects();
  assert.deepEqual(resolvedActions, ["replace", "merge"]);
  assert.equal(env.document.querySelector("[role=dialog]"), null);
});
