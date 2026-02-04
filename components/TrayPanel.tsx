import React, { useCallback, useEffect, useMemo } from "react";
import { useSessionState } from "../application/state/useSessionState";
import { usePortForwardingState } from "../application/state/usePortForwardingState";
import { useVaultState } from "../application/state/useVaultState";
import { toast } from "./ui/toast";
import { cn } from "../lib/utils";
import { useI18n } from "../application/i18n/I18nProvider";
import { useTrayPanelBackend } from "../application/state/useTrayPanelBackend";
import { useActiveTabId } from "../application/state/activeTabStore";

const TrayPanel: React.FC = () => {
  const { t } = useI18n();
  const { hideTrayPanel, openMainWindow, onTrayPanelCloseRequest } = useTrayPanelBackend();

  const { hosts, keys } = useVaultState();
  const { sessions, setActiveTabId } = useSessionState();
  const { rules: portForwardingRules, startTunnel, stopTunnel } = usePortForwardingState();
  const activeTabId = useActiveTabId();

  const keysForPf = useMemo(
    () => keys.map((k) => ({ id: k.id, privateKey: k.privateKey })),
    [keys],
  );

  const handleClose = useCallback(() => {
    void hideTrayPanel();
  }, [hideTrayPanel]);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        handleClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [handleClose]);

  useEffect(() => {
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target;
      if (!(target instanceof Node)) return;
      if (document.body && !document.body.contains(target)) return;
      // Clicking on background should close panel
      const root = document.getElementById("tray-panel-root");
      if (root && !root.contains(target)) {
        handleClose();
      }
    };
    window.addEventListener("pointerdown", onPointerDown, true);
    return () => window.removeEventListener("pointerdown", onPointerDown, true);
  }, [handleClose]);

  useEffect(() => {
    const unsubscribe = onTrayPanelCloseRequest(() => {
      handleClose();
    });
    return () => unsubscribe?.();
  }, [handleClose, onTrayPanelCloseRequest]);

  const handleOpenMain = useCallback(() => {
    void openMainWindow();
  }, [openMainWindow]);

  return (
    <div id="tray-panel-root" className="w-full h-full bg-background/95 backdrop-blur border border-border/60 rounded-lg shadow-lg overflow-hidden">
      <div className="px-3 py-2 border-b border-border/60 flex items-center justify-between app-no-drag">
        <div className="text-sm font-medium">Netcatty</div>
        <button
          className="text-xs text-muted-foreground hover:text-foreground"
          onClick={handleClose}
        >
          Esc
        </button>
      </div>

      <div className="p-2 space-y-3 text-sm">
        <button
          onClick={handleOpenMain}
          className="w-full text-left px-2 py-1.5 rounded hover:bg-muted"
        >
          {t("tray.openMainWindow")}
        </button>

        {sessions.length > 0 && (
          <div>
            <div className="px-2 py-1 text-xs text-muted-foreground">{t("tray.sessions")}</div>
            <div className="space-y-1">
              {sessions.map((s) => (
                <button
                  key={s.id}
                  onClick={() => {
                    setActiveTabId(s.id);
                    void openMainWindow();
                  }}
                  className={cn(
                    "w-full text-left px-2 py-1.5 rounded hover:bg-muted",
                    s.status === "connected" ? "" : "text-muted-foreground",
                    activeTabId === s.id ? "bg-muted" : "",
                  )}
                >
                  <span className="truncate">{s.hostLabel || s.hostname}</span>
                  <span className="ml-2 text-xs text-muted-foreground">({t(`tray.status.${s.status}`)})</span>
                </button>
              ))}
            </div>
          </div>
        )}

        {portForwardingRules.length > 0 && (
          <div>
            <div className="px-2 py-1 text-xs text-muted-foreground">{t("tray.portForwarding")}</div>
            <div className="space-y-1">
              {portForwardingRules.map((rule) => {
                const isConnecting = rule.status === "connecting";
                const isActive = rule.status === "active";
                const label = rule.label || (rule.type === "dynamic"
                  ? `SOCKS:${rule.localPort}`
                  : `${rule.localPort} → ${rule.remoteHost}:${rule.remotePort}`);

                return (
                  <button
                    key={rule.id}
                    disabled={isConnecting}
                    onClick={() => {
                      const host = rule.hostId ? hosts.find((h) => h.id === rule.hostId) : undefined;
                      if (!host) {
                        toast.error(t("pf.error.hostNotFound"));
                        return;
                      }
                      if (isActive) {
                        void stopTunnel(rule.id);
                      } else {
                        void startTunnel(rule, host, keysForPf, (status, error) => {
                          if (status === "error" && error) toast.error(error);
                        }, rule.autoStart);
                      }
                    }}
                    className={cn(
                      "w-full text-left px-2 py-1.5 rounded hover:bg-muted flex items-center justify-between",
                      isConnecting ? "opacity-60" : "",
                    )}
                  >
                    <span className="truncate">{label}</span>
                    <span className="ml-2 text-xs text-muted-foreground">
                      {t(`tray.status.${rule.status}`)}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default TrayPanel;
