import React from "react";
import { AlertTriangle, Fingerprint } from "lucide-react";
import type { KnownHost } from "../../domain/models";
import { usePortForwardHostKeyVerification } from "../../application/state/usePortForwardHostKeyVerification";
import { useI18n } from "../../application/i18n/I18nProvider";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";

interface PortForwardHostKeyTrayPromptProps {
  onAddKnownHost?: (knownHost: KnownHost) => void;
}

export const PortForwardHostKeyTrayPrompt: React.FC<PortForwardHostKeyTrayPromptProps> = ({
  onAddKnownHost,
}) => {
  const { t } = useI18n();
  const {
    hostKeyVerification,
    rejectHostKeyVerification,
    acceptHostKeyVerification,
    acceptAndSaveHostKeyVerification,
  } = usePortForwardHostKeyVerification(onAddKnownHost);

  if (!hostKeyVerification) return null;

  const { hostKeyInfo } = hostKeyVerification;
  const isChanged = hostKeyInfo.status === "changed";
  const Icon = isChanged ? AlertTriangle : Fingerprint;

  return (
    <div
      data-port-forward-host-key-tray-prompt="true"
      className={cn(
        "rounded-md border p-2.5 shadow-sm",
        isChanged
          ? "border-destructive/30 bg-destructive/8"
          : "border-amber-500/25 bg-amber-500/8",
      )}
    >
      <div className="flex items-start gap-2">
        <div
          className={cn(
            "mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md",
            isChanged
              ? "bg-destructive/15 text-destructive"
              : "bg-amber-500/15 text-amber-400",
          )}
        >
          <Icon size={15} />
        </div>
        <div className="min-w-0 flex-1">
          <div
            className={cn(
              "truncate text-sm font-semibold",
              isChanged ? "text-destructive" : "text-amber-400",
            )}
          >
            {isChanged
              ? t("terminal.hostKey.changedTitle")
              : t("terminal.hostKey.unknownTitle")}
          </div>
          <div className="truncate text-xs text-muted-foreground">
            {hostKeyInfo.hostname}:{hostKeyInfo.port}
          </div>
        </div>
      </div>

      <div className="mt-2 rounded-md border border-border/50 bg-background/55 p-2">
        <div className="mb-1 text-[10px] text-muted-foreground">
          {t("terminal.hostKey.fingerprintLabel", { keyType: hostKeyInfo.keyType })}
        </div>
        <code className="block break-all font-mono text-[11px] leading-4 text-foreground/90">
          {hostKeyInfo.fingerprint}
        </code>
      </div>

      {isChanged && hostKeyInfo.knownFingerprint && (
        <div className="mt-2 rounded-md border border-destructive/25 bg-destructive/8 p-2">
          <div className="mb-1 text-[10px] font-medium text-destructive">
            {t("terminal.hostKey.savedFingerprintLabel")}
          </div>
          <code className="block break-all font-mono text-[11px] leading-4 text-foreground/90">
            {hostKeyInfo.knownFingerprint}
          </code>
        </div>
      )}

      <div className="mt-2 grid grid-cols-[auto_auto_1fr] gap-1.5">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-[11px]"
          onClick={rejectHostKeyVerification}
        >
          {t("common.close")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2 text-[11px]"
          onClick={acceptHostKeyVerification}
        >
          {t("common.continue")}
        </Button>
        <Button
          size="sm"
          className="h-7 min-w-0 px-2 text-[11px]"
          onClick={acceptAndSaveHostKeyVerification}
        >
          <span className="truncate">
            {isChanged
              ? t("terminal.hostKey.updateAndContinue")
              : t("terminal.hostKey.addAndContinue")}
          </span>
        </Button>
      </div>
    </div>
  );
};
