"use strict";

async function stageRendererFileToTemp(file, localPath, fsImpl, signal = null) {
  if (!file || typeof file.stream !== "function") {
    throw new Error("Upload file streaming is unavailable");
  }
  let handle = null;
  let reader = null;
  const cancellationError = () => (
    signal?.reason instanceof Error ? signal.reason : new Error("Upload staging cancelled")
  );
  const onAbort = () => {
    void Promise.resolve(reader?.cancel?.(cancellationError())).catch(() => {});
    void handle?.close?.().catch(() => {});
  };
  try {
    handle = await fsImpl.promises.open(localPath, "wx", 0o600);
    // Stream/getReader can throw synchronously (revoked File, renderer teardown,
    // or a malformed provider). Initialize them inside the cleanup boundary so
    // the just-created file handle is never stranded.
    reader = file.stream().getReader();
    if (signal?.aborted) throw cancellationError();
    signal?.addEventListener?.("abort", onAbort, { once: true });
    while (true) {
      if (signal?.aborted) throw cancellationError();
      const { done, value } = await reader.read();
      if (signal?.aborted) throw cancellationError();
      if (done) break;
      if (!value) continue;
      const chunk = Buffer.from(value.buffer, value.byteOffset, value.byteLength);
      await handle.write(chunk);
    }
    return localPath;
  } catch (error) {
    await handle?.close?.().catch(() => {});
    await fsImpl.promises.unlink(localPath).catch(() => {});
    throw error;
  } finally {
    signal?.removeEventListener?.("abort", onAbort);
    reader?.releaseLock?.();
    await handle?.close?.().catch(() => {});
  }
}

module.exports = { stageRendererFileToTemp };
