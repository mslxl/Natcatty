import {
  Bold,
  Check,
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
  Minus,
  PencilLine,
  Quote,
  Redo2,
  Search,
  Sigma,
  SquareCode,
  Strikethrough,
  Table as TableIcon,
  Type,
  Underline,
  Undo2,
} from "lucide-react";
import React, { useMemo, useState } from "react";
import { type MarkdownActionType } from "../../domain/notes";
import { useAvailableFonts } from "../../application/state/fontStore";
import type { ActiveTextFormats, NoteEditorMode } from "./InlineMarkdownEditor";
import { EMPTY_ACTIVE_FORMATS } from "./InlineMarkdownEditor";
import { Dropdown, DropdownContent, DropdownTrigger } from "../ui/dropdown";
import { cn } from "../../lib/utils";

export interface NoteToolbarProps {
  editorMode: NoteEditorMode;
  onChangeMode: (mode: NoteEditorMode) => void;
  onAction?: (action: MarkdownActionType) => void;
  onOpenHostPicker?: () => void;
  className?: string;
  noteFontFamily?: string;
  onChangeNoteFontFamily?: (font: string) => void;
  noteFontSize?: number;
  onChangeNoteFontSize?: (size: number) => void;
  /** Active text-format toggles at the current selection (button highlight). */
  activeFormats?: ActiveTextFormats;
}

const FONT_SIZES = [12, 13, 14, 15, 16, 18, 20];

export const NoteToolbar: React.FC<NoteToolbarProps> = ({
  editorMode,
  onChangeMode,
  onAction,
  className = "",
  noteFontFamily = "",
  onChangeNoteFontFamily,
  noteFontSize = 14,
  onChangeNoteFontSize,
  activeFormats = EMPTY_ACTIVE_FORMATS,
}) => {
  const [fontSearch, setFontSearch] = useState("");

  // The font tool only controls code block / inline code fonts, so it lists
  // system monospace fonts (fontStore) rather than the UI font set.
  const availableSystemFonts = useAvailableFonts();

  const systemFontList = useMemo(() => {
    const defaultOption = { label: "默认等宽字体 (Default)", value: "" };
    const list = availableSystemFonts.map((f) => ({
      label: f.name,
      value: f.family,
    }));
    return [defaultOption, ...list];
  }, [availableSystemFonts]);

  const filteredFonts = useMemo(() => {
    if (!fontSearch.trim()) return systemFontList;
    const query = fontSearch.trim().toLowerCase();
    return systemFontList.filter(
      (f) => f.label.toLowerCase().includes(query) || f.value.toLowerCase().includes(query),
    );
  }, [fontSearch, systemFontList]);

  const isEditing = editorMode === "edit" || editorMode === "live" || editorMode === "source";

  // Highlight style for toggles that are active at the current selection.
  const formatButtonClass = (active: boolean) =>
    cn(
      "p-1.5 rounded-md transition-colors",
      active
        ? "bg-primary/15 text-primary hover:bg-primary/20 hover:text-primary"
        : "hover:bg-muted text-muted-foreground hover:text-foreground",
    );

  return (
    <div
      className={`flex items-center justify-between gap-1.5 px-3 py-1.5 border-b border-border/70 bg-card/40 text-xs select-none min-w-0 ${className}`}
    >
      {/* Left Area: View Mode Segmented Control */}
      <div className="flex items-center gap-0.5 bg-muted/60 p-0.5 rounded-lg border border-border/50 shrink-0">
        <button
          type="button"
          data-note-mode-switch="live"
          className={`flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "edit" || editorMode === "live"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("edit")}
          title="实时预览模式 (WYSIWYG)"
        >
          <PencilLine size={13} />
          <span className="hidden sm:inline">实时预览</span>
        </button>

        <button
          type="button"
          data-note-mode-switch="source"
          className={`flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "source"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("source")}
          title="源码模式 (Markdown 代码)"
        >
          <SquareCode size={13} />
          <span className="hidden sm:inline">源码模式</span>
        </button>

        <button
          type="button"
          data-note-mode-switch="preview"
          className={`flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium transition-all ${
            editorMode === "preview"
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChangeMode("preview")}
          title="阅读模式 (只读排版)"
        >
          <Eye size={13} />
          <span className="hidden sm:inline">阅读模式</span>
        </button>
      </div>

      {/* Middle Area: Formatting Tools (Available in Live Preview & Source Mode) */}
      {isEditing && (
        <div className="flex flex-1 items-center gap-0.5 min-w-0 overflow-x-auto [scrollbar-width:thin] [&::-webkit-scrollbar]:h-1.5 [&::-webkit-scrollbar-thumb]:rounded-full [&::-webkit-scrollbar-thumb]:bg-border/70 [&::-webkit-scrollbar-track]:bg-transparent">
          <div className="h-4 w-px bg-border mx-1 shrink-0" />

          {/* Undo / Redo */}
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors shrink-0"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("undo")}
            title="撤销 (Ctrl+Z)"
          >
            <Undo2 size={14} />
          </button>

          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors shrink-0"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("redo")}
            title="重做 (Ctrl+Y)"
          >
            <Redo2 size={14} />
          </button>

          <div className="h-4 w-px bg-border mx-1 shrink-0" />

          {/* Code Font & Typography Settings Dropdown */}
          <Dropdown>
            <DropdownTrigger asChild>
              <button
                type="button"
                className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors shrink-0"
                title="代码字体与排版设置"
                onMouseDown={(e) => e.preventDefault()}
              >
                <Type size={14} />
              </button>
            </DropdownTrigger>
            <DropdownContent align="start" className="w-64 p-2.5 space-y-2.5 z-50 text-xs shadow-lg">
              <div className="space-y-1.5">
                <div className="flex items-center justify-between text-[11px] font-medium text-muted-foreground">
                  <span>等宽字体</span>
                  <span className="text-[10px] opacity-70">共 {systemFontList.length} 款可用字体</span>
                </div>

                {/* Search Bar */}
                <div className="relative flex items-center">
                  <Search size={12} className="absolute left-2 text-muted-foreground pointer-events-none" />
                  <input
                    type="text"
                    placeholder="搜索等宽字体..."
                    value={fontSearch}
                    onChange={(e) => setFontSearch(e.target.value)}
                    className="w-full pl-6 pr-2 py-1 rounded border border-border bg-background text-[11px] text-foreground outline-none focus:border-primary placeholder:text-muted-foreground/60"
                  />
                </div>

                {/* Scrollable Font List */}
                <div className="space-y-0.5 max-h-52 overflow-y-auto pr-1">
                  {filteredFonts.length === 0 ? (
                    <div className="py-2 text-center text-muted-foreground text-[11px]">未找到匹配的等宽字体</div>
                  ) : (
                    filteredFonts.map((f) => (
                      <button
                        key={f.value}
                        type="button"
                        style={{ fontFamily: f.value || undefined }}
                        className={cn(
                          "w-full px-2 py-1 rounded text-left text-xs transition-colors flex items-center justify-between gap-1.5",
                          (noteFontFamily || "") === f.value
                            ? "bg-primary text-primary-foreground font-medium"
                            : "hover:bg-secondary text-foreground",
                        )}
                        onClick={() => onChangeNoteFontFamily?.(f.value)}
                      >
                        <span className="truncate">{f.label}</span>
                        {(noteFontFamily || "") === f.value && <Check size={12} className="shrink-0" />}
                      </button>
                    ))
                  )}
                </div>
              </div>

              <div className="border-t border-border/60 pt-2">
                <div className="text-[11px] font-medium text-muted-foreground mb-1">正文字号</div>
                <div className="flex flex-wrap gap-1">
                  {FONT_SIZES.map((sz) => (
                    <button
                      key={sz}
                      type="button"
                      className={cn(
                        "px-2 py-0.5 rounded text-xs border transition-colors",
                        (noteFontSize || 14) === sz
                          ? "bg-primary text-primary-foreground border-primary font-medium"
                          : "border-border hover:bg-secondary text-foreground",
                      )}
                      onClick={() => onChangeNoteFontSize?.(sz)}
                    >
                      {sz}px
                    </button>
                  ))}
                </div>
              </div>
            </DropdownContent>
          </Dropdown>

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
            className={formatButtonClass(activeFormats.bold)}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("bold")}
            title="加粗 (Ctrl+B)"
          >
            <Bold size={14} />
          </button>

          <button
            type="button"
            className={formatButtonClass(activeFormats.italic)}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("italic")}
            title="斜体 (Ctrl+I)"
          >
            <Italic size={14} />
          </button>

          <button
            type="button"
            className={formatButtonClass(activeFormats.strikethrough)}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("strikethrough")}
            title="删除线"
          >
            <Strikethrough size={14} />
          </button>

          <button
            type="button"
            className={formatButtonClass(activeFormats.underline)}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onAction?.("underline")}
            title="下划线"
          >
            <Underline size={14} />
          </button>

          <button
            type="button"
            className={formatButtonClass(activeFormats.code)}
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
            title="数学公式块 (LaTeX / Math)"
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
    </div>
  );
};
