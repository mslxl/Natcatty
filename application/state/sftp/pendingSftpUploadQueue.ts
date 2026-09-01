export function enqueuePendingSftpUpload<T>(
  queue: ReadonlyArray<T>,
  upload: T,
): T[] {
  return [...queue, upload];
}

export function removePendingSftpUpload<T extends { requestId: string }>(
  queue: ReadonlyArray<T>,
  requestId: string,
): ReadonlyArray<T> {
  const index = queue.findIndex((upload) => upload.requestId === requestId);
  if (index < 0) return queue;
  return [...queue.slice(0, index), ...queue.slice(index + 1)];
}
