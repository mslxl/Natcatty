import { useEffect } from "react";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";
import type { FileWatchErrorEvent, FileWatchSyncedEvent, SftpStateOptions } from "./types";

export const useSftpFileWatch = (options?: SftpStateOptions) => {
  useEffect(() => {
    const bridge = netcattyBridge.get();
    if (!bridge?.onFileWatchSynced || !bridge?.onFileWatchError) return;

    const unsubscribeSynced = bridge.onFileWatchSynced((payload: FileWatchSyncedEvent) => {
      options?.onFileWatchSynced?.(payload);
    });

    const unsubscribeError = bridge.onFileWatchError((payload: FileWatchErrorEvent) => {
      options?.onFileWatchError?.(payload);
    });

    return () => {
      try {
        unsubscribeSynced?.();
        unsubscribeError?.();
      } catch {
        // ignore cleanup errors
      }
    };
  }, [options]);
};
