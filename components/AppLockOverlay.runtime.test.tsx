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
          onResetAppLock: async () => {},
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

test("AppLockOverlay reveals reset action after clicking Netcatty logo five times", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  let resetCount = 0;

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false, error: "incorrect" as const }),
          onResetAppLock: async () => {
            resetCount += 1;
          },
        }),
      ),
    );
    await flushEffects();

    assert.doesNotMatch(dom.document.body.textContent ?? "", /Reset App Lock/i);
    const logoButton = dom.document.querySelector("[data-testid='app-lock-logo-easter-egg']");
    assert.ok(logoButton);

    for (let index = 0; index < 5; index += 1) {
      await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
      await flushEffects();
    }

    assert.match(dom.document.body.textContent ?? "", /Reset App Lock/i);

    const resetButton = [...dom.document.querySelectorAll("button")]
      .find((button) => /Reset App Lock/i.test(button.textContent ?? ""));
    assert.ok(resetButton);
    await dispatchDomEvent(resetButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();

    assert.equal(resetCount, 1);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay only reveals reset after five quick logo clicks", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false, error: "incorrect" as const }),
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();

    const logoButton = dom.document.querySelector("[data-testid='app-lock-logo-easter-egg']");
    assert.ok(logoButton);
    await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await new Promise((resolve) => dom.window.setTimeout(resolve, 1600));
    await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();

    assert.doesNotMatch(dom.document.body.textContent ?? "", /Reset App Lock/i);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay reset controls do not submit the unlock form", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  let unlockCount = 0;
  let resetCount = 0;

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => {
            unlockCount += 1;
            return { ok: false, error: "incorrect" as const };
          },
          onResetAppLock: async () => {
            resetCount += 1;
          },
        }),
      ),
    );
    await flushEffects();

    assert.doesNotMatch(dom.document.body.textContent ?? "", /forgot password/i);
    const logoButton = dom.document.querySelector("[data-testid='app-lock-logo-easter-egg']");
    assert.ok(logoButton);
    for (let index = 0; index < 5; index += 1) {
      await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    }
    await flushEffects();

    const cancelButton = [...dom.document.querySelectorAll("button")]
      .find((button) => /Cancel/i.test(button.textContent ?? ""));
    assert.ok(cancelButton);
    await dispatchDomEvent(cancelButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    assert.equal(unlockCount, 0);
    assert.equal(resetCount, 0);

    for (let index = 0; index < 5; index += 1) {
      await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
    }
    await flushEffects();
    const resetButton = [...dom.document.querySelectorAll("button")]
      .find((button) => /Reset App Lock/i.test(button.textContent ?? ""));
    assert.ok(resetButton);
    await dispatchDomEvent(resetButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();

    assert.equal(unlockCount, 0);
    assert.equal(resetCount, 1);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});
