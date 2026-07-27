import React, { useCallback, useMemo, useRef, useState } from "react";
import { FileSymlink, Import, Plug } from "lucide-react";
import { useI18n } from "../../application/i18n/I18nProvider";
import type { VaultImportFileEncoding } from "../../application/state/vaultImportFile";
import { usePluginVaultImporter } from "../../application/state/usePluginVaultImporter";
import { getVaultCsvTemplate } from "../../domain/vaultImport";
import type { VaultImportFormat } from "../../domain/vaultImport";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";

type ImportOption = {
  format: VaultImportFormat;
  label: string;
  iconSrc: string;
  accept: string;
};

const OPTIONS: ImportOption[] = [
  {
    format: "putty",
    label: "PuTTY",
    iconSrc: "/import/putty.png",
    accept: ".reg,.txt,.ini",
  },
  {
    format: "mobaxterm",
    label: "MobaXterm",
    iconSrc: "/import/moba.jpg",
    accept: ".ini,.mxtsessions,.txt",
  },
  {
    format: "csv",
    label: "CSV",
    iconSrc: "/import/csv.png",
    accept: ".csv,.txt",
  },
  {
    format: "securecrt",
    label: "SecureCRT",
    iconSrc: "/import/securecrt.png",
    accept: ".ini,.txt",
  },
  {
    format: "ssh_config",
    label: "ssh_config",
    iconSrc: "/import/file.png",
    accept: "*",
  },
];

export type ImportOptions = {
  managed?: boolean;
  filePath?: string;
  encoding?: VaultImportFileEncoding;
};

export type ImportVaultDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onFileSelected: (format: VaultImportFormat, file: File, options?: ImportOptions) => void;
  onPluginPreviewCommit: (preview: NetcattyPluginImporterPreview) => Promise<void> | void;
  getPluginPreviewAnalysis: (preview: NetcattyPluginImporterPreview) => {
    duplicateCount: number;
    validationErrorCount: number;
    safePreview: import('../../domain/pluginImporter').PluginImporterSafePreview;
  };
};

export const ImportVaultDialog: React.FC<ImportVaultDialogProps> = ({
  open,
  onOpenChange,
  onFileSelected,
  onPluginPreviewCommit,
  getPluginPreviewAnalysis,
}) => {
  const { t } = useI18n();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const pendingFormatRef = useRef<VaultImportFormat | null>(null);
  const pendingOptionsRef = useRef<ImportOptions | undefined>(undefined);
  const [showManagedChoice, setShowManagedChoice] = useState(false);
  const [showMobaEncodingChoice, setShowMobaEncodingChoice] = useState(false);
  const pluginImporter = usePluginVaultImporter({
    open,
    onOpenChange,
    onPluginPreviewCommit,
    t,
  });

  const downloadCsvTemplate = useCallback(() => {
    const csv = getVaultCsvTemplate();
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "netcatty-vault-template.csv";
    a.click();
    URL.revokeObjectURL(url);
  }, []);

  const pickFile = useCallback(
    (format: VaultImportFormat, accept: string, options?: ImportOptions) => {
      const input = fileInputRef.current;
      if (!input) return;
      pendingFormatRef.current = format;
      pendingOptionsRef.current = options;
      input.accept = accept;
      input.value = "";
      input.click();
    },
    [],
  );

  const handleFormatClick = useCallback(
    (opt: ImportOption) => {
      if (opt.format === "ssh_config") {
        setShowManagedChoice(true);
      } else if (opt.format === "mobaxterm") {
        setShowMobaEncodingChoice(true);
      } else {
        pickFile(opt.format, opt.accept);
      }
    },
    [pickFile],
  );

  const handleManagedChoice = useCallback(
    (managed: boolean) => {
      setShowManagedChoice(false);
      pickFile("ssh_config", "*", { managed });
    },
    [pickFile],
  );

  const handleMobaEncodingChoice = useCallback(
    (encoding: VaultImportFileEncoding) => {
      setShowMobaEncodingChoice(false);
      pickFile("mobaxterm", ".ini,.mxtsessions,.txt", { encoding });
    },
    [pickFile],
  );

  const onChangeFile = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0];
      const format = pendingFormatRef.current;
      const options = pendingOptionsRef.current;
      if (!file) return;
      if (format) {
        onFileSelected(format, file, options);
      }
      e.target.value = "";
      pendingOptionsRef.current = undefined;
    },
    [onFileSelected],
  );

  const handleOpenChange = useCallback(
    (newOpen: boolean) => {
      if (!newOpen) {
        setShowManagedChoice(false);
        setShowMobaEncodingChoice(false);
      }
      pluginImporter.handleOpenChange(newOpen);
    },
    [pluginImporter],
  );

  const previewAnalysis = useMemo(
    () => pluginImporter.pluginPreview
      ? getPluginPreviewAnalysis(pluginImporter.pluginPreview)
      : {
        duplicateCount: 0,
        validationErrorCount: 0,
        safePreview: { items: [], warnings: [], errors: [], omittedItemCount: 0, omittedDiagnosticCount: 0 },
      },
    [getPluginPreviewAnalysis, pluginImporter.pluginPreview],
  );

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader className="text-center sm:text-center">
          <div className="mx-auto h-14 w-14 rounded-2xl bg-muted/60 border border-border/60 flex items-center justify-center">
            <img
              src="/import/file.png"
              alt=""
              className="h-8 w-8 object-contain"
            />
          </div>
          <DialogTitle className="text-xl">{t("vault.import.title")}</DialogTitle>
          <DialogDescription className="mx-auto max-w-xl">
            {showManagedChoice
              ? t("vault.import.sshConfig.chooseMode")
              : showMobaEncodingChoice
                ? t("vault.import.mobaxterm.chooseEncoding")
                : t("vault.import.desc")}
          </DialogDescription>
        </DialogHeader>

        <input
          ref={fileInputRef}
          type="file"
          className="hidden"
          onChange={onChangeFile}
        />

        <div className="flex flex-col gap-4">
          {pluginImporter.pluginPreview ? (
            <div className="space-y-4">
              <div className="rounded-xl border border-border/60 bg-muted/20 p-4">
                <div className="text-sm font-medium">{t('vault.import.plugins.preview')}</div>
                <div className="mt-1 text-sm text-muted-foreground">{pluginImporter.previewSummary || t('vault.import.plugins.empty')}</div>
                <div className="mt-2 text-xs text-muted-foreground">
                  {t('vault.import.plugins.summary', {
                    parsed: pluginImporter.pluginPreview.result.parsed,
                    warnings: pluginImporter.pluginPreview.result.warnings,
                    errors: pluginImporter.pluginPreview.result.errors,
                  })}
                </div>
                {previewAnalysis.duplicateCount > 0 ? (
                  <div className="mt-1 text-xs text-muted-foreground">
                    {t('vault.import.plugins.duplicates', { count: previewAnalysis.duplicateCount })}
                  </div>
                ) : null}
                {previewAnalysis.validationErrorCount > 0 ? (
                  <div className="mt-1 text-xs text-destructive">
                    {t('vault.import.plugins.validationErrors', { count: previewAnalysis.validationErrorCount })}
                  </div>
                ) : null}
                {previewAnalysis.safePreview.items.length > 0 ? (
                  <div className="mt-3 max-h-56 space-y-1 overflow-y-auto rounded-lg border border-border/50 bg-background/60 p-2">
                    {previewAnalysis.safePreview.items.map((item, index) => (
                      <div key={`${item.kind}:${index}`} className="flex min-w-0 items-start gap-2 text-xs">
                        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] uppercase text-muted-foreground">
                          {t(`vault.import.plugins.kind.${item.kind}`)}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-medium text-foreground">{item.label}</span>
                          {item.detail ? <span className="block truncate text-muted-foreground">{item.detail}</span> : null}
                        </span>
                      </div>
                    ))}
                    {previewAnalysis.safePreview.omittedItemCount > 0 ? (
                      <div className="pt-1 text-xs text-muted-foreground">
                        {t('vault.import.plugins.moreItems', { count: previewAnalysis.safePreview.omittedItemCount })}
                      </div>
                    ) : null}
                  </div>
                ) : null}
                {[...previewAnalysis.safePreview.warnings, ...previewAnalysis.safePreview.errors].length > 0 ? (
                  <div className="mt-3 space-y-1" role="status">
                    {previewAnalysis.safePreview.warnings.map((message, index) => (
                      <div key={`warning:${index}`} className="text-xs text-amber-600 dark:text-amber-400">{message}</div>
                    ))}
                    {previewAnalysis.safePreview.errors.map((message, index) => (
                      <div key={`error:${index}`} className="text-xs text-destructive">{message}</div>
                    ))}
                    {previewAnalysis.safePreview.omittedDiagnosticCount > 0 ? (
                      <div className="text-xs text-muted-foreground">
                        {t('vault.import.plugins.moreDiagnostics', { count: previewAnalysis.safePreview.omittedDiagnosticCount })}
                      </div>
                    ) : null}
                  </div>
                ) : null}
              </div>
              <div className="flex justify-end gap-2">
                <Button variant="outline" onClick={pluginImporter.clearPluginPreview}>{t('common.back')}</Button>
                <Button
                  disabled={pluginImporter.pluginPreview.result.errors > 0 || previewAnalysis.validationErrorCount > 0 || pluginImporter.pluginBusy}
                  onClick={() => void pluginImporter.commitPluginPreview()}
                >
                  {t('vault.import.plugins.commit')}
                </Button>
              </div>
            </div>
          ) : showManagedChoice ? (
            <>
              <div className="text-sm font-medium text-center text-muted-foreground">
                {t("vault.import.sshConfig.modeQuestion")}
              </div>
              <div className="grid grid-cols-2 gap-4">
                <button
                  type="button"
                  className={cn(
                    "group rounded-2xl border border-border/60 bg-background",
                    "px-4 py-6 hover:bg-muted/30 hover:border-border transition-colors",
                    "flex flex-col items-center gap-3",
                  )}
                  onClick={() => handleManagedChoice(false)}
                >
                  <div className="h-12 w-12 rounded-xl bg-muted/60 flex items-center justify-center">
                    <Import className="h-6 w-6 text-muted-foreground" />
                  </div>
                  <div className="text-sm font-medium text-foreground">
                    {t("vault.import.sshConfig.importOnly")}
                  </div>
                  <div className="text-xs text-muted-foreground text-center">
                    {t("vault.import.sshConfig.importOnlyDesc")}
                  </div>
                </button>
                <button
                  type="button"
                  className={cn(
                    "group rounded-2xl border border-primary/60 bg-primary/5",
                    "px-4 py-6 hover:bg-primary/10 hover:border-primary transition-colors",
                    "flex flex-col items-center gap-3",
                  )}
                  onClick={() => handleManagedChoice(true)}
                >
                  <div className="h-12 w-12 rounded-xl bg-primary/10 flex items-center justify-center">
                    <FileSymlink className="h-6 w-6 text-primary" />
                  </div>
                  <div className="text-sm font-medium text-foreground">
                    {t("vault.import.sshConfig.managed")}
                  </div>
                  <div className="text-xs text-muted-foreground text-center">
                    {t("vault.import.sshConfig.managedDesc")}
                  </div>
                </button>
              </div>
              <button
                type="button"
                onClick={() => setShowManagedChoice(false)}
                className="text-xs text-muted-foreground hover:text-foreground"
              >
                {t("common.back")}
              </button>
            </>
          ) : showMobaEncodingChoice ? (
            <>
              <div className="text-sm font-medium text-center text-muted-foreground">
                {t("vault.import.mobaxterm.encodingQuestion")}
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                {([
                  ["auto", "auto", "autoDesc", true],
                  ["utf-8", "utf8", "utf8Desc", false],
                  ["gb18030", "gb18030", "gb18030Desc", false],
                ] as const).map(([encoding, labelKey, descKey, recommended]) => (
                  <button
                    key={encoding}
                    type="button"
                    className={cn(
                      "group rounded-2xl border bg-background px-4 py-5 transition-colors",
                      "flex flex-col items-center gap-3",
                      recommended
                        ? "border-primary/60 bg-primary/5 hover:bg-primary/10 hover:border-primary"
                        : "border-border/60 hover:bg-muted/30 hover:border-border",
                    )}
                    onClick={() => handleMobaEncodingChoice(encoding)}
                  >
                    <div className={cn(
                      "h-12 w-12 rounded-xl flex items-center justify-center",
                      recommended ? "bg-primary/10" : "bg-muted/60",
                    )}>
                      <Import className={cn(
                        "h-6 w-6",
                        recommended ? "text-primary" : "text-muted-foreground",
                      )} />
                    </div>
                    <div className="text-sm font-medium text-foreground">
                      {t(`vault.import.mobaxterm.${labelKey}`)}
                    </div>
                    <div className="text-xs text-muted-foreground text-center">
                      {t(`vault.import.mobaxterm.${descKey}`)}
                    </div>
                  </button>
                ))}
              </div>
              <button
                type="button"
                onClick={() => setShowMobaEncodingChoice(false)}
                className="text-xs text-muted-foreground hover:text-foreground"
              >
                {t("common.back")}
              </button>
            </>
          ) : (
            <>
              <div className="text-sm font-medium text-center text-muted-foreground">
                {t("vault.import.chooseFormat")}
              </div>

              <div className="grid grid-cols-2 sm:grid-cols-5 gap-3">
                {OPTIONS.map((opt) => (
                  <button
                    key={opt.format}
                    type="button"
                    className={cn(
                      "group rounded-2xl border border-border/60 bg-background",
                      "px-3 py-4 hover:bg-muted/30 hover:border-border transition-colors",
                      "flex flex-col items-center gap-3",
                    )}
                    onClick={() => handleFormatClick(opt)}
                  >
                    <div className="h-16 flex items-center justify-center">
                      <img
                        src={opt.iconSrc}
                        alt=""
                        className={cn(
                          "max-h-12 w-14 object-contain",
                          opt.format === "mobaxterm" && "w-16",
                        )}
                      />
                    </div>
                    <div className="text-sm font-medium text-foreground">
                      {opt.label}
                    </div>
                  </button>
                ))}
              </div>

              {pluginImporter.pluginProviders.length > 0 && (
                <>
                  <div className="pt-2 text-sm font-medium text-center text-muted-foreground">
                    {t('vault.import.plugins.title')}
                  </div>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    {pluginImporter.pluginProviders.map((provider) => (
                      <button
                        key={provider.provider.id}
                        type="button"
                        disabled={pluginImporter.pluginBusy}
                        className="flex items-center gap-3 rounded-xl border border-border/60 p-3 text-left transition-colors hover:bg-muted/30 disabled:opacity-50"
                        onClick={() => pluginImporter.pickPluginFile(provider)}
                      >
                        <span className="rounded-lg bg-primary/10 p-2 text-primary"><Plug className="h-5 w-5" /></span>
                        <span className="min-w-0">
                          <span className="block truncate text-sm font-medium">{pluginImporter.localizeProviderLabel(provider)}</span>
                          <span className="block truncate text-xs text-muted-foreground">{provider.pluginDisplayName || provider.provider.id}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </>
              )}
              {pluginImporter.pluginBusy && (
                <div className="text-center text-sm text-muted-foreground" role="status">
                  {pluginImporter.pluginProgress
                    ? t('vault.import.plugins.progress', {
                      completed: pluginImporter.pluginProgress.completed,
                      total: pluginImporter.pluginProgress.total ?? '?',
                      message: pluginImporter.pluginProgress.message ?? '',
                    })
                    : t('vault.import.plugins.loading')}
                </div>
              )}
              {pluginImporter.pluginError && <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">{pluginImporter.pluginError}</div>}

              <div className="flex items-center justify-between gap-3 pt-2 border-t border-border/60">
                <div className="text-xs text-muted-foreground">
                  {t("vault.import.csv.tip")}
                </div>
                <button
                  type="button"
                  onClick={downloadCsvTemplate}
                  className="text-xs text-primary hover:underline"
                >
                  {t("vault.import.csv.downloadTemplate")}
                </button>
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
};
