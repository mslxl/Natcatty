import assert from "node:assert/strict";
import test from "node:test";
import React from "react";

import { useAppLockBridge } from "../application/state/useAppLockBridge.ts";
import { useAppLockState } from "../application/state/useAppLockState.ts";
import { createAppLockGate } from "./AppLockGate.tsx";
import { createAppLockBridgeHarness } from "./test-support/createAppLockBridgeHarness.ts";
import {
  createDomRenderer,
  dispatchDomEvent,
  flushEffects,
  installDomEnvironment,
  runWithAct,
} from "./test-support/renderReactDom.tsx";

test("startup-locked gate reveals children after successful unlock", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "startup",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "unlocked-content" }, "Unlocked"),
      }),
    );
    await flushEffects();

    assert.equal(dom.document.getElementById("unlocked-content"), null);

    const form = dom.document.querySelector("form");
    assert.ok(form);
    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    assert.ok(input);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "secret");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await dispatchDomEvent(form, new dom.window.Event("submit", { bubbles: true, cancelable: true }));

    await flushEffects();
    await flushEffects();

    assert.deepEqual(bridgeHarness.getUnlockAttempts(), ["secret"]);
    assert.equal(bridgeHarness.getRuntimeState().locked, false);
    assert.equal(dom.document.getElementById("unlocked-content")?.textContent, "Unlocked");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("startup-locked gate reveals children after successful system unlock", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "startup",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
    systemUnlockStatus: {
      supported: true,
      available: true,
      enabled: true,
      platform: "darwin",
      label: "Touch ID",
      reason: null,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        systemUnlockEnabled: true,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "unlocked-content" }, "Unlocked"),
      }),
    );
    await flushEffects();
    await flushEffects();

    const button = [...dom.document.querySelectorAll("button")]
      .find((candidate) => /Touch ID/i.test(candidate.textContent ?? ""));
    assert.ok(button);
    await dispatchDomEvent(button, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();

    assert.equal(bridgeHarness.getSystemUnlockCount(), 1);
    assert.equal(bridgeHarness.getRuntimeState().locked, false);
    assert.equal(dom.document.getElementById("unlocked-content")?.textContent, "Unlocked");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("startup-locked gate reveals children after hidden app lock reset", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "startup",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "reset-unlocked-content" }, "Unlocked"),
      }),
    );
    await flushEffects();

    assert.equal(dom.document.getElementById("reset-unlocked-content"), null);

    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    assert.ok(input);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);
    setInputValue.call(input, "secret");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));

    const logoButton = dom.document.querySelector("[data-testid='app-lock-logo-easter-egg']");
    assert.ok(logoButton);
    for (let index = 0; index < 5; index += 1) {
      await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
      await flushEffects();
    }

    const resetButton = [...dom.document.querySelectorAll("button")]
      .find((button) => /Reset App Lock/i.test(button.textContent ?? ""));
    assert.ok(resetButton);
    await dispatchDomEvent(resetButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();

    assert.equal(bridgeHarness.getResetCount(), 1);
    assert.deepEqual(bridgeHarness.getResetAttempts(), ["secret"]);
    assert.equal(bridgeHarness.getRuntimeState().locked, false);
    assert.equal(dom.document.getElementById("reset-unlocked-content")?.textContent, "Unlocked");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("hidden app lock reset stays locked when reset bridge is unavailable", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "startup",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  const bridgeWithoutReset = { ...bridgeHarness.bridge };
  delete bridgeWithoutReset.requestAppLockReset;
  dom.window.netcatty = bridgeWithoutReset;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "reset-missing-content" }, "Unlocked"),
      }),
    );
    await flushEffects();

    const logoButton = dom.document.querySelector("[data-testid='app-lock-logo-easter-egg']");
    assert.ok(logoButton);
    for (let index = 0; index < 5; index += 1) {
      await dispatchDomEvent(logoButton, new dom.window.MouseEvent("click", { bubbles: true }));
      await flushEffects();
    }

    const resetButton = [...dom.document.querySelectorAll("button")]
      .find((button) => /Reset App Lock/i.test(button.textContent ?? ""));
    assert.ok(resetButton);
    await dispatchDomEvent(resetButton, new dom.window.MouseEvent("click", { bubbles: true }));
    await flushEffects();
    await flushEffects();

    assert.match(dom.document.body.textContent ?? "", /Could not reset App Lock/i);
    assert.equal(bridgeHarness.getRuntimeState().locked, true);
    assert.equal(dom.document.getElementById("reset-missing-content"), null);
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("mounted locked gate uses the latest unlock password without remounting", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "manual",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
    unlockPassword: "alpha",
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "latest-password-content" }, "Unlocked"),
      }),
    );
    await flushEffects();

    bridgeHarness.setUnlockPassword("bravo");

    const form = dom.document.querySelector("form");
    assert.ok(form);
    const input = dom.document.getElementById("app-lock-password") as HTMLInputElement | null;
    assert.ok(input);
    const setInputValue = Object.getOwnPropertyDescriptor(
      dom.window.HTMLInputElement.prototype,
      "value",
    )?.set;
    assert.ok(setInputValue);

    setInputValue.call(input, "alpha");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await dispatchDomEvent(form, new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await flushEffects();

    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 1);
    assert.equal(dom.document.getElementById("latest-password-content")?.textContent, "Unlocked");

    setInputValue.call(input, "bravo");
    await dispatchDomEvent(input, new dom.window.Event("input", { bubbles: true }));
    await dispatchDomEvent(form, new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await flushEffects();
    await flushEffects();

    assert.deepEqual(bridgeHarness.getUnlockAttempts(), ["alpha", "bravo"]);
    assert.equal(dom.document.querySelector('[role="dialog"]'), null);
    assert.equal(dom.document.getElementById("latest-password-content")?.textContent, "Unlocked");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("runtime unlock and relock broadcasts update multiple mounted gates together", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "manual",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(React.Fragment, null,
        React.createElement(AppLockGate, {
          notifyRendererReady: false,
          children: () => React.createElement("div", { id: "gate-a" }, "Gate A"),
        }),
        React.createElement(AppLockGate, {
          notifyRendererReady: false,
          children: () => React.createElement("div", { id: "gate-b" }, "Gate B"),
        }),
      ),
    );
    await flushEffects();

    assert.equal(dom.document.getElementById("gate-a")?.textContent, "Gate A");
    assert.equal(dom.document.getElementById("gate-b")?.textContent, "Gate B");
    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 2);

    await runWithAct(async () => {
      bridgeHarness.setRuntimeState({
        locked: false,
        reason: null,
        lastUnlockedAt: 2_000,
        lastActivityAt: 2_000,
      });
    });
    await flushEffects();

    assert.equal(dom.document.querySelector('[role="dialog"]'), null);

    await runWithAct(async () => {
      bridgeHarness.setRuntimeState({
        locked: true,
        reason: "manual",
        lastLockedAt: 3_000,
      });
    });
    await flushEffects();

    assert.equal(dom.document.getElementById("gate-a")?.textContent, "Gate A");
    assert.equal(dom.document.getElementById("gate-b")?.textContent, "Gate B");
    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 2);
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("focus recovery resync clears stale overlay state after a missed broadcast", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "manual",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "stale-gate" }, "Unlocked After Resync"),
      }),
    );
    await flushEffects();

    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 1);

    await runWithAct(async () => {
      bridgeHarness.setRuntimeState({
        locked: false,
        reason: null,
        lastUnlockedAt: 2_000,
        lastActivityAt: 2_000,
      }, { notify: false });
    });

    await dispatchDomEvent(dom.window, new dom.window.FocusEvent("focus"));
    await flushEffects();
    await flushEffects();

    assert.equal(dom.document.querySelector('[role="dialog"]'), null);
    assert.equal(dom.document.getElementById("stale-gate")?.textContent, "Unlocked After Resync");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("reopen recovery resync clears stale overlay state after a missed broadcast", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "manual",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "reopen-gate" }, "Unlocked After Reopen"),
      }),
    );
    await flushEffects();

    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 1);

    await runWithAct(async () => {
      bridgeHarness.setRuntimeState({
        locked: false,
        reason: null,
        lastUnlockedAt: 2_000,
        lastActivityAt: 2_000,
      }, { notify: false });
      bridgeHarness.emitReopen();
    });
    await flushEffects();
    await flushEffects();

    assert.equal(dom.document.querySelector('[role="dialog"]'), null);
    assert.equal(dom.document.getElementById("reopen-gate")?.textContent, "Unlocked After Reopen");
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});

test("reopen resync does not unlock a gate when runtime is still locked", async () => {
  const dom = installDomEnvironment();
  const renderer = await createDomRenderer(dom.document);
  const bridgeHarness = createAppLockBridgeHarness({
    runtimeState: {
      initialized: true,
      locked: true,
      reason: "startup",
      version: 1,
      lastLockedAt: 1_000,
      lastUnlockedAt: null,
      lastActivityAt: 1_000,
    },
  });
  const AppLockGate = createAppLockGate({
    useSettingsState: () => ({
      uiLanguage: "en",
      appLockSettings: {
        enabled: true,
        timeoutMinutes: 15,
        passwordVerifier: {
          version: 1,
          algorithm: "PBKDF2-SHA256",
          iterations: 210000,
          salt: "AAAAAAAAAAAAAAAAAAAAAA==",
          hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
        },
      },
    }) as ReturnType<typeof import("../application/state/useSettingsState.ts").useSettingsState>,
    useAppLockState,
    useAppLockBridge,
  });

  const previousWindowNetcatty = dom.window.netcatty;
  dom.window.netcatty = bridgeHarness.bridge;

  try {
    await renderer.render(
      React.createElement(AppLockGate, {
        notifyRendererReady: false,
        children: () => React.createElement("div", { id: "startup-locked-child" }, "Should Stay Hidden"),
      }),
    );
    await flushEffects();

    assert.equal(dom.document.getElementById("startup-locked-child"), null);
    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 1);
    const fetchCountBeforeReopen = bridgeHarness.getRuntimeFetchCount();

    await runWithAct(async () => {
      bridgeHarness.emitReopen();
    });
    await flushEffects();
    await flushEffects();

    assert.equal(bridgeHarness.getRuntimeFetchCount(), fetchCountBeforeReopen + 1);
    assert.equal(dom.document.getElementById("startup-locked-child"), null);
    assert.equal(dom.document.querySelectorAll('[role="dialog"]').length, 1);
  } finally {
    dom.window.netcatty = previousWindowNetcatty;
    await renderer.unmount();
    dom.cleanup();
  }
});
