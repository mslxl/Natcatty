import assert from "node:assert/strict";
import test from "node:test";

import {
  COPY_ON_SELECT_USER_GESTURE_RELEASE_MS,
  createCopyOnSelectUserGestureTracker,
  shouldWriteCopyOnSelect,
  subscribeCopyOnSelectUserGesture,
} from "./copyOnSelect.ts";

test("copy-on-select writes only after a user selection gesture", () => {
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: true,
  }), true);
});

test("copy-on-select skips SearchAddon and other programmatic selections", () => {
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: false,
  }), false);
});

test("copy-on-select still skips restore and attach snapshots", () => {
  assert.equal(shouldWriteCopyOnSelect({
    allowCopy: false,
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: true,
  }), false);
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: true,
    isUserSelection: true,
  }), false);
  assert.equal(shouldWriteCopyOnSelect({
    hasText: false,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: true,
  }), false);
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: false,
    isRestoringSelection: false,
    isUserSelection: true,
  }), false);
});

test("user gesture stays armed until shortly after pointer-up", () => {
  assert.ok(COPY_ON_SELECT_USER_GESTURE_RELEASE_MS < 200);

  const scheduled: Array<{ cb: () => void; ms: number }> = [];
  const tracker = createCopyOnSelectUserGestureTracker({
    setTimeoutFn: ((cb: () => void, ms?: number) => {
      scheduled.push({ cb, ms: ms ?? 0 });
      return scheduled.length as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout,
    clearTimeoutFn: (() => {}) as typeof clearTimeout,
  });

  assert.equal(tracker.isActive(), false);
  tracker.mark();
  assert.equal(tracker.isActive(), true);

  tracker.release();
  assert.equal(tracker.isActive(), true);
  assert.equal(scheduled.at(-1)?.ms, COPY_ON_SELECT_USER_GESTURE_RELEASE_MS);

  // A later SearchAddon revival (200ms) must not still look like a drag.
  scheduled.at(-1)?.cb();
  assert.equal(tracker.isActive(), false);

  tracker.dispose();
});

test("marking again cancels a pending release so a new drag can copy", () => {
  const cleared: number[] = [];
  let nextId = 1;
  const tracker = createCopyOnSelectUserGestureTracker({
    setTimeoutFn: ((cb: () => void) => {
      const id = nextId;
      nextId += 1;
      void cb;
      return id as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout,
    clearTimeoutFn: ((id: ReturnType<typeof setTimeout>) => {
      cleared.push(id as unknown as number);
    }) as typeof clearTimeout,
  });

  tracker.mark();
  tracker.release();
  tracker.mark();

  assert.deepEqual(cleared, [1]);
  assert.equal(tracker.isActive(), true);
  tracker.dispose();
});

test("pointer listeners mark on terminal down and release on document up", () => {
  const listeners = new Map<string, Set<EventListener>>();
  const add = (type: string, listener: EventListener) => {
    const set = listeners.get(type) ?? new Set();
    set.add(listener);
    listeners.set(type, set);
  };
  const remove = (type: string, listener: EventListener) => {
    listeners.get(type)?.delete(listener);
  };
  const fire = (target: "el" | "root", type: string) => {
    for (const listener of listeners.get(`${target}:${type}`) ?? []) listener(new Event(type));
  };

  const el = {
    addEventListener(type: string, listener: EventListener) {
      add(`el:${type}`, listener);
    },
    removeEventListener(type: string, listener: EventListener) {
      remove(`el:${type}`, listener);
    },
  };
  const root = {
    addEventListener(type: string, listener: EventListener) {
      add(`root:${type}`, listener);
    },
    removeEventListener(type: string, listener: EventListener) {
      remove(`root:${type}`, listener);
    },
  };

  let marked = 0;
  let released = 0;
  const unsubscribe = subscribeCopyOnSelectUserGesture(
    { element: el },
    {
      mark: () => {
        marked += 1;
      },
      release: () => {
        released += 1;
      },
    },
    root,
  );

  fire("el", "mousedown");
  fire("root", "mouseup");
  fire("el", "contextmenu");
  assert.equal(marked, 2);
  assert.equal(released, 1);

  unsubscribe();
  fire("el", "mousedown");
  fire("root", "mouseup");
  assert.equal(marked, 2);
  assert.equal(released, 1);
});

test("issue 3007: search match then later revival does not copy", () => {
  const scheduled: Array<() => void> = [];
  const tracker = createCopyOnSelectUserGestureTracker({
    setTimeoutFn: ((cb: () => void) => {
      scheduled.push(cb);
      return scheduled.length as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout,
    clearTimeoutFn: (() => {}) as typeof clearTimeout,
  });

  // Typing in the search bar selects the match with no terminal pointer.
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: tracker.isActive(),
  }), false);

  // User drag-selects a docker image id in the buffer.
  tracker.mark();
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: tracker.isActive(),
  }), true);
  tracker.release();
  scheduled.at(-1)?.();

  // Opening the snippet dialog resizes the terminal; SearchAddon re-selects
  // the search term. Clipboard must stay on the image id.
  assert.equal(shouldWriteCopyOnSelect({
    hasText: true,
    copyOnSelect: true,
    isRestoringSelection: false,
    isUserSelection: tracker.isActive(),
  }), false);

  tracker.dispose();
});
