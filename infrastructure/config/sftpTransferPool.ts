/**
 * @deprecated Transfer channel pool no longer parks SSH connections.
 * Keep-alive is owned by the main-process SSH transport registry
 * (`sshTransportIdleTtl.ts` / settings "SSH connection keep-alive").
 *
 * These symbols remain for import stability in older tests/call sites.
 */

import {
  DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS,
  SSH_TRANSPORT_IDLE_TTL_PRESETS_MS,
  resolveSshTransportIdleTtlMs,
} from "./sshTransportIdleTtl.ts";

/** @deprecated Use DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS */
export const DEFAULT_SFTP_TRANSFER_POOL_IDLE_TTL_MS = DEFAULT_SSH_TRANSPORT_IDLE_TTL_MS;

/** @deprecated Use SSH_TRANSPORT_IDLE_TTL_PRESETS_MS */
export const SFTP_TRANSFER_POOL_IDLE_TTL_PRESETS_MS = SSH_TRANSPORT_IDLE_TTL_PRESETS_MS;

/** @deprecated */
export type SftpTransferPoolIdleTtlMs = (typeof SFTP_TRANSFER_POOL_IDLE_TTL_PRESETS_MS)[number];

/** @deprecated Use resolveSshTransportIdleTtlMs */
export function resolveSftpTransferPoolIdleTtlMs(
  readStoredValue: () => number | null | undefined,
): number {
  return resolveSshTransportIdleTtlMs(readStoredValue);
}

/** @deprecated Transfer channels close when idle; always treated as reclaim-enabled. */
export function isTransferPoolIdleReclaimDisabled(_idleTtlMs: number): boolean {
  return false;
}
