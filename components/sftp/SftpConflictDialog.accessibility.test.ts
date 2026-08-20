import test from "node:test";
import assert from "node:assert/strict";
import React from "react";

import { createDomRenderer, flushEffects, installDomEnvironment } from "../test-support/renderReactDom.tsx";

test("associates the folder description and replace warning with the dialog", async (t) => {
  const env = installDomEnvironment();
  const previousMutationObserver = globalThis.MutationObserver;
  Object.defineProperty(globalThis, "MutationObserver", {
    configurable: true,
    writable: true,
    value: env.window.MutationObserver,
  });
  const { I18nProvider } = await import("../../application/i18n/I18nProvider.tsx");
  const { SftpConflictDialog } = await import("./SftpConflictDialog.tsx");
  const renderer = await createDomRenderer(env.document);
  t.after(async () => {
    await renderer.unmount();
    await new Promise((resolve) => setTimeout(resolve, 20));
    Object.defineProperty(globalThis, "MutationObserver", {
      configurable: true,
      writable: true,
      value: previousMutationObserver,
    });
    env.cleanup();
  });

  await renderer.render(React.createElement(
    I18nProvider,
    { locale: "en" },
    React.createElement(SftpConflictDialog, {
      conflicts: [{
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
      }],
      onResolve: () => {},
      formatFileSize: (size: number) => `${size} B`,
    }),
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
});
