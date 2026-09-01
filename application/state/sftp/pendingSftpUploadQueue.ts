export function enqueuePendingSftpUpload<T>(
  queue: ReadonlyArray<T>,
  upload: T,
): T[] {
  return [...queue, upload];
}

export function shouldActivatePendingSftpUploadImmediately<T>(
  queue: ReadonlyArray<T>,
): boolean {
  return queue.length === 0;
}

export function getPendingSftpUploadHead<T>(queue: ReadonlyArray<T>): T | null {
  return queue[0] ?? null;
}

export function resolvePendingSftpUploadFocusedSessionId(params: {
  tabId: string;
  workspaceFocusedSessionId?: string | null;
  sessionIds: Iterable<string>;
}): string | null {
  if (params.workspaceFocusedSessionId) return params.workspaceFocusedSessionId;
  for (const sessionId of params.sessionIds) {
    if (sessionId === params.tabId) return sessionId;
  }
  return null;
}

export function resolvePendingSftpUploadRoute<HostValue, Upload extends {
  activated: boolean;
  host: HostValue;
  initialLocation?: { hostId: string; path: string };
  originSessionId?: string;
  sourceSessionId?: string;
}>(
  queue: ReadonlyArray<Upload>,
  fallback: {
    host: HostValue;
    initialLocation: { hostId: string; path: string } | null;
    activeSessionId: string | null;
    focusedSessionId: string | null;
  },
): {
  pendingUpload: Upload | null;
  host: HostValue;
  initialLocation: { hostId: string; path: string } | null;
  activeSessionId: string | null;
  focusedSessionId: string | null;
} {
  const pendingUpload = getPendingSftpUploadHead(queue);
  if (!pendingUpload) return { pendingUpload: null, ...fallback };
  // A queued drop only owns the panel route after the previous request has
  // advanced it. Keep dormant requests visible to cancellation handling, but
  // never let them drive connection, navigation, or path-memory state.
  if (!pendingUpload.activated) return { pendingUpload, ...fallback };
  return {
    pendingUpload,
    host: pendingUpload.host,
    initialLocation: pendingUpload.initialLocation ?? null,
    activeSessionId: pendingUpload.sourceSessionId ?? null,
    focusedSessionId: pendingUpload.originSessionId ?? null,
  };
}

export function advancePendingSftpUploadQueue<Upload extends {
  activated: boolean;
  requestId: string;
  originSessionId?: string;
}>(
  queue: ReadonlyArray<Upload>,
  requestId: string,
  focusedSessionId: string | null | undefined,
): {
  queue: ReadonlyArray<Upload>;
  cancelledUploads: ReadonlyArray<Upload>;
  nextUploadToActivate: Upload | null;
  shouldFocusNext: boolean;
} {
  const handledUpload = getPendingSftpUploadHead(queue);
  const remaining = removePendingSftpUpload(queue, requestId);
  const removedHead = handledUpload?.requestId === requestId;
  const firstRemaining = removedHead ? getPendingSftpUploadHead(remaining) : null;
  const focusStayedOnHandled = Boolean(
    firstRemaining
    && focusedSessionId === handledUpload?.originSessionId
  );
  const preserveFifo = focusStayedOnHandled || focusedSessionId == null;
  const compatibleIndex = firstRemaining && !preserveFifo
    ? remaining.findIndex((upload) => (
        !upload.originSessionId || upload.originSessionId === focusedSessionId
      ))
    : 0;
  const cancelledUploads = removedHead && compatibleIndex !== 0
    ? remaining.slice(0, compatibleIndex < 0 ? remaining.length : compatibleIndex)
    : [];
  const eligibleRemaining = cancelledUploads.length > 0
    ? remaining.slice(cancelledUploads.length)
    : remaining;
  const nextHead = removedHead ? getPendingSftpUploadHead(eligibleRemaining) : null;
  const shouldActivateNext = Boolean(
    nextHead
    && (
      focusStayedOnHandled
      || focusedSessionId == null
      || focusedSessionId === nextHead.originSessionId
      || !nextHead.originSessionId
    )
  );
  const nextUploadToActivate = shouldActivateNext && nextHead
    ? (nextHead.activated ? nextHead : { ...nextHead, activated: true })
    : null;
  const nextQueue = nextUploadToActivate && nextHead !== nextUploadToActivate
    ? [nextUploadToActivate, ...eligibleRemaining.slice(1)]
    : eligibleRemaining;
  return {
    queue: nextQueue,
    cancelledUploads,
    nextUploadToActivate,
    shouldFocusNext: Boolean(
      nextHead?.originSessionId
      && handledUpload?.originSessionId
      && preserveFifo
      && focusedSessionId !== nextHead.originSessionId
    ),
  };
}

export function removePendingSftpUpload<T extends { requestId: string }>(
  queue: ReadonlyArray<T>,
  requestId: string,
): ReadonlyArray<T> {
  const index = queue.findIndex((upload) => upload.requestId === requestId);
  if (index < 0) return queue;
  return [...queue.slice(0, index), ...queue.slice(index + 1)];
}
