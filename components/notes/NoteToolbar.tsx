import {
  Bold,
  CheckSquare,
  Code,
  Eye,
  FileCode,
  Heading1,
  Heading2,
  Heading3,
  Heading4,
  Heading,
  Image as ImageIcon,
  Italic,
  Link as LinkIcon,
  List,
  ListOrdered,
  ListTree,
  Minus,
  PencilLine,
  Quote,
  Sigma,
  SquareCode,
  Strikethrough,
  Table as TableIcon,
  Underline,
} from "lucide-react";
import React, { useMemo } from "react";
import { calculateNoteStats, type MarkdownActionType } from "../../domain/notes";
import type { NoteEditorMode } from "./InlineMarkdownEditor";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { Dropdown, DropdownContent, DropdownTrigger } from "../ui/dropdown";

export interface NoteToolbarProps {
  content: string;
  editorMode: NoteEditorMode;
  onChangeMode: (mode: NoteEditorMode) => void;
  onAction?: (action: MarkdownActionType) => void;
  onOpenHostPicker?: () => void;
  showOutline?: boolean;
  onToggleOutline?: () => void;
  className?: string;
}

export const NoteToolbar: React.FC<NoteToolbarProps> = ({
  content,
  editorMode,
  onChangeMode,
  onAction,
  showOutline,
  onToggleOutline,
  className = "",
}) => {
  const stats = useMemo(() => calculateNoteStats(content), [content]);

  const isEditing = editorMode === "edit" || editorMode === "live" || editorMode === "source";

  return (
    <div
      className={`flex items-center justify-between gap-1 px-3 py-1.5 border-b border-border/70 bg-card/40 text-xs select-none overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden ${className}`}
    >
      {/* Left Area: View Mode Segmented Control */}
      <div className="flex items-center gap-1 bg-muted/60 p-0.5 rounded-lg border border-border/50 shrink-0">
        <button
          type="button"
          data-note-mode-switch="live"
          className={`flex items-center gap-1.5 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "edit" || editorMode === "live"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("edit")}
          title="实时预览模式 (WYSIWYG)"
        >
          <PencilLine size={13} />
          <span>实时预览</span>
        </button>

        <button
          type="button"
          data-note-mode-switch="source"
          className={`flex items-center gap-1.5 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "source"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("source")}
          title="源码模式 (Markdown 代码)"
        >
          <SquareCode size={13} />
          <span>源码模式</span>
        </button>

        <button
          type="button"
          data-note-mode-switch="preview"
          className={`flex items-center gap-1.5 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "preview"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("preview")}
          title="阅读模式 (只读排版)"
        >
          <Eye size={13} />
          <span>阅读模式</span>
        </button>
      </div>

      {/* Middle Area: Formatting Tools (Available in Live Preview & Source Mode) */}
      {isEditing && (
        <div className="flex items-center gap-0.5 min-w-0">
          <div className="h-4 w-px bg-border mx-1 shrink-0" />

          {/* Heading Dropdown (Using Portal Dropdown to avoid clipping) */}
          <Dropdown>
            <DropdownTrigger asChild>
              <button
                type="button"
                className="flex items-center gap-1 p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
                onMouseDown={(e) => e.preventDefault()}
                title="标题层级"
              >
                <Heading size={14} />
              </button>
            </DropdownTrigger>
            <DropdownContent align="start" className="w-32 py-1 z-50 text-xs">
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2 hover:bg-secondary text-foreground transition-colors text-left"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onAction?.("h1")}
              >
                <Heading1 size={14} className="text-primary" />
                <span>一级标题</span>
              </button>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2 hover:bg-secondary text-foreground transition-colors text-left"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onAction?.("h2")}
              >
                <Heading2 size={14} className="text-primary" />
                <span>二级标题</span>
              </button>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2 hover:bg-secondary text-foreground transition-colors text-left"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onAction?.("h3")}
              >
                <Heading3 size={14} className="text-primary" />
                <span>三级标题</span>
              </button>
              <button
                type="button"
                className="w-full px-3 py-1.5 flex items-center gap-2 hover:bg-secondary text-foreground transition-colors text-left"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onAction?.("h4")}
              >
                <Heading4 size={14} className="text-primary" />
                <span>四级标题</span>
              </button>
            </DropdownContent>
          </Dropdown>

          {/* Inline Styles */}
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("bold")}
            title="加粗 (Ctrl+B)"
          >
            <Bold size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("italic")}
            title="斜体 (Ctrl+I)"
          >
            <Italic size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("strikethrough")}
            title="删除线"
          >
            <Strikethrough size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("underline")}
            title="下划线"
          >
            <Underline size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("code")}
            title="行内代码"
          >
            <Code size={14} />
          </button>

          <div className="h-4 w-px bg-border mx-1 shrink-0" />

          {/* Lists */}
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("bullet")}
            title="无序列表"
          >
            <List size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("number")}
            title="有序列表"
          >
            <ListOrdered size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("task")}
            title="待办任务清单"
          >
            <CheckSquare size={14} />
          </button>

          <div className="h-4 w-px bg-border mx-1 shrink-0" />

          {/* Blocks */}
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("quote")}
            title="引用块"
          >
            <Quote size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("codeblock")}
            title="代码块"
          >
            <FileCode size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("math")}
            title="数学公式块 (LaTeX / $$)"
          >
            <Sigma size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("table")}
            title="插入表格"
          >
            <TableIcon size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("divider")}
            title="分割线"
          >
            <Minus size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("link")}
            title="插入超链接"
          >
            <LinkIcon size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("image")}
            title="插入图片"
          >
            <ImageIcon size={14} />
          </button>
        </div>
      )}

      {/* Right Area: Document Stats & Outline */}
      <div className="flex items-center gap-2 shrink-0 text-muted-foreground text-[11px] ml-auto">
        <span className="opacity-70 whitespace-nowrap">
          {stats.words} 词 · {stats.chars} 字
        </span>

        {onToggleOutline && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                className={`p-1.5 rounded-md transition-colors ${
                  showOutline
                    ? "bg-secondary text-primary font-medium"
                    : "hover:bg-muted text-muted-foreground hover:text-foreground"
                }`}
                onClick={onToggleOutline}
              >
                <ListTree size={14} />
              </button>
            </TooltipTrigger>
            <TooltipContent side="bottom">大纲目录</TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  );
};
