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

  const onTrayPanelCloseRequest = useCallback((callback: () => void) => {
    const bridge = netcattyBridge.get();
    return bridge?.onTrayPanelCloseRequest?.(callback);
  }, []);

  const onTrayPanelRefresh = useCallback((callback: () => void) => {
    const bridge = netcattyBridge.get();
    return bridge?.onTrayPanelRefresh?.(callback);
  }, []);

  return { hideTrayPanel, openMainWindow, onTrayPanelCloseRequest, onTrayPanelRefresh };
};
