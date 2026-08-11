import type { KeyBinding } from "../../domain/models";
import { parseKeyCombo } from "../../domain/models";

const TERMINAL_KEYBOARD_TARGET_SELECTOR =
  ".xterm, .xterm-helper-textarea, .xterm-screen, .xterm-viewport";

export type TerminalKeyboardShortcut = {
  key: string;
  meta: boolean;
  control: boolean;
  alt: boolean;
  shift: boolean;
};

const TERMINAL_FONT_SIZE_ACTIONS = new Set([
  "increaseTerminalFontSize",
  "decreaseTerminalFontSize",
  "resetTerminalFontSize",
]);

const normalizeShortcutKey = (key: string): string =>
  /^[A-Za-z]$/.test(key) ? key.toLowerCase() : key;

export function resolveTerminalFontShortcuts(
  keyBindings: readonly KeyBinding[],
  hotkeyScheme: "disabled" | "mac" | "pc",
): TerminalKeyboardShortcut[] {
  if (hotkeyScheme === "disabled") return [];
  return keyBindings.flatMap((binding) => {
    if (!TERMINAL_FONT_SIZE_ACTIONS.has(binding.action)) return [];
    const parsed = parseKeyCombo(binding[hotkeyScheme]);
    if (!parsed || parsed.key === "Disabled") return [];
    const modifiers = new Set(parsed.modifiers);
    const isMac = hotkeyScheme === "mac";
    return [{
      key: normalizeShortcutKey(parsed.key),
      meta: isMac ? modifiers.has("⌘") : modifiers.has("Win"),
      control: isMac ? modifiers.has("⌃") : modifiers.has("Ctrl"),
      alt: isMac ? modifiers.has("⌥") : modifiers.has("Alt"),
      shift: modifiers.has("Shift"),
    }];
  });
}

type FocusDocument = Pick<Document, "activeElement" | "addEventListener" | "removeEventListener">;
type FocusWindow = Pick<Window, "addEventListener" | "removeEventListener">;

export const isTerminalKeyboardTarget = (target: EventTarget | null): boolean => {
  const element = target as (Element & {
    closest?: (selector: string) => Element | null;
  }) | null;
  return Boolean(element?.closest?.(TERMINAL_KEYBOARD_TARGET_SELECTOR));
};

export function installTerminalKeyboardFocusTracking(
  documentRef: FocusDocument,
  windowRef: FocusWindow,
  onFocusChange: (focused: boolean) => void,
): () => void {
  let pendingPointerTarget: EventTarget | null | undefined;
  let pendingPointerClearTimer: ReturnType<typeof setTimeout> | undefined;
  let isTracking = true;
  let windowFocused = true;

  const clearPendingPointerTarget = () => {
    if (pendingPointerClearTimer !== undefined) {
      clearTimeout(pendingPointerClearTimer);
      pendingPointerClearTimer = undefined;
    }
    pendingPointerTarget = undefined;
  };

  const publishForTarget = (target: EventTarget | null) => {
    if (!isTracking) return;
    onFocusChange(windowFocused && isTerminalKeyboardTarget(target));
  };

  const handlePointerDown = (event: Event) => {
    clearPendingPointerTarget();
    pendingPointerTarget = event.target;
    pendingPointerClearTimer = setTimeout(() => {
      const pointerTarget = pendingPointerTarget;
      clearPendingPointerTarget();
      if (pointerTarget !== undefined) {
        publishForTarget(documentRef.activeElement);
      }
    }, 0);
  };

  const handleFocusIn = (event: Event) => {
    clearPendingPointerTarget();
    publishForTarget(event.target);
  };

  const handleFocusOut = () => {
    queueMicrotask(() => {
      if (!isTracking) return;
      const pointerTarget = pendingPointerTarget;
      clearPendingPointerTarget();
      if (!windowFocused) {
        onFocusChange(false);
        return;
      }
      if (pointerTarget !== undefined) {
        publishForTarget(documentRef.activeElement);
        return;
      }
      publishForTarget(documentRef.activeElement);
    });
  };

  const handleWindowBlur = () => {
    windowFocused = false;
    clearPendingPointerTarget();
    onFocusChange(false);
  };

  const handleWindowFocus = () => {
    windowFocused = true;
    publishForTarget(documentRef.activeElement);
  };

  publishForTarget(documentRef.activeElement);
  documentRef.addEventListener("pointerdown", handlePointerDown, true);
  documentRef.addEventListener("focusin", handleFocusIn, true);
  documentRef.addEventListener("focusout", handleFocusOut, true);
  windowRef.addEventListener("blur", handleWindowBlur);
  windowRef.addEventListener("focus", handleWindowFocus);

  return () => {
    isTracking = false;
    documentRef.removeEventListener("pointerdown", handlePointerDown, true);
    documentRef.removeEventListener("focusin", handleFocusIn, true);
    documentRef.removeEventListener("focusout", handleFocusOut, true);
    windowRef.removeEventListener("blur", handleWindowBlur);
    windowRef.removeEventListener("focus", handleWindowFocus);
    clearPendingPointerTarget();
  };
}
