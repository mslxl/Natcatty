const TERMINAL_KEYBOARD_TARGET_SELECTOR =
  ".xterm, .xterm-helper-textarea, .xterm-screen, .xterm-viewport";

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

  const clearPendingPointerTarget = () => {
    if (pendingPointerClearTimer !== undefined) {
      clearTimeout(pendingPointerClearTimer);
      pendingPointerClearTimer = undefined;
    }
    pendingPointerTarget = undefined;
  };

  const publishForTarget = (target: EventTarget | null) => {
    onFocusChange(isTerminalKeyboardTarget(target));
  };

  const handlePointerDown = (event: Event) => {
    clearPendingPointerTarget();
    pendingPointerTarget = event.target;
    publishForTarget(event.target);
    pendingPointerClearTimer = setTimeout(() => {
      pendingPointerTarget = undefined;
      pendingPointerClearTimer = undefined;
    }, 0);
  };

  const handleFocusIn = (event: Event) => {
    clearPendingPointerTarget();
    publishForTarget(event.target);
  };

  const handleFocusOut = () => {
    queueMicrotask(() => {
      const pointerTarget = pendingPointerTarget;
      clearPendingPointerTarget();
      if (pointerTarget !== undefined) {
        publishForTarget(pointerTarget);
        return;
      }
      publishForTarget(documentRef.activeElement);
    });
  };

  const handleWindowBlur = () => {
    clearPendingPointerTarget();
    onFocusChange(false);
  };

  publishForTarget(documentRef.activeElement);
  documentRef.addEventListener("pointerdown", handlePointerDown, true);
  documentRef.addEventListener("focusin", handleFocusIn, true);
  documentRef.addEventListener("focusout", handleFocusOut, true);
  windowRef.addEventListener("blur", handleWindowBlur);

  return () => {
    documentRef.removeEventListener("pointerdown", handlePointerDown, true);
    documentRef.removeEventListener("focusin", handleFocusIn, true);
    documentRef.removeEventListener("focusout", handleFocusOut, true);
    windowRef.removeEventListener("blur", handleWindowBlur);
    clearPendingPointerTarget();
  };
}
