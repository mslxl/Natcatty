import { useEffect, useLayoutEffect, useRef } from "react";

import { sftpTransferCenterStore } from "../sftpTransferCenterStore";

export function useWarmSftpTransferPool(params: {
  hostIds: readonly string[];
  activeHostId?: string;
  warmTransferPoolForHost?: (hostId: string) => void | Promise<void>;
}) {
  const warmRef = useRef(params.warmTransferPoolForHost);
  warmRef.current = params.warmTransferPoolForHost;
  const hostIdsKey = [...new Set([
    ...params.hostIds,
    ...(params.activeHostId ? [params.activeHostId] : []),
  ])].sort().join("\u0000");

  useEffect(() => {
    for (const hostId of hostIdsKey.split("\u0000").filter(Boolean)) {
      void warmRef.current?.(hostId);
    }
  }, [hostIdsKey]);
}

export function useReportSftpTransferOwnerActivity(params: {
  ownerId: string;
  activeTransfersCount: number;
  onActiveTransfersChange?: (count: number) => void;
}) {
  const onChangeRef = useRef(params.onActiveTransfersChange);
  onChangeRef.current = params.onActiveTransfersChange;

  useLayoutEffect(() => {
    onChangeRef.current?.(params.activeTransfersCount);
  }, [params.activeTransfersCount]);

  useEffect(() => () => {
    const unfinished = sftpTransferCenterStore.getSnapshot().tasks.filter((task) => (
      task.ownerId === params.ownerId
      && !task.parentTaskId
      && task.status !== "completed"
      && task.status !== "cancelled"
    )).length;
    onChangeRef.current?.(unfinished);
  }, [params.ownerId]);
}
