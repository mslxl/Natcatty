export const isSessionError = (err: unknown): boolean => {
  if (!(err instanceof Error)) return false;
  const msg = err.message.toLowerCase();
  return (
    msg.includes("session not found") ||
    msg.includes("sftp session") ||
    msg.includes("session lost") ||
    msg.includes("channel not ready") ||
    msg.includes("readdir is not a function") ||
    msg.includes("channel closed") ||
    msg.includes("connection closed") ||
    msg.includes("connection reset") ||
    msg.includes("write after end") ||
    msg.includes("no response") ||
    msg.includes("not connected") ||
    msg.includes("client disconnected") ||
    msg.includes("timed out")
  );
};

/** True absence only — ENOTSUP / unknown inspection must not map to "no conflict". */
export const isMissingStatError = (error: unknown): boolean => {
  const code = (error as { code?: string | number } | null)?.code;
  return code === 2
    || code === "ENOENT"
    || code === "NO_SUCH_FILE"
    || code === "SSH_FX_NO_SUCH_FILE"
    || String((error as { message?: string } | null)?.message || "").trim() === "ENOENT";
};
