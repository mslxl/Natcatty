import { useEffect, useState } from 'react';

import type { HotkeyScheme } from '../../domain/models/keyBindings';

export type ShortcutModifierEvent = Pick<KeyboardEvent, 'key' | 'metaKey' | 'ctrlKey'>;

function getModifierKey(scheme: Exclude<HotkeyScheme, 'disabled'>): 'Meta' | 'Control' {
  return scheme === 'mac' ? 'Meta' : 'Control';
}

function hasModifierFlag(event: ShortcutModifierEvent, scheme: Exclude<HotkeyScheme, 'disabled'>): boolean {
  return scheme === 'mac' ? event.metaKey : event.ctrlKey;
}

/** Whether a keyboard event indicates that the active shortcut modifier is held. */
export function isShortcutModifierHeld(
  event: ShortcutModifierEvent,
  scheme: HotkeyScheme,
): boolean {
  if (scheme === 'disabled') return false;
  return event.key === getModifierKey(scheme) || hasModifierFlag(event, scheme);
}

/** Whether a keyup event means the active shortcut modifier is no longer held. */
export function shouldReleaseShortcutModifier(
  event: ShortcutModifierEvent,
  scheme: HotkeyScheme,
): boolean {
  if (scheme === 'disabled') return true;
  return event.key === getModifierKey(scheme) || !hasModifierFlag(event, scheme);
}

export function useShortcutModifierHeld(scheme: HotkeyScheme): boolean {
  const [isHeld, setIsHeld] = useState(false);

  useEffect(() => {
    setIsHeld(false);
    if (scheme === 'disabled') return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (isShortcutModifierHeld(event, scheme)) {
        setIsHeld(true);
      }
    };
    const handleKeyUp = (event: KeyboardEvent) => {
      if (shouldReleaseShortcutModifier(event, scheme)) {
        setIsHeld(false);
      }
    };
    const clearHeldState = () => setIsHeld(false);
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        clearHeldState();
      }
    };

    window.addEventListener('keydown', handleKeyDown, true);
    window.addEventListener('keyup', handleKeyUp, true);
    window.addEventListener('blur', clearHeldState);
    document.addEventListener('visibilitychange', handleVisibilityChange);

    return () => {
      window.removeEventListener('keydown', handleKeyDown, true);
      window.removeEventListener('keyup', handleKeyUp, true);
      window.removeEventListener('blur', clearHeldState);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
    };
  }, [scheme]);

  return isHeld;
}
