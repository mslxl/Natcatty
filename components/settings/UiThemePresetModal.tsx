import React, { useCallback, useEffect } from "react";
import { createPortal } from "react-dom";
import { Check, Palette, X } from "lucide-react";
import { useI18n } from "../../application/i18n/I18nProvider";
import type { UiThemePreset } from "../../infrastructure/config/uiThemes";
import { cn } from "../../lib/utils";

type UiThemePresetModalProps = {
  open: boolean;
  onClose: () => void;
  presets: UiThemePreset[];
  selectedThemeId: string;
  onSelect: (themeId: string) => void;
};

const hsl = (value: string, alpha?: number) => (
  alpha === undefined ? `hsl(${value})` : `hsl(${value} / ${alpha})`
);

const PreviewLine = ({
  color,
  width,
  alpha = 0.22,
}: {
  color: string;
  width: string;
  alpha?: number;
}) => (
  <span
    className="block h-1.5 rounded-full"
    style={{ width, backgroundColor: hsl(color, alpha) }}
  />
);

const ThemePreview = ({ preset }: { preset: UiThemePreset }) => {
  const { tokens } = preset;
  const preview = preset.preview ?? {
    sidebar: tokens.secondary,
    activity: [tokens.destructive, tokens.primary, tokens.secondary, tokens.ring],
    syntax: [tokens.primary, tokens.destructive, tokens.accent, tokens.ring],
  };
  const activity = preview.activity.slice(0, 5);
  const syntax = preview.syntax.slice(0, 4);

  return (
    <div
      className="h-32 overflow-hidden rounded-t-lg border-b"
      style={{
        backgroundColor: hsl(tokens.card),
        borderColor: hsl(tokens.border),
      }}
    >
      <div className="grid h-[116px] grid-cols-[36%_64%]">
        <div
          className="border-r px-3 py-3"
          style={{
            backgroundColor: hsl(preview.sidebar, 0.58),
            borderColor: hsl(tokens.border),
          }}
        >
          <div className="mb-5 flex gap-1.5">
            {[0, 1, 2].map((dot) => (
              <span
                key={dot}
                className="h-1.5 w-1.5 rounded-full"
                style={{ backgroundColor: hsl(tokens.mutedForeground, 0.42) }}
              />
            ))}
          </div>
          <div className="space-y-2.5">
            <PreviewLine color={tokens.foreground} width="72%" alpha={0.08} />
            <PreviewLine color={tokens.foreground} width="52%" alpha={0.08} />
            <PreviewLine color={tokens.foreground} width="40%" alpha={0.08} />
          </div>
          <div className="mt-6 space-y-2.5">
            <PreviewLine color={tokens.foreground} width="58%" alpha={0.1} />
            <div
              className="flex items-center justify-between rounded-sm px-1.5 py-1"
              style={{ backgroundColor: hsl(tokens.foreground, 0.07) }}
            >
              <PreviewLine color={tokens.foreground} width="68%" alpha={0.22} />
              <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: hsl(tokens.primary) }} />
            </div>
          </div>
        </div>
        <div className="px-4 py-3">
          <div className="mb-5 flex items-center justify-between">
            <div className="flex items-center gap-1.5">
              <span className="h-2 w-5 rounded-sm" style={{ backgroundColor: hsl(tokens.primary) }} />
              <PreviewLine color={tokens.foreground} width="64px" alpha={0.28} />
            </div>
            <span className="h-2 w-8 rounded-sm" style={{ backgroundColor: hsl(tokens.accent, 0.82) }} />
          </div>
          <div className="space-y-2.5">
            <PreviewLine color={tokens.foreground} width="94%" alpha={0.12} />
            <PreviewLine color={tokens.foreground} width="70%" alpha={0.12} />
          </div>
          <div className="my-5 space-y-2">
            <div className="flex h-1.5 gap-1.5">
              <span className="w-8 rounded-full" style={{ backgroundColor: hsl(syntax[1] ?? tokens.destructive, 0.58) }} />
              <span className="w-12 rounded-full" style={{ backgroundColor: hsl(syntax[1] ?? tokens.destructive, 0.58) }} />
              <span className="flex-1" style={{ backgroundColor: hsl(tokens.destructive, 0.08) }} />
            </div>
            <div className="flex h-1.5 gap-1.5">
              <span className="w-7 rounded-full" style={{ backgroundColor: hsl(syntax[2] ?? tokens.accent, 0.58) }} />
              <span className="w-14 rounded-full" style={{ backgroundColor: hsl(syntax[2] ?? tokens.accent, 0.58) }} />
              <span className="flex-1" style={{ backgroundColor: hsl(tokens.accent, 0.08) }} />
            </div>
          </div>
          <div className="space-y-2.5">
            <PreviewLine color={tokens.foreground} width="94%" alpha={0.12} />
            <PreviewLine color={tokens.foreground} width="55%" alpha={0.12} />
          </div>
        </div>
      </div>
      <div className="grid h-1 grid-flow-col auto-cols-fr">
        {activity.map((stripe, index) => (
          <span key={`${preset.id}-${index}`} style={{ backgroundColor: hsl(stripe) }} />
        ))}
      </div>
    </div>
  );
};

export function UiThemePresetModal({
  open,
  onClose,
  presets,
  selectedThemeId,
  onSelect,
}: UiThemePresetModalProps) {
  const { t } = useI18n();
  const handleThemeSelect = useCallback((themeId: string) => {
    onSelect(themeId);
    onClose();
  }, [onClose, onSelect]);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose, open]);

  const handleBackdropClick = useCallback((event: React.MouseEvent) => {
    if (event.target === event.currentTarget) onClose();
  }, [onClose]);

  if (!open) return null;

  const modalTitleId = "ui-theme-preset-modal-title";

  return createPortal(
    <div
      className="fixed inset-0 flex items-center justify-center bg-black/55 px-6 py-8"
      style={{ zIndex: 99999 }}
      role="dialog"
      aria-modal="true"
      aria-labelledby={modalTitleId}
      onClick={handleBackdropClick}
    >
      <div
        className="flex max-h-full w-full max-w-5xl flex-col overflow-hidden rounded-lg border border-border bg-background shadow-2xl animate-in fade-in zoom-in-95 duration-200"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-border px-5 py-3">
          <div className="flex items-center gap-3">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <Palette size={16} />
            </div>
            <h2 id={modalTitleId} className="text-sm font-semibold text-foreground">
              {t("settings.appearance.themePresets.modalTitle")}
            </h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label={t("common.close")}
          >
            <X size={16} />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-5">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {presets.map((preset) => {
              const selected = selectedThemeId === preset.id;
              return (
                <button
                  key={preset.id}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => handleThemeSelect(preset.id)}
                  className={cn(
                    "overflow-hidden rounded-lg border bg-card text-left shadow-sm transition-all hover:-translate-y-0.5 hover:border-primary/70 hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    selected ? "border-primary ring-2 ring-primary/25" : "border-border",
                  )}
                >
                  <ThemePreview preset={preset} />
                  <div className="flex h-14 items-center justify-between px-4">
                    <span className="min-w-0 truncate text-base font-medium text-foreground">
                      {preset.name}
                    </span>
                    {selected && (
                      <span className="ml-3 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
                        <Check size={14} />
                      </span>
                    )}
                  </div>
                </button>
              );
            })}
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}

export default UiThemePresetModal;
