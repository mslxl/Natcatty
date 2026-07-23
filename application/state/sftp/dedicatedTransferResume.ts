import type { Host, Identity, KnownHost, SSHKey, TerminalSettings, TransferTask } from "../../../domain/models";
import { validateTransferResumeSource } from "../../../domain/sftpTransferCenter";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { buildSftpHostCredentials } from "./useSftpHostCredentials";
import { runWithTransferRetry } from "./transferRetry";

export interface DedicatedResumeDeps {
  hosts: readonly Host[];
  keys: readonly SSHKey[];
  identities: readonly Identity[];
  knownHosts?: readonly KnownHost[];
  terminalSettings?: Pick<TerminalSettings, "verifyHostKeys" | "keepaliveInterval" | "keepaliveCountMax">;
}

export interface DedicatedResumeProgress {
  transferred: number;
  total: number;
  speed: number;
  checkpointBytes?: number;
  resumeStage?: TransferTask["resumeStage"];
  downloadCheckpointBytes?: number;
  uploadCheckpointBytes?: number;
  sourceFingerprint?: string;
}

/**
 * Resolve a vault host for a transfer endpoint. Prefer stable id; fall back to
 * label/hostname when the id is stale after vault edits or older task records.
 */
export function resolveHostForTransferEndpoint(
  hosts: readonly Host[],
  hostId?: string,
  hostLabel?: string,
): Host | null {
  if (hostId) {
    const byId = hosts.find((host) => host.id === hostId);
    if (byId) return byId;
  }
  const needle = (hostLabel || "").trim().toLowerCase();
  if (!needle) return null;
  return hosts.find((host) => {
    const label = (host.label || "").trim().toLowerCase();
    const hostname = (host.hostname || "").trim().toLowerCase();
    return label === needle || hostname === needle;
  }) ?? null;
}

function isAuthError(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  const msg = err.message.toLowerCase();
  return (
    msg.includes("authentication")
    || msg.includes("auth")
    || msg.includes("password")
    || msg.includes("permission denied")
  );
}

/**
 * Open a transfer-owned SFTP session from vault credentials.
 * Shared entry for dedicated resume / bulk transfer (not browse-panel sessions).
 */
export async function openTransferSftpSession(
  host: Host,
  deps: DedicatedResumeDeps,
): Promise<string> {
  const bridge = netcattyBridge.get();
  if (!bridge?.openSftp) throw new Error("SFTP bridge unavailable");

  const credentials = buildSftpHostCredentials({
    host,
    hosts: [...deps.hosts],
    keys: [...deps.keys],
    identities: [...deps.identities],
    knownHosts: deps.knownHosts ? [...deps.knownHosts] : undefined,
    terminalSettings: deps.terminalSettings,
  });

  const hasKey = !!credentials.privateKey || !!credentials.identityFilePaths?.length;
  const hasPassword = !!credentials.password;

  if (hasKey) {
    try {
      const keyFirst = { ...credentials };
      if (!credentials.sudo) keyFirst.password = undefined;
      return await bridge.openSftp(keyFirst);
    } catch (err) {
      if (hasPassword && isAuthError(err)) {
        return await bridge.openSftp({
          ...credentials,
          privateKey: undefined,
          certificate: undefined,
          publicKey: undefined,
          keyId: undefined,
          keySource: undefined,
          identityFilePaths: undefined,
        });
      }
      throw err;
    }
  }

  return bridge.openSftp(credentials);
}

/** @deprecated Use openTransferSftpSession */
const openDedicatedSftpSession = openTransferSftpSession;

async function closeDedicatedSftpSession(sftpId: string | undefined): Promise<void> {
  if (!sftpId) return;
  try {
    await netcattyBridge.get()?.closeSftp?.(sftpId);
  } catch {
    // Best-effort cleanup of transfer-owned sessions.
  }
}

/**
 * Resume a transfer by opening a dedicated SFTP session (not tied to any UI
 * panel). Used after app restart or when the original browse connection is gone.
 */
export async function resumeTransferWithDedicatedSession(
  task: TransferTask,
  deps: DedicatedResumeDeps,
  onProgress?: (progress: DedicatedResumeProgress) => void,
): Promise<{ success: boolean; error?: string }> {
  const bridge = netcattyBridge.get();
  if (!bridge?.startStreamTransfer) {
    return { success: false, error: "Transfer bridge unavailable" };
  }

  const isDownload = task.direction === "download"
    || (!!task.sourceHostId && !task.targetHostId)
    || (task.targetConnectionId === "local" && !!task.sourceHostId);
  const isUpload = task.direction === "upload"
    || (!!task.targetHostId && !task.sourceHostId)
    || (task.sourceConnectionId === "local" && !!task.targetHostId);

  if (!isDownload && !isUpload) {
    return {
      success: false,
      error: "Server-to-server resume still needs an open SFTP panel. Open both hosts and try again.",
    };
  }

  const remoteHost = isDownload
    ? resolveHostForTransferEndpoint(deps.hosts, task.sourceHostId, task.sourceHostLabel)
    : resolveHostForTransferEndpoint(deps.hosts, task.targetHostId, task.targetHostLabel);

  if (!remoteHost) {
    const label = isDownload
      ? (task.sourceHostLabel || task.sourceHostId || "source")
      : (task.targetHostLabel || task.targetHostId || "target");
    return {
      success: false,
      error: `Cannot find host "${label}" in your vault. Re-add the host or start a new transfer.`,
    };
  }

  let sftpId: string | undefined;
  try {
    // Open session + stream with one automatic retry on transient failures.
    await runWithTransferRetry(async (attempt) => {
      if (attempt > 0 || !sftpId) {
        await closeDedicatedSftpSession(sftpId);
        sftpId = await openTransferSftpSession(remoteHost, deps);
      }

      if (isDownload) {
        const sourceStat = await bridge.statSftp?.(sftpId, task.sourcePath, "auto");
        if (!sourceStat) throw new Error("Source is unavailable");
        const validationError = validateTransferResumeSource(task, {
          size: sourceStat.size,
          lastModified: sourceStat.lastModified,
        });
        if (validationError) throw new Error(validationError);
      }

      const result = await bridge.startStreamTransfer!(
        {
          transferId: task.id,
          sourcePath: task.sourcePath,
          targetPath: task.targetPath,
          sourceType: isDownload ? "sftp" : "local",
          targetType: isDownload ? "local" : "sftp",
          sourceSftpId: isDownload ? sftpId : undefined,
          targetSftpId: isUpload ? sftpId : undefined,
          sourceHostId: isDownload ? remoteHost.id : undefined,
          targetHostId: isUpload ? remoteHost.id : undefined,
          totalBytes: task.totalBytes || undefined,
          resumable: task.resumable !== false,
          checkpointBytes: task.checkpointBytes ?? task.transferredBytes ?? 0,
          resumeStage: task.resumeStage,
          downloadCheckpointBytes: task.downloadCheckpointBytes,
          uploadCheckpointBytes: task.uploadCheckpointBytes,
          sourceFingerprint: task.sourceFingerprint,
        },
        (transferred, total, speed, checkpoint) => {
          onProgress?.({
            transferred,
            total,
            speed,
            checkpointBytes: checkpoint?.checkpointBytes ?? transferred,
            resumeStage: checkpoint?.resumeStage,
            downloadCheckpointBytes: checkpoint?.downloadCheckpointBytes,
            uploadCheckpointBytes: checkpoint?.uploadCheckpointBytes,
            sourceFingerprint: checkpoint?.sourceFingerprint,
          });
        },
      );

      if (result?.error) {
        throw new Error(result.error);
      }
    }, { retries: 1, delayMs: 600 });

    return { success: true };
  } catch (error) {
    return {
      success: false,
      error: error instanceof Error ? error.message : String(error),
    };
  } finally {
    // Transfer bridge releases its session lease on completion; force-close the
    // dedicated handle we opened for this resume.
    await closeDedicatedSftpSession(sftpId);
  }
}
