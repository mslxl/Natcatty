import React, { useCallback, useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { useI18n } from "../../../../application/i18n/I18nProvider";
import type { AIToolIntegrationMode } from "../../../../infrastructure/ai/types";
import { cn } from "../../../../lib/utils";
import {
  readExternalMcpStoredEnabled,
} from "../../../../application/state/useExternalMcpToggleState";
import { AI_STATE_CHANGED_EVENT } from "../../../../application/state/aiStateEvents";
import { STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED } from "../../../../infrastructure/config/storageKeys";
import { getBridge } from "./types";
import { EXTERNAL_MCP_DISCOVERY_ENV_VAR } from "./ExternalMcpCard";

type ExternalMcpStatusLite = {
  ok?: boolean;
  enabled?: boolean;
  state?: string;
  launcherPath?: string | null;
  discoveryPath?: string | null;
};

/** Build a ready-to-paste prompt so an external AI client can register Netcatty MCP itself. */
export function buildMcpOnboardingPrompt(
  launcherPath: string | null | undefined,
  discoveryPath: string | null | undefined,
): string {
  if (!launcherPath) {
    return [
      "Please connect Netcatty to this session via MCP.",
      "In the Netcatty desktop app, open Settings → AI → Tool Access, turn on External MCP,",
      "then copy the generated prompt from the External MCP card and run it here.",
      "After that, list the netcatty-external MCP tools and call get_environment to verify the connection.",
    ].join(" ");
  }
  const lines = [
    "Please register Netcatty's MCP server in your MCP client configuration:",
    `- Server name: netcatty-external`,
    `- Transport: local stdio`,
    `- Command: ${launcherPath}`,
  ];
  if (discoveryPath) {
    lines.push(`- Environment: ${EXTERNAL_MCP_DISCOVERY_ENV_VAR}=${discoveryPath}`);
  }
  lines.push(
    "After registering, list the server's tools and call get_environment to verify the connection.",
    "Keep the Netcatty desktop app running while you use these tools.",
  );
  return lines.join("\n");
}

type CopyRowProps = {
  value: string;
  label: string;
  copyLabel: string;
  copiedLabel: string;
  testId?: string;
};

const CopyRow: React.FC<CopyRowProps> = ({ value, label, copyLabel, copiedLabel, testId }) => {
  const [copied, setCopied] = useState(false);
  const canCopy = Boolean(value);

  const handleCopy = async () => {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard may be unavailable; the text stays selectable in the block.
    }
  };

  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className="group relative rounded-md border border-border/60 bg-muted/20">
        <pre
          data-testid={testId}
          className={cn(
            "max-h-40 overflow-auto whitespace-pre-wrap break-all px-3 py-2.5 pr-11 font-mono text-xs leading-5",
            !value && "text-muted-foreground",
          )}
        >
          {value}
        </pre>
        <button
          type="button"
          disabled={!canCopy}
          className="absolute right-1.5 top-1.5 flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:opacity-40"
          onClick={() => void handleCopy()}
          aria-label={copied ? copiedLabel : copyLabel}
          title={copied ? copiedLabel : copyLabel}
        >
          {copied ? <Check size={14} className="text-emerald-500" /> : <Copy size={14} />}
        </button>
      </div>
    </div>
  );
};

export const ToolAccessGuidance: React.FC<{ mode: AIToolIntegrationMode }> = ({ mode }) => {
  const { t } = useI18n();
  const [skillPath, setSkillPath] = useState<string | null>(null);
  const [commandPrefix, setCommandPrefix] = useState<string>("");
  const [mcpLauncherPath, setMcpLauncherPath] = useState<string | null>(null);
  const [mcpDiscoveryPath, setMcpDiscoveryPath] = useState<string | null>(null);

  const refreshMcpStatus = useCallback(async (): Promise<string | null | undefined> => {
    const raw = await getBridge()?.externalMcpGetStatus?.();
    const status = raw as ExternalMcpStatusLite | undefined;
    if (!status?.ok) return undefined;
    // buildStatus keeps reporting the launcher/discovery paths even while the
    // runtime is disabled or after an idle shutdown, so only trust them when
    // External MCP is actually enabled; otherwise fall back to the enable hint.
    const ready = Boolean(status.enabled);
    const launcherPath = ready ? status.launcherPath ?? null : null;
    const discoveryPath = ready ? status.discoveryPath ?? null : null;
    setMcpLauncherPath(launcherPath);
    setMcpDiscoveryPath(discoveryPath);
    return launcherPath && discoveryPath ? launcherPath : null;
  }, []);

  useEffect(() => {
    let cancelled = false;
    if (mode === "skills") {
      void getBridge()?.aiSkillsCliGetInvocation?.().then((result) => {
        if (cancelled) return;
        if (result?.ok) {
          setSkillPath(result.skillPath ?? null);
          setCommandPrefix(result.commandPrefix || "");
        }
      });
    } else {
      void refreshMcpStatus();
    }
    return () => {
      cancelled = true;
    };
  }, [mode, refreshMcpStatus]);

  useEffect(() => {
    if (mode === "skills") return;
    let retryTimer: number | undefined;
    let retries = 0;

    const refetch = async () => {
      const readyLauncherPath = await refreshMcpStatus();
      // Right after an enable toggle the runtime may still be starting and the
      // discovery file is not written yet; poll briefly so the prompt picks up
      // the launcher/discovery paths once the host is ready.
      if (retries < 3 && readExternalMcpStoredEnabled() && !readyLauncherPath) {
        retries += 1;
        retryTimer = window.setTimeout(() => {
          void refetch();
        }, 1200);
      }
    };

    const refetchOnChange = () => {
      retries = 0;
      void refetch();
    };
    const handleAIStateChanged = (event: Event) => {
      const key = (event as CustomEvent<{ key?: string }>).detail?.key;
      if (key !== STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED) return;
      refetchOnChange();
    };
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED) return;
      refetchOnChange();
    };

    window.addEventListener(AI_STATE_CHANGED_EVENT, handleAIStateChanged);
    window.addEventListener("storage", handleStorage);
    return () => {
      if (retryTimer) window.clearTimeout(retryTimer);
      window.removeEventListener(AI_STATE_CHANGED_EVENT, handleAIStateChanged);
      window.removeEventListener("storage", handleStorage);
    };
  }, [mode, refreshMcpStatus]);

  if (mode === "skills") {
    return (
      <div className="rounded-md border border-border/60 bg-background/50 p-3 space-y-2">
        <p className="text-xs text-muted-foreground leading-5">
          {t("ai.toolAccess.skills.description")}
        </p>
        <CopyRow
          value={skillPath || ""}
          label={t("ai.toolAccess.skills.file")}
          copyLabel={t("ai.externalMcp.copy")}
          copiedLabel={t("ai.externalMcp.copied")}
          testId="tool-access-skill-path"
        />
        {!skillPath ? (
          <p className="text-xs text-amber-500">{t("ai.toolAccess.skills.unavailable")}</p>
        ) : null}
        {commandPrefix ? (
          <p className="text-xs text-muted-foreground/80 font-mono break-all">{commandPrefix}</p>
        ) : null}
      </div>
    );
  }

  return (
    <div className="rounded-md border border-border/60 bg-background/50 p-3 space-y-2">
      <p className="text-xs text-muted-foreground leading-5">
        {t("ai.toolAccess.mcpPrompt.description")}
      </p>
      <CopyRow
        value={buildMcpOnboardingPrompt(mcpLauncherPath, mcpDiscoveryPath)}
        label={t("ai.toolAccess.mcpPrompt.title")}
        copyLabel={t("ai.externalMcp.copy")}
        copiedLabel={t("ai.externalMcp.copied")}
        testId="tool-access-mcp-prompt"
      />
      {!mcpLauncherPath ? (
        <p className="text-xs text-amber-500">{t("ai.toolAccess.mcpPrompt.enableHint")}</p>
      ) : null}
    </div>
  );
};
