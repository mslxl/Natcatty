/**
 * Copy-on-select policy for the xterm selection overlay.
 *
 * SearchAddon (and other programmatic paths) call terminal.select() to mark
 * the active match. Those selection-change events must not write the
 * clipboard — otherwise a later user selection is overwritten by the search
 * term after a resize/write revival (issue #3007).
 */

export const COPY_ON_SELECT_USER_GESTURE_RELEASE_MS = 80;

export type CopyOnSelectUserGestureTracker = {
  mark: () => void;
  release: () => void;
  isActive: () => boolean;
  dispose: () => void;
};

export const createCopyOnSelectUserGestureTracker = ({
  releaseDelayMs = COPY_ON_SELECT_USER_GESTURE_RELEASE_MS,
  setTimeoutFn = setTimeout,
  clearTimeoutFn = clearTimeout,
}: {
  releaseDelayMs?: number;
  setTimeoutFn?: typeof setTimeout;
  clearTimeoutFn?: typeof clearTimeout;
} = {}): CopyOnSelectUserGestureTracker => {
  let active = false;
  let releaseTimer: ReturnType<typeof setTimeout> | null = null;

  const clearReleaseTimer = () => {
    if (releaseTimer === null) return;
    clearTimeoutFn(releaseTimer);
    releaseTimer = null;
  };

  const mark = () => {
    clearReleaseTimer();
    active = true;
  };

  const release = () => {
    clearReleaseTimer();
    releaseTimer = setTimeoutFn(() => {
      releaseTimer = null;
      active = false;
    }, releaseDelayMs);
  };

  const dispose = () => {
    clearReleaseTimer();
    active = false;
  };

  return {
    mark,
    release,
    isActive: () => active,
    dispose,
  };
};

export const subscribeCopyOnSelectUserGesture = (
  term: { element?: EventTarget | null } | null | undefined,
  tracker: Pick<CopyOnSelectUserGestureTracker, "mark" | "release">,
  root: Pick<EventTarget, "addEventListener" | "removeEventListener"> | null = (
    typeof document === "undefined" ? null : document
  ),
): (() => void) => {
  const el = term?.element;
  if (!el) return () => {};

  const onPointerDown = () => tracker.mark();
  const onPointerUp = () => tracker.release();

  el.addEventListener("mousedown", onPointerDown);
  el.addEventListener("touchstart", onPointerDown);
  // Right-click select-word fires on contextmenu, sometimes after mouseup.
  el.addEventListener("contextmenu", onPointerDown);
  root?.addEventListener("mouseup", onPointerUp);
  root?.addEventListener("touchend", onPointerUp);

  return () => {
    el.removeEventListener("mousedown", onPointerDown);
    el.removeEventListener("touchstart", onPointerDown);
    el.removeEventListener("contextmenu", onPointerDown);
    root?.removeEventListener("mouseup", onPointerUp);
    root?.removeEventListener("touchend", onPointerUp);
  };
};

export const shouldWriteCopyOnSelect = ({
  allowCopy = true,
  hasText,
  copyOnSelect,
  isRestoringSelection,
  isUserSelection,
}: {
  allowCopy?: boolean;
  hasText: boolean;
  copyOnSelect: boolean;
  isRestoringSelection: boolean;
  isUserSelection: boolean;
}): boolean => (
  allowCopy
  && hasText
  && copyOnSelect
  && !isRestoringSelection
  && isUserSelection
);
