import { useCallback } from "react";
import type { MutableRefObject } from "react";
import type { Host } from "../../domain/models";
import type { SftpPane } from "./types";

interface UseSftpSessionErrorsParams {
  getActivePane: (side: "left" | "right") => SftpPane | null;
  leftTabsRef: MutableRefObject<{ tabs: SftpPane[]; activeTabId: string | null }>;
  rightTabsRef: MutableRefObject<{ tabs: SftpPane[]; activeTabId: string | null }>;
  updateActiveTab: (
    side: "left" | "right",
    updater: (prev: SftpPane) => SftpPane,
  ) => void;
  sftpSessionsRef: MutableRefObject<Map<string, string>>;
  clearCacheForConnection: (connectionId: string) => void;
  navSeqRef: MutableRefObject<{ left: number; right: number }>;
  lastConnectedHostRef: MutableRefObject<{ left: Host | "local" | null; right: Host | "local" | null }>;
  reconnectingRef: MutableRefObject<{ left: boolean; right: boolean }>;
}

export const useSftpSessionErrors = ({
  getActivePane,
  leftTabsRef,
  rightTabsRef,
  updateActiveTab,
  sftpSessionsRef,
  clearCacheForConnection,
  navSeqRef,
  lastConnectedHostRef,
  reconnectingRef,
}: UseSftpSessionErrorsParams) =>
  useCallback(
    (side: "left" | "right", _error: Error) => {
      const pane = getActivePane(side);
      const sideTabs = side === "left" ? leftTabsRef.current : rightTabsRef.current;

      if (!pane || !sideTabs.activeTabId) return;

      if (pane.connection) {
        sftpSessionsRef.current.delete(pane.connection.id);
        clearCacheForConnection(pane.connection.id);
      }

      navSeqRef.current[side] += 1;

      const lastHost = lastConnectedHostRef.current[side];
      if (lastHost && pane.files.length > 0 && !reconnectingRef.current[side]) {
        reconnectingRef.current[side] = true;
        updateActiveTab(side, (prev) => ({
          ...prev,
          reconnecting: true,
          error: "sftp.error.connectionLostReconnecting",
        }));
      } else {
        updateActiveTab(side, (prev) => ({
          ...prev,
          connection: null,
          files: [],
          loading: false,
          reconnecting: false,
          error: "sftp.error.sessionLost",
          selectedFiles: new Set(),
          filter: "",
        }));
      }
    },
    [
      getActivePane,
      leftTabsRef,
      rightTabsRef,
      updateActiveTab,
      sftpSessionsRef,
      clearCacheForConnection,
      navSeqRef,
      lastConnectedHostRef,
      reconnectingRef,
    ],
  );
