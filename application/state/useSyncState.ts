import { useCallback, useState } from "react";
import { loadFromGist, syncToGist } from "../../infrastructure/services/syncService";

export type SyncStatus = "idle" | "success" | "error";

export const useSyncState = () => {
  const [isSyncing, setIsSyncing] = useState(false);
  const [syncStatus, setSyncStatus] = useState<SyncStatus>("idle");

  const resetSyncStatus = useCallback(() => {
    setSyncStatus("idle");
  }, []);

  const verify = useCallback(async (token: string, gistId?: string) => {
    setIsSyncing(true);
    setSyncStatus("idle");
    try {
      if (gistId) {
        await loadFromGist(token, gistId);
      }
      setSyncStatus("success");
    } catch (err) {
      setSyncStatus("error");
      throw err;
    } finally {
      setIsSyncing(false);
    }
  }, []);

  const upload = useCallback(
    async (
      token: string,
      gistId: string | undefined,
      data: Parameters<typeof syncToGist>[2],
    ) => {
      setIsSyncing(true);
      setSyncStatus("idle");
      try {
        const newGistId = await syncToGist(token, gistId, data);
        setSyncStatus("success");
        return newGistId;
      } catch (err) {
        setSyncStatus("error");
        throw err;
      } finally {
        setIsSyncing(false);
      }
    },
    [],
  );

  const download = useCallback(async (token: string, gistId: string) => {
    setIsSyncing(true);
    setSyncStatus("idle");
    try {
      const data = await loadFromGist(token, gistId);
      setSyncStatus("success");
      return data;
    } catch (err) {
      setSyncStatus("error");
      throw err;
    } finally {
      setIsSyncing(false);
    }
  }, []);

  return { isSyncing, syncStatus, resetSyncStatus, verify, upload, download };
};
