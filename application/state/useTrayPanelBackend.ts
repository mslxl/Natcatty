import { useCallback } from "react";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";

export const useTrayPanelBackend = () => {
  const hideTrayPanel = useCallback(async () => {
    const bridge = netcattyBridge.get();
    await bridge?.hideTrayPanel?.();
  }, []);

  const openMainWindow = useCallback(async () => {
    const bridge = netcattyBridge.get();
    await bridge?.openMainWindow?.();
  }, []);

  const jumpToSession = useCallback(async (sessionId: string) => {
    const bridge = netcattyBridge.get();
    await bridge?.jumpToSessionFromTrayPanel?.(sessionId);
  }, []);

  const connectToHostFromTrayPanel = useCallback(async (hostId: string) => {
    const bridge = netcattyBridge.get();
    await bridge?.connectToHostFromTrayPanel?.(hostId);
  }, []);

  const onTrayPanelCloseRequest = useCallback((callback: () => void) => {
    const bridge = netcattyBridge.get();
    return bridge?.onTrayPanelCloseRequest?.(callback);
  }, []);

  const onTrayPanelRefresh = useCallback((callback: () => void) => {
    const bridge = netcattyBridge.get();
    return bridge?.onTrayPanelRefresh?.(callback);
  }, []);

  const onTrayPanelMenuData = useCallback(
    (
      callback: (data: {
        sessions?: Array<{ id: string; label: string; hostLabel: string; status: "connecting" | "connected" | "disconnected" }>;
        portForwardRules?: Array<{
          id: string;
          label: string;
          type: "local" | "remote" | "dynamic";
          localPort: number;
          remoteHost?: string;
          remotePort?: number;
          status: "inactive" | "connecting" | "active" | "error";
          hostId?: string;
        }>;
      }) => void,
    ) => {
      const bridge = netcattyBridge.get();
      return bridge?.onTrayPanelMenuData?.(callback);
    },
    [],
  );

  return {
    hideTrayPanel,
    openMainWindow,
    jumpToSession,
    connectToHostFromTrayPanel,
    onTrayPanelCloseRequest,
    onTrayPanelRefresh,
    onTrayPanelMenuData,
  };
};
