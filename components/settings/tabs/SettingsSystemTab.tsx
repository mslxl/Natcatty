/**
 * Settings System Tab - System information, temp file management, session logs, and global hotkey
 */
import { FileText, FolderOpen, HardDrive, Keyboard, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import React, { useCallback, useEffect, useState } from "react";
import { useI18n } from "../../../application/i18n/I18nProvider";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { SessionLogFormat, keyEventToString } from "../../../domain/models";
import { TabsContent } from "../../ui/tabs";
import { Button } from "../../ui/button";
import { Toggle, Select, SettingRow } from "../settings-ui";
import { cn } from "../../../lib/utils";

interface TempDirInfo {
  path: string;
  fileCount: number;
  totalSize: number;
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`;
}

interface SettingsSystemTabProps {
  sessionLogsEnabled: boolean;
  setSessionLogsEnabled: (enabled: boolean) => void;
  sessionLogsDir: string;
  setSessionLogsDir: (dir: string) => void;
  sessionLogsFormat: SessionLogFormat;
  setSessionLogsFormat: (format: SessionLogFormat) => void;
  toggleWindowHotkey: string;
  setToggleWindowHotkey: (hotkey: string) => void;
  closeToTray: boolean;
  setCloseToTray: (enabled: boolean) => void;
  hotkeyRegistrationError: string | null;
}

const SettingsSystemTab: React.FC<SettingsSystemTabProps> = ({
  sessionLogsEnabled,
  setSessionLogsEnabled,
  sessionLogsDir,
  setSessionLogsDir,
  sessionLogsFormat,
  setSessionLogsFormat,
  toggleWindowHotkey,
  setToggleWindowHotkey,
  closeToTray,
  setCloseToTray,
  hotkeyRegistrationError,
}) => {
  const { t } = useI18n();
  const isMac = typeof navigator !== "undefined" && /Mac/i.test(navigator.platform);

  const [tempDirInfo, setTempDirInfo] = useState<TempDirInfo | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [isClearing, setIsClearing] = useState(false);
  const [clearResult, setClearResult] = useState<{ deletedCount: number; failedCount: number } | null>(null);
  const [isRecordingHotkey, setIsRecordingHotkey] = useState(false);
  const [hotkeyError, setHotkeyError] = useState<string | null>(null);

  const loadTempDirInfo = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.getTempDirInfo) return;

    setIsLoading(true);
    try {
      const info = await bridge.getTempDirInfo();
      setTempDirInfo(info);
    } catch (err) {
      console.error("[SettingsSystemTab] Failed to get temp dir info:", err);
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    loadTempDirInfo();
  }, [loadTempDirInfo]);

  const handleClearTempFiles = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.clearTempDir) return;

    setIsClearing(true);
    setClearResult(null);
    try {
      const result = await bridge.clearTempDir();
      setClearResult(result);
      // Refresh info after clearing
      await loadTempDirInfo();
    } catch (err) {
      console.error("[SettingsSystemTab] Failed to clear temp dir:", err);
    } finally {
      setIsClearing(false);
    }
  }, [loadTempDirInfo]);

  const handleOpenTempDir = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!tempDirInfo?.path || !bridge?.openTempDir) return;
    await bridge.openTempDir();
  }, [tempDirInfo]);

  const handleSelectSessionLogsDir = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.selectSessionLogsDir) return;

    try {
      const result = await bridge.selectSessionLogsDir();
      if (result.success && result.directory) {
        setSessionLogsDir(result.directory);
      }
    } catch (err) {
      console.error("[SettingsSystemTab] Failed to select directory:", err);
    }
  }, [setSessionLogsDir]);

  const handleOpenSessionLogsDir = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!sessionLogsDir || !bridge?.openSessionLogsDir) return;

    try {
      await bridge.openSessionLogsDir(sessionLogsDir);
    } catch (err) {
      console.error("[SettingsSystemTab] Failed to open directory:", err);
    }
  }, [sessionLogsDir]);

  // Handle global toggle hotkey recording
  const cancelHotkeyRecording = useCallback(() => {
    setIsRecordingHotkey(false);
  }, []);

  const handleResetHotkey = useCallback(() => {
    // Reset to default hotkey (Ctrl+` or ⌃+` on Mac)
    const defaultHotkey = isMac ? '⌃ + `' : 'Ctrl + `';
    setToggleWindowHotkey(defaultHotkey);
    setHotkeyError(null);
  }, [isMac, setToggleWindowHotkey]);

  // Hotkey recording effect
  useEffect(() => {
    if (!isRecordingHotkey) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopPropagation();

      if (e.key === "Escape") {
        cancelHotkeyRecording();
        return;
      }

      // Ignore modifier-only keys
      if (["Meta", "Control", "Alt", "Shift"].includes(e.key)) return;

      const keyString = keyEventToString(e, isMac);
      setToggleWindowHotkey(keyString);
      setHotkeyError(null);
      cancelHotkeyRecording();
    };

    const handleClick = () => {
      cancelHotkeyRecording();
    };

    const timer = setTimeout(() => {
      window.addEventListener("click", handleClick, true);
    }, 100);

    window.addEventListener("keydown", handleKeyDown, true);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("keydown", handleKeyDown, true);
      window.removeEventListener("click", handleClick, true);
    };
  }, [isRecordingHotkey, isMac, setToggleWindowHotkey, cancelHotkeyRecording]);

  const formatOptions = [
    { value: "txt", label: t("settings.sessionLogs.formatTxt") },
    { value: "raw", label: t("settings.sessionLogs.formatRaw") },
    { value: "html", label: t("settings.sessionLogs.formatHtml") },
  ];

  return (
    <TabsContent
      value="system"
      className="data-[state=inactive]:hidden h-full flex flex-col"
    >
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-8 py-6">
        <div className="max-w-2xl space-y-8">
          {/* Header */}
          <div>
            <h2 className="text-xl font-semibold">{t("settings.system.title")}</h2>
            <p className="text-sm text-muted-foreground mt-1">
              {t("settings.system.description")}
            </p>
          </div>

          {/* Temp Directory Section */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <HardDrive size={18} className="text-muted-foreground" />
              <h3 className="text-base font-medium">{t("settings.system.tempDirectory")}</h3>
            </div>

            <div className="bg-muted/30 rounded-lg p-4 space-y-3">
              {/* Path */}
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <p className="text-sm text-muted-foreground">{t("settings.system.location")}</p>
                  <p className="text-sm font-mono mt-1 break-all">
                    {isLoading ? "..." : (tempDirInfo?.path ?? "-")}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="shrink-0"
                  onClick={handleOpenTempDir}
                  disabled={!tempDirInfo?.path}
                  title={t("settings.system.openFolder")}
                >
                  <FolderOpen size={16} />
                </Button>
              </div>

              {/* Stats */}
              <div className="flex items-center gap-6 text-sm">
                <div>
                  <span className="text-muted-foreground">{t("settings.system.fileCount")}:</span>{" "}
                  <span className="font-medium">
                    {isLoading ? "..." : (tempDirInfo?.fileCount ?? 0)}
                  </span>
                </div>
                <div>
                  <span className="text-muted-foreground">{t("settings.system.totalSize")}:</span>{" "}
                  <span className="font-medium">
                    {isLoading ? "..." : formatBytes(tempDirInfo?.totalSize ?? 0)}
                  </span>
                </div>
              </div>

              {/* Actions */}
              <div className="flex items-center gap-2 pt-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={loadTempDirInfo}
                  disabled={isLoading}
                  className="gap-1.5"
                >
                  <RefreshCw size={14} className={isLoading ? "animate-spin" : ""} />
                  {t("settings.system.refresh")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleClearTempFiles}
                  disabled={isClearing || (tempDirInfo?.fileCount ?? 0) === 0}
                  className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10"
                >
                  <Trash2 size={14} />
                  {isClearing ? t("settings.system.clearing") : t("settings.system.clearTempFiles")}
                </Button>
              </div>

              {/* Clear Result */}
              {clearResult && (
                <p className="text-sm text-muted-foreground">
                  {t("settings.system.clearResult", {
                    deleted: clearResult.deletedCount,
                    failed: clearResult.failedCount,
                  })}
                </p>
              )}
            </div>

            <p className="text-xs text-muted-foreground">
              {t("settings.system.tempDirectoryHint")}
            </p>
          </div>

          {/* Session Logs Section */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <FileText size={18} className="text-muted-foreground" />
              <h3 className="text-base font-medium">{t("settings.sessionLogs.title")}</h3>
            </div>

            <div className="bg-muted/30 rounded-lg p-4 space-y-4">
              {/* Enable Toggle */}
              <SettingRow
                label={t("settings.sessionLogs.enableAutoSave")}
                description={t("settings.sessionLogs.enableAutoSaveDesc")}
              >
                <Toggle
                  checked={sessionLogsEnabled}
                  onChange={setSessionLogsEnabled}
                />
              </SettingRow>

              {/* Directory Selection */}
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium">{t("settings.sessionLogs.directory")}</span>
                </div>
                <div className="flex items-center gap-2">
                  <div className="flex-1 min-w-0">
                    <div className="bg-background border border-input rounded-md px-3 py-2 text-sm font-mono truncate">
                      {sessionLogsDir || t("settings.sessionLogs.noDirectory")}
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={handleSelectSessionLogsDir}
                    className="shrink-0"
                  >
                    {t("settings.sessionLogs.browse")}
                  </Button>
                  {sessionLogsDir && (
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={handleOpenSessionLogsDir}
                      className="shrink-0"
                      title={t("settings.sessionLogs.openFolder")}
                    >
                      <FolderOpen size={16} />
                    </Button>
                  )}
                </div>
                <p className="text-xs text-muted-foreground">
                  {t("settings.sessionLogs.directoryHint")}
                </p>
              </div>

              {/* Format Selection */}
              <SettingRow
                label={t("settings.sessionLogs.format")}
                description={t("settings.sessionLogs.formatDesc")}
              >
                <Select
                  value={sessionLogsFormat}
                  options={formatOptions}
                  onChange={(val) => setSessionLogsFormat(val as SessionLogFormat)}
                  className="w-32"
                  disabled={!sessionLogsEnabled}
                />
              </SettingRow>
            </div>

            <p className="text-xs text-muted-foreground">
              {t("settings.sessionLogs.hint")}
            </p>
          </div>

          {/* Global Toggle Window Section (Quake Mode) */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <Keyboard size={18} className="text-muted-foreground" />
              <h3 className="text-base font-medium">{t("settings.globalHotkey.title")}</h3>
            </div>

            <div className="bg-muted/30 rounded-lg p-4 space-y-4">
              {/* Toggle Window Hotkey */}
              <SettingRow
                label={t("settings.globalHotkey.toggleWindow")}
                description={t("settings.globalHotkey.toggleWindowDesc")}
              >
                <div className="flex items-center gap-2">
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      setIsRecordingHotkey(true);
                    }}
                    className={cn(
                      "px-3 py-1.5 text-sm font-mono rounded border transition-colors min-w-[100px] text-center",
                      isRecordingHotkey
                        ? "border-primary bg-primary/10 animate-pulse"
                        : "border-border hover:border-primary/50",
                    )}
                  >
                    {isRecordingHotkey
                      ? t("settings.shortcuts.recording")
                      : toggleWindowHotkey || t("settings.globalHotkey.notSet")}
                  </button>
                  {toggleWindowHotkey && (
                    <button
                      onClick={handleResetHotkey}
                      className="p-1 hover:bg-muted rounded"
                      title={t("settings.globalHotkey.reset")}
                    >
                      <RotateCcw size={14} />
                    </button>
                  )}
                </div>
              </SettingRow>
              {(hotkeyError || hotkeyRegistrationError) && (
                <p className="text-sm text-destructive">{hotkeyError || hotkeyRegistrationError}</p>
              )}

              {/* Close to Tray */}
              <SettingRow
                label={t("settings.globalHotkey.closeToTray")}
                description={t("settings.globalHotkey.closeToTrayDesc")}
              >
                <Toggle
                  checked={closeToTray}
                  onChange={setCloseToTray}
                />
              </SettingRow>
            </div>

            <p className="text-xs text-muted-foreground">
              {t("settings.globalHotkey.hint")}
            </p>
          </div>
        </div>
      </div>
    </TabsContent>
  );
};

export default SettingsSystemTab;
