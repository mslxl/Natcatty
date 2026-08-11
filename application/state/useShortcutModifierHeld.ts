import { useEffect, useMemo, useState } from 'react';

import { parseKeyCombo, type HotkeyScheme } from '../../domain/models/keyBindings';

export type ShortcutModifierEvent = Pick<KeyboardEvent, 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey'>;

export type ShortcutModifierRequirements = Readonly<ShortcutModifierEvent>;

const MODIFIER_TOKENS = {
  mac: {
    '⌘': 'metaKey',
    '⌃': 'ctrlKey',
    '⌥': 'altKey',
    Shift: 'shiftKey',
  },
  pc: {
    Ctrl: 'ctrlKey',
    Alt: 'altKey',
    Shift: 'shiftKey',
    Win: 'metaKey',
  },
} as const;

/**
 * Resolve the modifier combination for the effective [1...9] binding.
 * A non-range or disabled binding cannot reveal tab numbers because the
 * dispatcher does not use it for the number-switch action.
 */
export function getShortcutModifierRequirements(
  keyBinding: string | null,
  scheme: HotkeyScheme,
): ShortcutModifierRequirements | null {
  if (scheme === 'disabled' || !keyBinding || !keyBinding.includes('[1...9]')) return null;
  const parsed = parseKeyCombo(keyBinding);
  if (!parsed || parsed.modifiers.length === 0) return null;

  const requirements: Record<keyof ShortcutModifierEvent, boolean> = {
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
  };
  const tokenMap = MODIFIER_TOKENS[scheme];
  for (const modifier of parsed.modifiers) {
    const flag = tokenMap[modifier as keyof typeof tokenMap];
    if (!flag) return null;
    requirements[flag] = true;
  }

  return requirements;
}

/** Whether a keyboard event has exactly the modifiers required by the binding. */
export function isShortcutModifierHeld(
  event: ShortcutModifierEvent,
  requirements: ShortcutModifierRequirements | null,
): boolean {
  if (!requirements) return false;
  return event.metaKey === requirements.metaKey
    && event.ctrlKey === requirements.ctrlKey
    && event.altKey === requirements.altKey
    && event.shiftKey === requirements.shiftKey;
}

/** Whether a keyup event means the required modifier combination is no longer held. */
export function shouldReleaseShortcutModifier(
  event: ShortcutModifierEvent,
  requirements: ShortcutModifierRequirements | null,
): boolean {
  return !isShortcutModifierHeld(event, requirements);
}

export function useShortcutModifierHeld(
  keyBinding: string | null,
  scheme: HotkeyScheme,
): boolean {
  const [isHeld, setIsHeld] = useState(false);
  const requirements = useMemo(
    () => getShortcutModifierRequirements(keyBinding, scheme),
    [keyBinding, scheme],
  );

  useEffect(() => {
    setIsHeld(false);
    if (!requirements) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (isShortcutModifierHeld(event, requirements)) {
        setIsHeld(true);
      }
    };
    const handleKeyUp = (event: KeyboardEvent) => {
      if (shouldReleaseShortcutModifier(event, requirements)) {
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
  }, [requirements]);

  return isHeld;
}
