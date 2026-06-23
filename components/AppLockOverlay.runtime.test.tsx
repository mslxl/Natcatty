import assert from "node:assert/strict";
import test from "node:test";
import React from "react";

import { I18nProvider } from "../application/i18n/I18nProvider.tsx";
import { AppLockOverlay } from "./AppLockOverlay.tsx";
import {
  createDomRenderer,
  dispatchDomEvent,
  flushEffects,
  installDomEnvironment,
} from "./test-support/renderReactDom.tsx";

test("AppLockOverlay shows incorrect-password error and clears it after editing", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const unlockAttempts: string[] = [];

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async (password) => {
            unlockAttempts.push(password);
            return { ok: false, error: "incorrect" as const };
          },
        }),
      ),
    );
    await flushEffects();

    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    const form = dom.document.querySelector("form");
    assert.ok(input);
    assert.ok(form);

    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "wrong");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await dispatchDomEvent(form, new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await flushEffects();
    await flushEffects();

    assert.deepEqual(unlockAttempts, ["wrong"]);
    assert.match(dom.document.body.textContent ?? "", /Incorrect lock password/i);

    setInputValue.call(input, "wrong-again");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await flushEffects();

    assert.doesNotMatch(dom.document.body.textContent ?? "", /Incorrect lock password/i);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});
