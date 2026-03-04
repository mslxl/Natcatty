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

/**
 * Check if an error message indicates a fatal error that should stop the entire upload.
 * This includes session errors AND target directory deletion errors.
 */
export const isFatalUploadError = (errorMessage: string): boolean => {
  // Reuse isSessionError for session-related checks
  const asError = new Error(errorMessage);
  if (isSessionError(asError)) return true;

  const msg = errorMessage.toLowerCase();
  return (
    // Connection-related (broader than session errors)
    msg.includes("connection") ||
    // Target directory was deleted during upload
    msg.includes("no such file") ||
    msg.includes("enoent") ||
    msg.includes("does not exist") ||
    msg.includes("write stream error") ||
    // Directory was removed
    msg.includes("directory not found") ||
    msg.includes("not a directory")
  );
};
