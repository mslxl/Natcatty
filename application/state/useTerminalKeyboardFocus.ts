import { useEffect } from "react";
import type { KeyBinding } from "../../domain/models";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";
import {
  installTerminalKeyboardFocusTracking,
  resolveTerminalFontShortcuts,
  type TerminalKeyboardShortcut,
} from "./terminalKeyboardFocus";

export type TerminalKeyboardHotkeyScheme = "disabled" | "mac" | "pc";

export function useTerminalKeyboardFocus(
  enabled = true,
  hotkeyScheme: TerminalKeyboardHotkeyScheme = "disabled",
  keyBindings: readonly KeyBinding[] = [],
): void {
  useEffect(() => {
    if (!enabled) return undefined;
    const bridge = netcattyBridge.get();
    if (!bridge?.setTerminalKeyboardFocus) return undefined;
    const terminalFontShortcuts: TerminalKeyboardShortcut[] = resolveTerminalFontShortcuts(
      keyBindings,
      hotkeyScheme,
    );

    let lastPublished: boolean | undefined;
    const publish = (focused: boolean) => {
      if (focused === lastPublished) return;
      lastPublished = focused;
      try {
        bridge.setTerminalKeyboardFocus?.(focused, hotkeyScheme, terminalFontShortcuts);
      } catch {
        // Browser preview or a disposed Electron bridge.
      }
    };

    const cleanupTracking = installTerminalKeyboardFocusTracking(
      document,
      window,
      publish,
    );

    return () => {
      cleanupTracking();
      publish(false);
    };
  }, [enabled, hotkeyScheme, keyBindings]);
}
