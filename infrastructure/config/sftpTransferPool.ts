/** 0 keeps the connection warm until its host is torn down or the app exits. */
export const DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS = 5 * 60_000;
export const SFTP_TRANSFER_POOL_IDLE_TTL_PRESETS_MS = [
  60_000,
  5 * 60_000,
  15 * 60_000,
  30 * 60_000,
  0,
] as const;

export type SftpTransferPoolIdleTtlMs = (typeof SFTP_TRANSFER_POOL_IDLE_TTL_PRESETS_MS)[number];

export function resolveSftpTransferPoolIdleTtlMs(
  readStoredValue: () => number | null | undefined,
): number {
  const stored = readStoredValue();
  if (stored === 0) return 0;
  if (
    typeof stored === "number"
    && Number.isFinite(stored)
    && (SFTP_TRANSFER_POOL_IDLE_TTL_PRESETS_MS as readonly number[]).includes(stored)
  ) {
    return stored;
  }
  return DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS;
}

export function isTransferPoolIdleReclaimDisabled(idleTtlMs: number): boolean {
  return !Number.isFinite(idleTtlMs) || idleTtlMs <= 0;
}
