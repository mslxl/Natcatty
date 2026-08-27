/**
 * CJK IMEs (notably Sogou on macOS) often emit a keydown whose `event.key` is
 * still the half-width ASCII punctuation, then commit the full-width glyph via
 * an `input`/`insertText` event. xterm.js sends the keydown character and then
 * drops the input event because `_keyDownSeen` is set — so the PTY receives
 * "," instead of "，".
 *
 * Defer those keydowns to the following insertText. If no remap arrives before
 * keyup/blur, the original ASCII key is flushed. Composition (keyCode 229 /
 * isComposing) stays on xterm's CompositionHelper path.
 *
 * The deferral must never outlive the keystroke it was armed for: Windows IMEs
 * report `Process` / keyCode 229 as the keyup of a key they consumed (or drop
 * the keyup), so a deferral flushed only on an exact key match stayed armed and
 * blocked typed input from then on (#3103). Any real key release, and any later
 * unrelated keydown, now ends the deferral.
 */

export type ImeTextInputKeyEvent = {
  type?: string;
  key: string;
  keyCode?: number;
  altKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  isComposing?: boolean;
};

export type ImeTextInputEvent = Pick<InputEvent, "data" | "inputType">;

/** Printable ASCII punctuation IMEs commonly remap to full-width forms. */
const ASCII_PUNCTUATION_RE = /^[\x21-\x2f\x3a-\x40\x5b-\x60\x7b-\x7e]$/;

export function isAsciiPunctuationKey(key: string): boolean {
  return ASCII_PUNCTUATION_RE.test(key);
}

export function shouldDeferKeyDownForImeTextInput(
  event: ImeTextInputKeyEvent,
): boolean {
  if (event.type !== undefined && event.type !== "keydown") return false;
  if (event.isComposing === true || event.keyCode === 229) return false;
  if (event.altKey || event.ctrlKey || event.metaKey) return false;
  return isAsciiPunctuationKey(event.key);
}

/** Key releases that carry no keystroke of their own. */
const MODIFIER_ONLY_KEY_RE =
  /^(Shift|Control|Alt|Meta|CapsLock|NumLock|ScrollLock|Hyper|Super|Fn|FnLock|Symbol|SymbolLock)$/;

export function isModifierOnlyKey(key: string): boolean {
  return MODIFIER_ONLY_KEY_RE.test(key);
}

/**
 * DOM keys that stand in for a key the IME consumed, mirroring the non-text
 * DOM keys the Kitty encoder already refuses to send as text.
 */
const IME_SENTINEL_KEYS = new Set(["Dead", "Process", "Unidentified", "Compose"]);

export function isImeSentinelKeyUp(event: ImeTextInputKeyEvent): boolean {
  if (event.type !== undefined && event.type !== "keyup") return false;
  return event.keyCode === 229 || IME_SENTINEL_KEYS.has(event.key);
}

/**
 * A deferred punctuation keystroke is over once any real key release arrives.
 * The IME remap (insertText) is dispatched before keyup, so a release means the
 * IME did not remap the key and the ASCII character must be flushed.
 *
 * The release key cannot be matched exactly: Windows IMEs report `Process` /
 * keyCode 229 as the release of a key they consumed, and some drop the release
 * entirely. Requiring an exact key match left the deferral armed, and the
 * armed deferral then blocked input (#3103). Composing releases are excluded —
 * an active composition still owns the keystroke and resolves it via
 * insertText.
 */
export function shouldFlushDeferredImeTextInputOnKeyUp(
  deferredKey: string | null | undefined,
  event: ImeTextInputKeyEvent,
): boolean {
  if (!deferredKey) return false;
  if (event.type !== undefined && event.type !== "keyup") return false;
  if (event.isComposing === true) return false;
  if (event.altKey || event.ctrlKey || event.metaKey) return false;
  return !isModifierOnlyKey(event.key);
}

/**
 * A deferral that outlived its own keystroke is stale — the IME swallowed the
 * release. Flush it when a new, unmodified, non-composing keydown for a
 * different key arrives so the pending ASCII character still reaches the PTY.
 * A same-key keydown is auto-repeat (or a second IME attempt) and keeps
 * re-arming instead.
 */
export function shouldFlushStaleDeferredImeTextInput(
  deferredKey: string | null | undefined,
  event: ImeTextInputKeyEvent,
): boolean {
  if (!deferredKey) return false;
  if (event.type !== undefined && event.type !== "keydown") return false;
  if (event.isComposing === true || event.keyCode === 229) return false;
  if (event.altKey || event.ctrlKey || event.metaKey) return false;
  if (isModifierOnlyKey(event.key)) return false;
  return event.key !== deferredKey;
}

/**
 * True when the release that ends a deferral is an IME sentinel standing in
 * for the deferred key, so the paired Kitty release must be encoded from the
 * deferred physical key instead. A real keyup for another held key keeps its
 * own identity — rewriting it would strand that key's forwarded press and the
 * TUI would see it stuck down until focus loss.
 */
export function shouldRewriteKeyUpToDeferredImeKey(
  deferredKey: string | null | undefined,
  event: ImeTextInputKeyEvent,
): boolean {
  if (!deferredKey) return false;
  if (event.type !== undefined && event.type !== "keyup") return false;
  if (event.altKey || event.ctrlKey || event.metaKey) return false;
  if (isModifierOnlyKey(event.key)) return false;
  if (event.key === deferredKey) return false;
  return isImeSentinelKeyUp(event);
}

export function shouldBlockKeyPressForImeTextInput(
  deferredKey: string | null | undefined,
  event: ImeTextInputKeyEvent,
  deferredKeyCode?: number | null,
): boolean {
  if (!deferredKey || event.type !== "keypress") return false;
  // Composition keypresses carry no committable character on this path.
  if (event.isComposing === true || event.keyCode === 229) return true;
  // Only the deferred keystroke itself is suppressed. Blocking every keypress
  // while armed turned one stale deferral into a terminal that ignored all
  // typed input (#3103).
  return (
    event.key === deferredKey ||
    (deferredKeyCode != null && event.keyCode === deferredKeyCode)
  );
}

export function shouldCommitDeferredImeTextInput(
  deferredKey: string | null | undefined,
  event: ImeTextInputEvent,
): event is ImeTextInputEvent & { data: string } {
  return (
    Boolean(deferredKey) &&
    event.inputType === "insertText" &&
    typeof event.data === "string" &&
    event.data.length > 0
  );
}

/**
 * True when insertText/flush kept the deferred ASCII key (no CJK remap).
 * Those commits must not use Kitty composition encoding — under report-all
 * that emits unidentified CSI 0 u and drops press/release.
 */
export function isUnchangedDeferredImeTextInput(
  deferredKey: string | null | undefined,
  text: string,
): boolean {
  return deferredKey != null && text === deferredKey;
}
