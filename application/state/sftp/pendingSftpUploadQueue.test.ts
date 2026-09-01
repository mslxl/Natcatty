import assert from "node:assert/strict";
import test from "node:test";

import {
  advancePendingSftpUploadQueue,
  enqueuePendingSftpUpload,
  getPendingSftpUploadHead,
  removePendingSftpUpload,
  resolvePendingSftpUploadFocusedSessionId,
  resolvePendingSftpUploadRoute,
  shouldActivatePendingSftpUploadImmediately,
} from "./pendingSftpUploadQueue";

test("repeated terminal drops remain queued in arrival order", () => {
  const first = { requestId: "drop-1", activated: true, entries: ["one.txt"] };
  const second = { requestId: "drop-2", activated: false, entries: ["two.txt"] };

  const queued = enqueuePendingSftpUpload(
    enqueuePendingSftpUpload([], first),
    second,
  );

  assert.deepEqual(queued, [first, second]);
  assert.deepEqual(removePendingSftpUpload(queued, "drop-1"), [second]);
  assert.equal(shouldActivatePendingSftpUploadImmediately([]), true);
  assert.equal(shouldActivatePendingSftpUploadImmediately([first]), false);
});

test("handling a stale request leaves the pending queue unchanged", () => {
  const queued = [{ requestId: "drop-2", activated: false, entries: ["two.txt"] }];
  assert.equal(removePendingSftpUpload(queued, "drop-1"), queued);
});

test("cross-route drops keep each request paired with its own host and terminal state", () => {
  const first = {
    requestId: "drop-1",
    activated: true,
    host: { id: "host-a" },
    initialLocation: { hostId: "host-a", path: "/srv/a" },
    originSessionId: "terminal-a",
    sourceSessionId: "ssh-a",
  };
  const second = {
    requestId: "drop-2",
    activated: false,
    host: { id: "host-b" },
    initialLocation: { hostId: "host-b", path: "/srv/b" },
    originSessionId: "terminal-b",
    sourceSessionId: "ssh-b",
  };
  const queued = enqueuePendingSftpUpload(
    enqueuePendingSftpUpload([], first),
    second,
  );

  assert.equal(getPendingSftpUploadHead(queued), first);
  assert.equal(getPendingSftpUploadHead(queued)?.host.id, "host-a");
  assert.equal(getPendingSftpUploadHead(queued)?.initialLocation.path, "/srv/a");
  assert.equal(getPendingSftpUploadHead(queued)?.originSessionId, "terminal-a");

  assert.deepEqual(resolvePendingSftpUploadRoute(queued, {
    host: { id: "shared-host-overwritten-by-drop-2" },
    initialLocation: { hostId: "host-b", path: "/wrong" },
    activeSessionId: "ssh-b",
    focusedSessionId: "terminal-b",
  }), {
    pendingUpload: first,
    host: first.host,
    initialLocation: first.initialLocation,
    activeSessionId: first.sourceSessionId,
    focusedSessionId: first.originSessionId,
  });

  const remaining = removePendingSftpUpload(queued, first.requestId);
  assert.equal(getPendingSftpUploadHead(remaining), second);
  assert.equal(getPendingSftpUploadHead(remaining)?.sourceSessionId, "ssh-b");
});

test("advancing the head activates the next route only when focus stayed on the completed drop", () => {
  const first = {
    requestId: "drop-1",
    activated: true,
    hostId: "host-a",
    originSessionId: "terminal-a",
  };
  const second = {
    requestId: "drop-2",
    activated: false,
    hostId: "host-b",
    originSessionId: "terminal-b",
  };

  assert.deepEqual(
    advancePendingSftpUploadQueue(
    [first, second],
      first.requestId,
      "terminal-a",
    ),
    {
      queue: [{ ...second, activated: true }],
      cancelledUploads: [],
      nextUploadToActivate: { ...second, activated: true },
      shouldFocusNext: true,
    },
  );
});

test("advancing the head preserves an unrelated manual focus change", () => {
  const first = {
    requestId: "drop-1",
    activated: true,
    hostId: "host-a",
    originSessionId: "terminal-a",
  };
  const second = {
    requestId: "drop-2",
    activated: false,
    hostId: "host-b",
    originSessionId: "terminal-b",
  };

  assert.deepEqual(
    advancePendingSftpUploadQueue(
      [first, second],
      first.requestId,
      "terminal-c",
    ),
    {
      queue: [],
      cancelledUploads: [second],
      nextUploadToActivate: null,
      shouldFocusNext: false,
    },
  );
});

test("an already-focused next route activates without rewriting focus", () => {
  const first = { requestId: "drop-1", activated: true, originSessionId: "terminal-a" };
  const second = { requestId: "drop-2", activated: false, originSessionId: "terminal-b" };

  assert.deepEqual(
    advancePendingSftpUploadQueue(
      [first, second],
      first.requestId,
      "terminal-b",
    ),
    {
      queue: [{ ...second, activated: true }],
      cancelledUploads: [],
      nextUploadToActivate: { ...second, activated: true },
      shouldFocusNext: false,
    },
  );
});

test("a dormant same-host drop cannot replace a manually selected route or path", () => {
  const pending = {
    requestId: "drop-b",
    activated: false,
    host: { id: "host-1", label: "B" },
    initialLocation: { hostId: "host-1", path: "/srv/b" },
    originSessionId: "terminal-b",
    sourceSessionId: "ssh-b",
  };
  const fallback = {
    host: { id: "host-1", label: "C" },
    initialLocation: { hostId: "host-1", path: "/srv/c" },
    activeSessionId: "ssh-c",
    focusedSessionId: "terminal-c",
  };

  assert.deepEqual(resolvePendingSftpUploadRoute([pending], fallback), {
    pendingUpload: pending,
    ...fallback,
  });
});

test("standalone terminal drops use the terminal tab itself as the active route", () => {
  const focusedSessionId = resolvePendingSftpUploadFocusedSessionId({
    tabId: "terminal-a",
    workspaceFocusedSessionId: null,
    sessionIds: ["terminal-a", "terminal-b"],
  });
  const first = {
    requestId: "drop-1",
    activated: true,
    originSessionId: "terminal-a",
  };
  const second = {
    requestId: "drop-2",
    activated: false,
    originSessionId: "terminal-a",
  };

  assert.deepEqual(
    advancePendingSftpUploadQueue([first, second], first.requestId, focusedSessionId),
    {
      queue: [{ ...second, activated: true }],
      cancelledUploads: [],
      nextUploadToActivate: { ...second, activated: true },
      shouldFocusNext: false,
    },
  );
});

test("manual focus skips incompatible dormant drops and activates the matching route", () => {
  const first = { requestId: "drop-a", activated: true, originSessionId: "terminal-a" };
  const skipped = { requestId: "drop-b", activated: false, originSessionId: "terminal-b" };
  const matching = { requestId: "drop-c", activated: false, originSessionId: "terminal-c" };

  assert.deepEqual(
    advancePendingSftpUploadQueue(
      [first, skipped, matching],
      first.requestId,
      "terminal-c",
    ),
    {
      queue: [{ ...matching, activated: true }],
      cancelledUploads: [skipped],
      nextUploadToActivate: { ...matching, activated: true },
      shouldFocusNext: false,
    },
  );
});

test("workspace focus remains authoritative over a matching standalone tab id", () => {
  assert.equal(resolvePendingSftpUploadFocusedSessionId({
    tabId: "terminal-a",
    workspaceFocusedSessionId: "terminal-b",
    sessionIds: ["terminal-a", "terminal-b"],
  }), "terminal-b");
});

test("a temporarily unknown focus preserves FIFO and activates the next drop", () => {
  const first = { requestId: "drop-a", activated: true, originSessionId: "terminal-a" };
  const second = { requestId: "drop-b", activated: false, originSessionId: "terminal-b" };

  assert.deepEqual(
    advancePendingSftpUploadQueue([first, second], first.requestId, null),
    {
      queue: [{ ...second, activated: true }],
      cancelledUploads: [],
      nextUploadToActivate: { ...second, activated: true },
      shouldFocusNext: true,
    },
  );
});
