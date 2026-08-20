import { Hash, ListTree, X } from "lucide-react";
import React, { useMemo } from "react";
import { extractNoteHeadings, type NoteHeadingItem } from "../../domain/notes";
import { useI18n } from "../../application/i18n/I18nProvider";

export interface NoteOutlineProps {
  content: string;
  onSelectHeading?: (heading: NoteHeadingItem) => void;
  onClose?: () => void;
  className?: string;
}

export const NoteOutline: React.FC<NoteOutlineProps> = ({
  content,
  onSelectHeading,
  onClose,
  className = "",
}) => {
  const { t } = useI18n();
  const headings = useMemo(() => extractNoteHeadings(content), [content]);

  const getIndentClass = (level: number) => {
    switch (level) {
      case 1:
        return "pl-2 font-semibold text-foreground";
      case 2:
        return "pl-5 text-foreground/90";
      case 3:
        return "pl-8 text-muted-foreground";
      case 4:
      default:
        return "pl-11 text-muted-foreground/80 text-xs";
    }
  };

  return (
    <div
      className={`flex flex-col h-full bg-background/95 border-l border-border select-none ${className}`}
    >
      <div className="flex items-center justify-between px-3.5 py-2.5 border-b border-border/80 shrink-0">
        <div className="flex items-center gap-2 text-xs font-semibold text-foreground uppercase tracking-wider">
          <ListTree size={14} className="text-primary" />
          <span>大纲目录 ({headings.length})</span>
        </div>
        {onClose && (
          <button
            type="button"
            className="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onClick={onClose}
            title={t("common.close") || "关闭"}
          >
            <X size={14} />
          </button>
        )}
      </div>

      <div className="flex-1 overflow-y-auto p-2 space-y-0.5">
        {headings.length === 0 ? (
          <div className="py-8 text-center text-xs text-muted-foreground px-4">
            <Hash size={24} className="mx-auto mb-2 opacity-30" />
            <p>暂无标题大纲</p>
            <p className="mt-1 text-[11px] opacity-70">
              在 Markdown 中使用 #、## 编写各级标题
            </p>
          </div>
        ) : (
          headings.map((item) => (
            <button
              key={item.id}
              type="button"
              className={`w-full text-left py-1.5 pr-2 rounded-md hover:bg-muted/70 text-xs truncate transition-colors flex items-center gap-1.5 ${getIndentClass(
                item.level,
              )}`}
              onClick={() => onSelectHeading?.(item)}
              title={item.text}
            >
              <span className="opacity-40 text-[10px] shrink-0">H{item.level}</span>
              <span className="truncate">{item.text}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
};
