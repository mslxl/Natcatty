import {
  ChevronDown,
  ChevronRight,
  Clock,
  Edit2,
  FileQuestion,
  FileText,
  Folder,
  FolderOpen,
  FolderPlus,
  Hash,
  Pin,
  Plus,
  Tag,
  Trash2,
  Upload,
} from "lucide-react";
import React, { useMemo, useState } from "react";
import { useI18n } from "../../application/i18n/I18nProvider";
import {
  ancestorNoteGroupPaths,
  cleanNoteGroupPath,
  extractAllNoteTags,
  getNoteGroupLeafName,
  getNoteGroupParentPath,
  isNoteGroupInside,
  joinNoteGroupPath,
  type NoteFilterMode,
  type VaultNote,
} from "../../domain/notes";

export interface NotesSidebarProps {
  notes: VaultNote[];
  noteGroups: string[];
  selectedGroup: string | null;
  selectedTag: string | null;
  filterMode: NoteFilterMode;
  onSelectFilterMode: (mode: NoteFilterMode) => void;
  onSelectGroup: (group: string | null) => void;
  onSelectTag: (tag: string | null) => void;
  onCreateGroup: (parentGroup?: string | null) => void;
  onRenameGroup: (group: string) => void;
  onDeleteGroup: (group: string) => void;
  onImportMarkdown?: () => void;
  className?: string;
}

interface GroupTreeItem {
  path: string;
  name: string;
  depth: number;
  hasChildren: boolean;
  directCount: number;
  totalCount: number;
}

export const NotesSidebar: React.FC<NotesSidebarProps> = ({
  notes,
  noteGroups,
  selectedGroup,
  selectedTag,
  filterMode,
  onSelectFilterMode,
  onSelectGroup,
  onSelectTag,
  onCreateGroup,
  onRenameGroup,
  onDeleteGroup,
  onImportMarkdown,
  className = "",
}) => {
  const { t } = useI18n();
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set());

  const toggleFolder = (path: string, e: React.MouseEvent) => {
    e.stopPropagation();
    setExpandedFolders((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  // Compute counts for quick filter views
  const counts = useMemo(() => {
    const pinned = notes.filter((n) => n.isPinned).length;
    const sevenDaysAgo = Date.now() - 7 * 24 * 60 * 60 * 1000;
    const recent = notes.filter((n) => n.updatedAt >= sevenDaysAgo).length;
    const uncategorized = notes.filter((n) => !n.group).length;
    return {
      all: notes.length,
      pinned,
      recent,
      uncategorized,
    };
  }, [notes]);

  // Compute tags
  const allTags = useMemo(() => extractAllNoteTags(notes), [notes]);

  // Build sorted folder hierarchy
  const folderTree = useMemo(() => {
    const allGroupPaths = new Set<string>();
    for (const g of noteGroups) {
      if (g.trim()) {
        const clean = cleanNoteGroupPath(g);
        ancestorNoteGroupPaths(clean).forEach((p) => allGroupPaths.add(p));
      }
    }
    for (const note of notes) {
      if (note.group) {
        ancestorNoteGroupPaths(cleanNoteGroupPath(note.group)).forEach((p) =>
          allGroupPaths.add(p),
        );
      }
    }

    const sortedPaths = Array.from(allGroupPaths).sort((a, b) =>
      a.localeCompare(b, undefined, { sensitivity: "base" }),
    );

    const items: GroupTreeItem[] = [];
    for (const path of sortedPaths) {
      const parts = path.split("/");
      const depth = parts.length - 1;
      const name = getNoteGroupLeafName(path);
      const hasChildren = sortedPaths.some(
        (other) => other !== path && other.startsWith(`${path}/`),
      );
      const directCount = notes.filter((n) => n.group === path).length;
      const totalCount = notes.filter((n) => isNoteGroupInside(n.group, path)).length;

      // Check if all parent folders are expanded
      let visible = true;
      let cur = getNoteGroupParentPath(path);
      while (cur) {
        if (!expandedFolders.has(cur)) {
          visible = false;
          break;
        }
        cur = getNoteGroupParentPath(cur);
      }

      if (visible || depth === 0) {
        items.push({
          path,
          name,
          depth,
          hasChildren,
          directCount,
          totalCount,
        });
      }
    }
    return items;
  }, [noteGroups, notes, expandedFolders]);

  return (
    <div
      className={`flex flex-col h-full bg-card/60 border-r border-border select-none text-xs ${className}`}
    >
      {/* Sidebar Header */}
      <div className="flex items-center justify-between px-3 py-3 border-b border-border/80 shrink-0">
        <span className="font-semibold text-foreground tracking-wide text-[13px] flex items-center gap-2">
          <FileText size={16} className="text-primary" />
          <span>{t("notes.title") || "笔记管理"}</span>
        </span>
        <div className="flex items-center gap-1">
          {onImportMarkdown && (
            <button
              type="button"
              className="p-1 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
              onClick={onImportMarkdown}
              title={t("notes.importMarkdown") || "导入 Markdown"}
            >
              <Upload size={14} />
            </button>
          )}
          <button
            type="button"
            className="p-1 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            onClick={() => onCreateGroup(null)}
            title={t("notes.newGroup") || "新建文件夹"}
          >
            <FolderPlus size={14} />
          </button>
        </div>
      </div>

      {/* Main Navigation Scroll Area */}
      <div className="flex-1 overflow-y-auto p-2 space-y-4">
        {/* Quick Filter Section */}
        <div className="space-y-0.5">
          <button
            type="button"
            className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-md transition-colors ${
              !selectedGroup && !selectedTag && filterMode === "all"
                ? "bg-primary/15 text-primary font-medium"
                : "text-foreground hover:bg-muted/70"
            }`}
            onClick={() => {
              onSelectGroup(null);
              onSelectTag(null);
              onSelectFilterMode("all");
            }}
          >
            <span className="flex items-center gap-2">
              <FileText size={14} className="opacity-70" />
              <span>{t("notes.allNotes") || "全部笔记"}</span>
            </span>
            <span className="text-[11px] opacity-60 bg-muted px-1.5 py-0.2 rounded-full">
              {counts.all}
            </span>
          </button>

          <button
            type="button"
            className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-md transition-colors ${
              !selectedGroup && !selectedTag && filterMode === "pinned"
                ? "bg-primary/15 text-primary font-medium"
                : "text-foreground hover:bg-muted/70"
            }`}
            onClick={() => {
              onSelectGroup(null);
              onSelectTag(null);
              onSelectFilterMode("pinned");
            }}
          >
            <span className="flex items-center gap-2">
              <Pin size={14} className="text-amber-500 opacity-90" />
              <span>{t("notes.pinned") || "已置顶"}</span>
            </span>
            <span className="text-[11px] opacity-60 bg-muted px-1.5 py-0.2 rounded-full">
              {counts.pinned}
            </span>
          </button>

          <button
            type="button"
            className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-md transition-colors ${
              !selectedGroup && !selectedTag && filterMode === "recent"
                ? "bg-primary/15 text-primary font-medium"
                : "text-foreground hover:bg-muted/70"
            }`}
            onClick={() => {
              onSelectGroup(null);
              onSelectTag(null);
              onSelectFilterMode("recent");
            }}
          >
            <span className="flex items-center gap-2">
              <Clock size={14} className="text-blue-500 opacity-80" />
              <span>{t("notes.recent") || "最近修改"}</span>
            </span>
            <span className="text-[11px] opacity-60 bg-muted px-1.5 py-0.2 rounded-full">
              {counts.recent}
            </span>
          </button>

          <button
            type="button"
            className={`w-full flex items-center justify-between px-2.5 py-1.5 rounded-md transition-colors ${
              !selectedGroup && !selectedTag && filterMode === "uncategorized"
                ? "bg-primary/15 text-primary font-medium"
                : "text-foreground hover:bg-muted/70"
            }`}
            onClick={() => {
              onSelectGroup(null);
              onSelectTag(null);
              onSelectFilterMode("uncategorized");
            }}
          >
            <span className="flex items-center gap-2">
              <FileQuestion size={14} className="text-purple-500 opacity-80" />
              <span>{t("notes.uncategorized") || "未分类"}</span>
            </span>
            <span className="text-[11px] opacity-60 bg-muted px-1.5 py-0.2 rounded-full">
              {counts.uncategorized}
            </span>
          </button>
        </div>

        {/* Folders / Notebooks Section */}
        <div className="space-y-1">
          <div className="flex items-center justify-between px-2 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <span>{t("notes.folders") || "笔记本 / 文件夹"}</span>
            <button
              type="button"
              className="p-0.5 rounded hover:bg-muted hover:text-foreground transition-colors"
              onClick={() => onCreateGroup(null)}
              title="新建文件夹"
            >
              <Plus size={13} />
            </button>
          </div>

          <div className="space-y-0.5">
            {folderTree.length === 0 ? (
              <div className="px-2.5 py-2 text-[11px] text-muted-foreground/60 italic">
                暂无文件夹，点击 + 创建
              </div>
            ) : (
              folderTree.map((item) => {
                const isSelected = selectedGroup === item.path;
                const isExpanded = expandedFolders.has(item.path);

                return (
                  <div
                    key={item.path}
                    className={`group/folder flex items-center justify-between px-2 py-1 rounded-md transition-colors cursor-pointer ${
                      isSelected
                        ? "bg-primary/15 text-primary font-medium"
                        : "text-foreground hover:bg-muted/60"
                    }`}
                    style={{ paddingLeft: `${Math.max(8, item.depth * 14 + 8)}px` }}
                    onClick={() => {
                      onSelectGroup(item.path);
                      onSelectTag(null);
                      onSelectFilterMode("all");
                    }}
                  >
                    <div className="flex items-center gap-1.5 min-w-0 flex-1">
                      {item.hasChildren ? (
                        <button
                          type="button"
                          className="p-0.5 rounded hover:bg-muted text-muted-foreground shrink-0"
                          onClick={(e) => toggleFolder(item.path, e)}
                        >
                          {isExpanded ? (
                            <ChevronDown size={12} />
                          ) : (
                            <ChevronRight size={12} />
                          )}
                        </button>
                      ) : (
                        <span className="w-3.5 shrink-0" />
                      )}

                      {isExpanded ? (
                        <FolderOpen size={14} className="text-amber-500 shrink-0" />
                      ) : (
                        <Folder size={14} className="text-amber-500 shrink-0" />
                      )}
                      <span className="truncate text-xs">{item.name}</span>
                    </div>

                    <div className="flex items-center gap-1">
                      <span className="text-[11px] opacity-60 group-hover/folder:hidden">
                        {item.totalCount}
                      </span>
                      <div className="hidden group-hover/folder:flex items-center gap-0.5">
                        <button
                          type="button"
                          className="p-1 rounded hover:bg-background text-muted-foreground hover:text-foreground"
                          onClick={(e) => {
                            e.stopPropagation();
                            onCreateGroup(item.path);
                          }}
                          title="新建子文件夹"
                        >
                          <FolderPlus size={11} />
                        </button>
                        <button
                          type="button"
                          className="p-1 rounded hover:bg-background text-muted-foreground hover:text-foreground"
                          onClick={(e) => {
                            e.stopPropagation();
                            onRenameGroup(item.path);
                          }}
                          title="重命名"
                        >
                          <Edit2 size={11} />
                        </button>
                        <button
                          type="button"
                          className="p-1 rounded hover:bg-background text-destructive hover:text-destructive/80"
                          onClick={(e) => {
                            e.stopPropagation();
                            onDeleteGroup(item.path);
                          }}
                          title="删除"
                        >
                          <Trash2 size={11} />
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>

        {/* Tags Section */}
        {allTags.length > 0 && (
          <div className="space-y-1">
            <div className="flex items-center justify-between px-2 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
              <span className="flex items-center gap-1.5">
                <Tag size={12} />
                <span>{t("notes.tags") || "标签"}</span>
              </span>
            </div>

            <div className="space-y-0.5">
              {allTags.map(({ tag, count }) => {
                const isSelected = selectedTag === tag;
                return (
                  <button
                    key={tag}
                    type="button"
                    className={`w-full flex items-center justify-between px-2.5 py-1 rounded-md transition-colors text-left ${
                      isSelected
                        ? "bg-primary/15 text-primary font-medium"
                        : "text-foreground hover:bg-muted/60"
                    }`}
                    onClick={() => {
                      onSelectTag(isSelected ? null : tag);
                      onSelectGroup(null);
                      onSelectFilterMode("all");
                    }}
                  >
                    <span className="flex items-center gap-1.5 truncate">
                      <Hash size={12} className="opacity-50 shrink-0" />
                      <span className="truncate">{tag}</span>
                    </span>
                    <span className="text-[11px] opacity-60 bg-muted px-1.5 py-0.2 rounded-full">
                      {count}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
        )}
      </div>

      {/* Sidebar Footer */}
      <div className="px-3 py-2 border-t border-border/80 text-[11px] text-muted-foreground flex items-center justify-between shrink-0">
        <span>共 {notes.length} 篇笔记</span>
      </div>
    </div>
  );
};
