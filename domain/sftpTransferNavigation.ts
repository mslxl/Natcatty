import type { TransferTask } from "./models";

export type SftpTransferNavigationTarget = {
  kind: "local-path" | "local-copy-panel" | "remote-host";
  hostId?: string;
  /** When true, open the source path instead of the target path. */
  useSourcePath: boolean;
};

/**
 * Decide which endpoint the global transfer center should open.
 *
 * Resume needs a remote host available for re-auth/reconnect. Opening the
 * destination folder after completion should open the target instead.
 *
 * Important: dual-pane downloads store a real local connection UUID in
 * `targetConnectionId`, not the sentinel `"local"`. Prefer host ids +
 * direction over connection-id string equality.
 */
export function resolveSftpTransferNavigationTarget(
  task: Pick<
    TransferTask,
    | "direction"
    | "sourceHostId"
    | "targetHostId"
    | "sourceConnectionId"
    | "targetConnectionId"
    | "sourcePath"
    | "targetPath"
    | "isDirectory"
  >,
  forResume: boolean,
): SftpTransferNavigationTarget {
  const sourceIsRemote = Boolean(task.sourceHostId);
  const targetIsRemote = Boolean(task.targetHostId);
  const isLocalCopy = task.direction === "local-copy"
    || (!sourceIsRemote && !targetIsRemote);

  if (forResume) {
    if (isLocalCopy) {
      return { kind: "local-copy-panel", useSourcePath: false };
    }
    // Prefer the remote source when present (downloads, remote-to-remote).
    // Uploads only have a remote target.
    if (sourceIsRemote) {
      return { kind: "remote-host", hostId: task.sourceHostId, useSourcePath: true };
    }
    return { kind: "remote-host", hostId: task.targetHostId, useSourcePath: false };
  }

  if (!targetIsRemote) {
    return { kind: "local-path", useSourcePath: false };
  }
  return { kind: "remote-host", hostId: task.targetHostId, useSourcePath: false };
}

export function resolveSftpTransferNavigationPath(
  task: Pick<TransferTask, "sourcePath" | "targetPath" | "isDirectory">,
  useSourcePath: boolean,
): string {
  const rawPath = useSourcePath ? task.sourcePath : task.targetPath;
  if (task.isDirectory) return rawPath;
  return rawPath.replace(/[\\/][^\\/]+$/, "") || "/";
}
