import {
  Copy,
  Download,
  FileCode,
  FileText,
  Share2,
} from "lucide-react";
import React, { useState, useRef, useEffect } from "react";
import { useI18n } from "../../application/i18n/I18nProvider";
import {
  buildVaultNoteMarkdownExportFiles,
  sanitizeNoteExportFileNamePart,
  type VaultNote,
} from "../../domain/notes";
import { copyToClipboard } from "../keychain/utils";
import { toast } from "../ui/toast";
import { buildTextFilesZipBlob } from "../../lib/textZip";

export interface NoteExportMenuProps {
  note: VaultNote | null;
  allNotes: VaultNote[];
  className?: string;
}

export const NoteExportMenu: React.FC<NoteExportMenuProps> = ({
  note,
  allNotes,
  className = "",
}) => {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    window.addEventListener("mousedown", handleClickOutside);
    return () => window.removeEventListener("mousedown", handleClickOutside);
  }, [open]);

  const handleExportSingleMarkdown = () => {
    if (!note) return;
    const fileName = `${sanitizeNoteExportFileNamePart(note.title, "note")}.md`;
    const blob = new Blob([note.content], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = fileName;
    a.click();
    URL.revokeObjectURL(url);
    toast.success(t("notes.export.toast.markdownSuccess"));
    setOpen(false);
  };

  const handleExportSingleHtml = () => {
    if (!note) return;
    const title = note.title || "Untitled Note";
    const escapedTitle = title.replace(/</g, "&lt;").replace(/>/g, "&gt;");
    const htmlContent = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <title>${escapedTitle}</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; line-height: 1.6; max-width: 800px; margin: 40px auto; padding: 0 20px; color: #333; }
    h1, h2, h3 { color: #111; }
    pre { background: #f4f4f5; padding: 16px; border-radius: 8px; overflow-x: auto; }
    code { font-family: monospace; background: #f4f4f5; padding: 2px 6px; border-radius: 4px; }
    pre code { background: none; padding: 0; }
    blockquote { border-left: 4px solid #e4e4e7; margin: 0; padding-left: 16px; color: #71717a; }
  </style>
</head>
<body>
  <h1>${escapedTitle}</h1>
  <pre style="white-space: pre-wrap;">${note.content.replace(/</g, "&lt;").replace(/>/g, "&gt;")}</pre>
</body>
</html>`;
    const fileName = `${sanitizeNoteExportFileNamePart(note.title, "note")}.html`;
    const blob = new Blob([htmlContent], { type: "text/html;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = fileName;
    a.click();
    URL.revokeObjectURL(url);
    toast.success(t("notes.export.toast.htmlSuccess"));
    setOpen(false);
  };

  const handleCopyMarkdown = async () => {
    if (!note) return;
    const ok = await copyToClipboard(note.content);
    if (ok) {
      toast.success(t("common.copied") || "已复制到剪贴板");
    }
    setOpen(false);
  };

  const handleExportAllZip = () => {
    if (!allNotes.length) return;
    const files = buildVaultNoteMarkdownExportFiles(allNotes, { type: "all" });
    const zipBlob = buildTextFilesZipBlob(files);
    const url = URL.createObjectURL(zipBlob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `netcatty-notes-backup-${new Date().toISOString().slice(0, 10)}.zip`;
    a.click();
    URL.revokeObjectURL(url);
    toast.success(t("notes.export.toast.zipSuccess"));
    setOpen(false);
  };

  return (
    <div className={`relative inline-block ${className}`} ref={menuRef}>
      <button
        type="button"
        className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
        title={t("notes.export.share")}
        onClick={() => setOpen((prev) => !prev)}
      >
        <Share2 size={16} />
      </button>

      {open && (
        <div className="absolute right-0 top-full mt-1.5 w-56 bg-popover border border-border rounded-lg shadow-lg py-1.5 z-50 text-sm animate-in fade-in-50 zoom-in-95">
          {note && (
            <>
              <div className="px-3 py-1 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                {t("notes.export.currentNote")}
              </div>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2.5 hover:bg-muted text-foreground transition-colors text-left"
                onClick={handleExportSingleMarkdown}
              >
                <FileText size={14} className="text-primary" />
                <span>{t("notes.export.exportMarkdown")}</span>
              </button>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2.5 hover:bg-muted text-foreground transition-colors text-left"
                onClick={handleExportSingleHtml}
              >
                <FileCode size={14} className="text-primary" />
                <span>{t("notes.export.exportHtml")}</span>
              </button>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2.5 hover:bg-muted text-foreground transition-colors text-left"
                onClick={handleCopyMarkdown}
              >
                <Copy size={14} className="text-muted-foreground" />
                <span>{t("notes.export.copyMarkdown")}</span>
              </button>
              <div className="my-1 border-t border-border" />
            </>
          )}

          <div className="px-3 py-1 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
            {t("notes.export.allNotes")}
          </div>
          <button
            type="button"
            className="w-full px-3 py-1.5 flex items-center gap-2.5 hover:bg-muted text-foreground transition-colors text-left"
            onClick={handleExportAllZip}
            disabled={!allNotes.length}
          >
            <Download size={14} className="text-emerald-500" />
            <span>{t("notes.export.exportAllZip")}</span>
          </button>
        </div>
      )}
    </div>
  );
};
