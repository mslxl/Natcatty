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
  const resetAttempts: string[] = [];

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false, error: "incorrect" as const }),
          onResetAppLock: async (currentPassword) => {
            resetAttempts.push(currentPassword);
          },
        }),
      ),
    );
    await flushEffects();

    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    assert.ok(input);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "secret");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));

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

    assert.deepEqual(resetAttempts, ["secret"]);
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
  const resetAttempts: string[] = [];

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
          onResetAppLock: async (currentPassword) => {
            resetAttempts.push(currentPassword);
          },
        }),
      ),
    );
    await flushEffects();

    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    assert.ok(input);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "secret");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));

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
    assert.deepEqual(resetAttempts, []);

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
    assert.deepEqual(resetAttempts, ["secret"]);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay renders platform-specific system unlock button", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  let systemUnlockCount = 0;

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false, error: "incorrect" as const }),
          systemUnlockStatus: {
            supported: true,
            available: true,
            enabled: true,
            platform: "win32",
            label: "Windows Hello",
            reason: null,
          },
          onSystemUnlock: async () => {
            systemUnlockCount += 1;
            return { ok: true as const };
          },
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();

    const button = [...dom.document.querySelectorAll("button")]
      .find((candidate) => /Unlock with Windows Hello/i.test(candidate.textContent ?? ""));
    assert.ok(button);
    await dispatchDomEvent(button, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();
    assert.equal(systemUnlockCount, 2);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay localizes the system unlock button label", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);

  try {
    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "zh-CN" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false, error: "incorrect" as const }),
          systemUnlockStatus: {
            supported: true,
            available: true,
            enabled: true,
            platform: "darwin",
            label: "Touch ID",
            reason: null,
          },
          onSystemUnlock: async () => ({ ok: true as const }),
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();

    assert.match(dom.document.body.textContent ?? "", /使用 Touch ID 解锁/i);
    assert.doesNotMatch(dom.document.body.textContent ?? "", /Unlock with Touch ID/i);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay automatically requests system unlock once when locked", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  let systemUnlockCount = 0;

  try {
    const props = {
      locked: true,
      reason: "manual" as const,
      onUnlock: async () => ({ ok: false as const, error: "incorrect" as const }),
      systemUnlockStatus: {
        supported: true,
        available: true,
        enabled: true,
        platform: "darwin" as const,
        label: "Touch ID" as const,
        reason: null,
      },
      onSystemUnlock: async () => {
        systemUnlockCount += 1;
        return { ok: true as const };
      },
      onResetAppLock: async () => {},
    };

    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, props),
      ),
    );
    await flushEffects();
    await flushEffects();

    assert.equal(systemUnlockCount, 1);

    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, props),
      ),
    );
    await flushEffects();
    await flushEffects();

    assert.equal(systemUnlockCount, 1);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay waits until the document is visible before auto system unlock", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  let systemUnlockCount = 0;

  try {
    Object.defineProperty(dom.document, "visibilityState", {
      configurable: true,
      value: "hidden",
    });

    await renderer.render(
      React.createElement(
        I18nProvider,
        { locale: "en" },
        React.createElement(AppLockOverlay, {
          locked: true,
          reason: "manual",
          onUnlock: async () => ({ ok: false as const, error: "incorrect" as const }),
          systemUnlockStatus: {
            supported: true,
            available: true,
            enabled: true,
            platform: "darwin" as const,
            label: "Touch ID" as const,
            reason: null,
          },
          onSystemUnlock: async () => {
            systemUnlockCount += 1;
            return { ok: true as const };
          },
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();
    await flushEffects();

    assert.equal(systemUnlockCount, 0);

    Object.defineProperty(dom.document, "visibilityState", {
      configurable: true,
      value: "visible",
    });
    await dispatchDomEvent(dom.document, new dom.window.Event("visibilitychange"));
    await flushEffects();
    await flushEffects();

    assert.equal(systemUnlockCount, 1);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay hides system unlock when unavailable", async () => {
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
          systemUnlockStatus: {
            supported: true,
            available: false,
            enabled: true,
            platform: "darwin",
            label: "Touch ID",
            reason: "unavailable",
          },
          onSystemUnlock: async () => ({ ok: true as const }),
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();

    assert.doesNotMatch(dom.document.body.textContent ?? "", /Unlock with Touch ID/i);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});

test("AppLockOverlay keeps password fallback after system unlock failure", async () => {
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
            return { ok: true as const };
          },
          systemUnlockStatus: {
            supported: true,
            available: true,
            enabled: true,
            platform: "darwin",
            label: "Touch ID",
            reason: null,
          },
          onSystemUnlock: async () => ({ ok: false as const, error: "cancelled" as const }),
          onResetAppLock: async () => {},
        }),
      ),
    );
    await flushEffects();

    const systemButton = [...dom.document.querySelectorAll("button")]
      .find((candidate) => /Unlock with Touch ID/i.test(candidate.textContent ?? ""));
    assert.ok(systemButton);
    await dispatchDomEvent(systemButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();
    assert.match(dom.document.body.textContent ?? "", /System unlock was not completed/i);

    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    const form = dom.document.querySelector("form");
    assert.ok(input);
    assert.ok(form);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "secret");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await dispatchDomEvent(form, new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await flushEffects();

    assert.deepEqual(unlockAttempts, ["secret"]);
  } finally {
    await renderer.unmount();
    dom.cleanup();
  }
});
