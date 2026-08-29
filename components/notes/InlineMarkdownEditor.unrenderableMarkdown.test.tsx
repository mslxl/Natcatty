import assert from "node:assert/strict";
import test from "node:test";
import { JSDOM } from "jsdom";

import { runWithAct } from "../test-support/renderReactDom.tsx";

/**
 * Regression tests for the notes rich editor: markdown that MDX cannot parse
 * (e.g. unbalanced angle tags such as `<host>` in prose) used to render as an
 * empty editor, so an AI-written or imported note looked like it had lost its
 * content. The editor must fall back to the raw markdown source view instead.
 */

const NOTE_MARKDOWN_MDX_CANNOT_PARSE = [
  "# SlurmDB Agent Skill",
  "",
  "## Overview",
  "",
  "Run the exporter with mysql -h <host> -u <user> -P 3306.",
  "",
  "The content after the angle tags must stay visible.",
].join("\n");

const PLAIN_NOTE_MARKDOWN = ["# Steps", "", "Promote the replica, then restart the agent."].join("\n");

const setupDom = () => {
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
    pretendToBeVisual: true,
    url: "http://localhost",
  });
  const window = dom.window;
  const previousGlobals = new Map<string, PropertyDescriptor | undefined>();
  const installGlobal = (key: string, value: unknown) => {
    previousGlobals.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  };

  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }

  for (const [key, value] of Object.entries({
    window,
    document: window.document,
    navigator: window.navigator,
    HTMLElement: window.HTMLElement,
    HTMLInputElement: window.HTMLInputElement,
    HTMLTextAreaElement: window.HTMLTextAreaElement,
    HTMLSelectElement: window.HTMLSelectElement,
    Element: window.Element,
    SVGElement: window.SVGElement,
    Node: window.Node,
    NodeFilter: window.NodeFilter,
    MutationObserver: window.MutationObserver,
    CustomEvent: window.CustomEvent,
    DOMRect: window.DOMRect,
    Event: window.Event,
    KeyboardEvent: window.KeyboardEvent,
    MouseEvent: window.MouseEvent,
    getComputedStyle: window.getComputedStyle.bind(window),
    requestAnimationFrame: window.requestAnimationFrame.bind(window),
    cancelAnimationFrame: window.cancelAnimationFrame.bind(window),
    ResizeObserver: ResizeObserverStub,
    IS_REACT_ACT_ENVIRONMENT: true,
  })) {
    installGlobal(key, value);
  }

  return {
    window,
    cleanup() {
      for (const [key, descriptor] of previousGlobals) {
        if (descriptor) Object.defineProperty(globalThis, key, descriptor);
        else delete (globalThis as Record<string, unknown>)[key];
      }
      dom.window.close();
    },
  };
};

type DomHarness = ReturnType<typeof setupDom>;

type ActiveFormats = {
  bold: boolean;
  italic: boolean;
  underline: boolean;
  strikethrough: boolean;
  code: boolean;
};

type RenderEditorProps = {
  value: string;
  editorMode?: "edit" | "preview" | "source";
  onActiveFormatsChange?: (formats: ActiveFormats) => void;
};

const renderEditor = async (
  window: DomHarness["window"],
  props: RenderEditorProps,
) => {
  const { act } = await import("react");
  const { createRoot } = await import("react-dom/client");
  const { I18nProvider } = await import("../../application/i18n/I18nProvider.tsx");
  const { InlineMarkdownEditor } = await import("./InlineMarkdownEditor.tsx");

  const rootNode = window.document.getElementById("root");
  assert.ok(rootNode);
  const root = createRoot(rootNode);
  const changes: string[] = [];
  const render = async (nextProps: RenderEditorProps) => act(async () => {
    root.render(
      <I18nProvider locale="en">
        <InlineMarkdownEditor
          noteId="note-1"
          value={nextProps.value}
          placeholder="Write Markdown notes here..."
          editorMode={nextProps.editorMode ?? "edit"}
          onChange={(next) => changes.push(next)}
          onActiveFormatsChange={nextProps.onActiveFormatsChange}
          hosts={[]}
        />
      </I18nProvider>,
    );
  });
  await render(props);
  // Let the deferred MDX import (and its parse-error reporting) settle.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
  return {
    rootNode,
    changes,
    rerender: render,
    async unmount() {
      await act(async () => {
        root.unmount();
      });
    },
  };
};

const querySourceFallback = (rootNode: HTMLElement) =>
  rootNode.querySelector<HTMLTextAreaElement>("[data-note-markdown-source-fallback] textarea");

const setTextareaValue = (window: DomHarness["window"], textarea: HTMLTextAreaElement, nextValue: string) => {
  const setValue = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")?.set;
  assert.ok(setValue);
  setValue.call(textarea, nextValue);
  textarea.dispatchEvent(new window.Event("input", { bubbles: true }));
};

test("unrenderable markdown stays visible via the raw source fallback", async () => {
  const { window, cleanup } = setupDom();
  try {
    const { rootNode, unmount } = await renderEditor(window, { value: NOTE_MARKDOWN_MDX_CANNOT_PARSE });

    const fallback = querySourceFallback(rootNode);
    assert.ok(fallback, "expected the raw markdown fallback to be rendered");
    assert.equal(fallback.value, NOTE_MARKDOWN_MDX_CANNOT_PARSE);

    const notice = rootNode.querySelector("[data-note-markdown-source-notice]");
    assert.ok(notice, "expected an explanatory notice next to the fallback");

    // The rich editor must not sit next to the fallback showing a blank page.
    assert.equal(rootNode.querySelectorAll("[contenteditable]").length, 0);
    await unmount();
  } finally {
    cleanup();
  }
});

test("plain markdown still renders in the rich editor", async () => {
  const { window, cleanup } = setupDom();
  try {
    const { rootNode, unmount } = await renderEditor(window, { value: PLAIN_NOTE_MARKDOWN });

    assert.equal(querySourceFallback(rootNode), null);
    const editable = rootNode.querySelector<HTMLElement>("[contenteditable]");
    assert.ok(editable, "expected the rich editor to stay mounted");
    assert.match(editable.textContent || "", /Promote the replica/);
    await unmount();
  } finally {
    cleanup();
  }
});

test("unrenderable markdown in preview mode falls back to a read-only source view", async () => {
  const { window, cleanup } = setupDom();
  try {
    const { rootNode, changes, unmount } = await renderEditor(window, {
      value: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
      editorMode: "preview",
    });

    const fallback = querySourceFallback(rootNode);
    assert.ok(fallback, "expected the raw markdown fallback in preview mode");
    assert.equal(fallback.value, NOTE_MARKDOWN_MDX_CANNOT_PARSE);
    assert.equal(fallback.readOnly, true);

    // Read-only must also block the custom Tab insertion and history handling.
    await runWithAct(async () => {
      fallback.dispatchEvent(new window.KeyboardEvent("keydown", {
        key: "Tab",
        bubbles: true,
        cancelable: true,
      }));
    });
    assert.deepEqual(changes, [], "read-only fallback must not mutate the note");
    await unmount();
  } finally {
    cleanup();
  }
});

test("retry imports the latest fallback draft, not the stale prop value", async () => {
  const { window, cleanup } = setupDom();
  try {
    // The host keeps the original (invalid) `value` prop, like the 300 ms
    // draft debounce in NotesManager does while the user edits the fallback.
    const { rootNode, changes, unmount } = await renderEditor(window, { value: NOTE_MARKDOWN_MDX_CANNOT_PARSE });

    const fallback = querySourceFallback(rootNode);
    assert.ok(fallback);

    const fixedMarkdown = NOTE_MARKDOWN_MDX_CANNOT_PARSE.replace("mysql -h <host> -u <user>", "mysql with host and user");
    await runWithAct(async () => {
      setTextareaValue(window, fallback, fixedMarkdown);
    });
    assert.deepEqual(changes, [fixedMarkdown]);

    const retry = rootNode.querySelector<HTMLButtonElement>("[data-note-markdown-source-retry]");
    assert.ok(retry, "expected a retry action on the fallback notice");
    await runWithAct(async () => {
      retry.click();
    });
    await runWithAct(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });

    const editable = rootNode.querySelector<HTMLElement>("[contenteditable]");
    assert.ok(editable, "expected the rich editor back after the retry");
    assert.match(editable.textContent || "", /The content after the angle tags/);
    assert.equal(querySourceFallback(rootNode), null, "the fallback must be gone after a successful retry");
    await unmount();
  } finally {
    cleanup();
  }
});

test("retry rebinds active-format listeners after the rich editor remounts", async () => {
  const { window, cleanup } = setupDom();
  try {
    const formatChanges: ActiveFormats[] = [];
    const onActiveFormatsChange = (formats: ActiveFormats) => {
      formatChanges.push(formats);
    };
    const { rootNode, changes, unmount } = await renderEditor(window, {
      value: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
      onActiveFormatsChange,
    });

    assert.ok(querySourceFallback(rootNode), "expected the raw markdown fallback");
    assert.equal(rootNode.querySelectorAll("[contenteditable]").length, 0);
    const callsWhileFallback = formatChanges.length;

    const fallback = querySourceFallback(rootNode);
    assert.ok(fallback);
    const fixedMarkdown = NOTE_MARKDOWN_MDX_CANNOT_PARSE.replace(
      "mysql -h <host> -u <user>",
      "mysql with host and user",
    );
    await runWithAct(async () => {
      setTextareaValue(window, fallback, fixedMarkdown);
    });
    assert.deepEqual(changes, [fixedMarkdown]);

    const retry = rootNode.querySelector<HTMLButtonElement>("[data-note-markdown-source-retry]");
    assert.ok(retry, "expected a retry action on the fallback notice");
    await runWithAct(async () => {
      retry.click();
    });
    await runWithAct(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });

    const editable = rootNode.querySelector<HTMLElement>("[contenteditable]");
    assert.ok(editable, "expected the rich editor back after the retry");
    assert.equal(querySourceFallback(rootNode), null, "the fallback must be gone after a successful retry");
    assert.ok(
      formatChanges.length > callsWhileFallback,
      "expected onActiveFormatsChange after the rich editor remounted",
    );
    await unmount();
  } finally {
    cleanup();
  }
});

test("same-note external markdown clears an active fallback", async () => {
  const { window, cleanup } = setupDom();
  try {
    const { rootNode, rerender, unmount } = await renderEditor(window, {
      value: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
    });
    assert.ok(querySourceFallback(rootNode));

    await rerender({ value: PLAIN_NOTE_MARKDOWN });
    await runWithAct(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });

    assert.equal(querySourceFallback(rootNode), null);
    assert.match(rootNode.querySelector<HTMLElement>("[contenteditable]")?.textContent || "", /Promote the replica/);
    await unmount();
  } finally {
    cleanup();
  }
});

test("leading and trailing whitespace does not hide an unrenderable note", async () => {
  const { window, cleanup } = setupDom();
  try {
    const { rootNode, unmount } = await renderEditor(window, {
      value: `\n${NOTE_MARKDOWN_MDX_CANNOT_PARSE}\n`,
    });

    const fallback = querySourceFallback(rootNode);
    assert.ok(fallback, "expected padded unrenderable markdown to use the source fallback");
    assert.equal(fallback.value, `\n${NOTE_MARKDOWN_MDX_CANNOT_PARSE}\n`);
    await unmount();
  } finally {
    cleanup();
  }
});

test("a stale parse error is ignored when the same note has newer markdown", async () => {
  const { shouldApplyMdxParseFailure } = await import("./InlineMarkdownEditor.tsx");

  assert.equal(shouldApplyMdxParseFailure({
    currentNoteId: "note-1",
    failedNoteId: "note-1",
    currentMarkdown: PLAIN_NOTE_MARKDOWN,
    failedMarkdown: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
  }), false);
  assert.equal(shouldApplyMdxParseFailure({
    currentNoteId: "note-1",
    failedNoteId: "note-1",
    currentMarkdown: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
    failedMarkdown: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
  }), true);
  assert.equal(shouldApplyMdxParseFailure({
    currentNoteId: "note-1",
    failedNoteId: "note-1",
    currentMarkdown: `\n${NOTE_MARKDOWN_MDX_CANNOT_PARSE}\n`,
    failedMarkdown: NOTE_MARKDOWN_MDX_CANNOT_PARSE,
  }), true);
});
