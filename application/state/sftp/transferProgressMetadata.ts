export function hasNewSourceFingerprint(
  current: string | undefined,
  incoming: string | undefined,
): incoming is string {
  return typeof incoming === "string" && incoming.length > 0 && incoming !== current;
}

export function shouldApplyTransferProgress({
  elapsedMs,
  transferred,
  total,
  currentSourceFingerprint,
  incomingSourceFingerprint,
}: {
  elapsedMs: number;
  transferred: number;
  total: number;
  currentSourceFingerprint?: string;
  incomingSourceFingerprint?: string;
}): boolean {
  return elapsedMs >= 100
    || transferred >= total
    || hasNewSourceFingerprint(currentSourceFingerprint, incomingSourceFingerprint);
}
