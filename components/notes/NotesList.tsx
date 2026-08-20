import {
  Copy,
  Folder,
  Hash,
  MoreVertical,
  Pin,
  PinOff,
  Plus,
  Search,
  Trash2,
  X,
} from "lucide-react";
import React, { useMemo, useState } from "react";
import { useI18n } from "../../application/i18n/I18nProvider";
import {
  extractNoteSnippet,
  filterAndSortVaultNotes,
  getNoteGroupLeafName,
  type NoteFilterMode,
  type NoteSortOption,
  type VaultNote,
} from "../../domain/notes";
import type { Host } from "../../types";

export interface NotesListProps {
  notes: VaultNote[];
  selectedNoteId: string | null;
  selectedGroup: string | null;
  selectedTag: string | null;
  filterMode: NoteFilterMode;
  hosts?: Host[];
  onSelectNote: (noteId: string) => void;
  onCreateNote: () => void;
  onTogglePinNote: (noteId: string) => void;
  onDuplicateNote: (note: VaultNote) => void;
  onDeleteNote: (noteId: string) => void;
  className?: string;
}

function formatNoteRelativeTime(timestamp: number): string {
  if (!timestamp || !Number.isFinite(timestamp)) return "";
  const diff = Date.now() - timestamp;
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;

  if (diff < minute) return "刚刚";
  if (diff < hour) return `${Math.floor(diff / minute)} 分钟前`;
  if (diff < day) {
    const d = new Date(timestamp);
    return `${d.getHours().toString().padStart(2, "0")}:${d
      .getMinutes()
      .toString()
      .padStart(2, "0")}`;
  }
  if (diff < 2 * day) return "昨天";
  if (diff < 365 * day) {
    const d = new Date(timestamp);
    return `${d.getMonth() + 1}月${d.getDate()}日`;
  }
  const d = new Date(timestamp);
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

export const NotesList: React.FC<NotesListProps> = ({
  notes,
  selectedNoteId,
  selectedGroup,
  selectedTag,
  filterMode,
  hosts = [],
  onSelectNote,
  onCreateNote,
  onTogglePinNote,
  onDuplicateNote,
  onDeleteNote,
  className = "",
}) => {
  const { t } = useI18n();
  const [searchQuery, setSearchQuery] = useState("");
  const [sortOption, setSortOption] = useState<NoteSortOption>("updatedDesc");
  const [activeMenuNoteId, setActiveMenuNoteId] = useState<string | null>(null);

  // Compute title for the list header
  const headerTitle = useMemo(() => {
    if (selectedTag) return `#${selectedTag}`;
    if (selectedGroup) return getNoteGroupLeafName(selectedGroup);
    if (filterMode === "pinned") return t("notes.pinned") || "已置顶笔记";
    if (filterMode === "recent") return t("notes.recent") || "最近修改";
    if (filterMode === "uncategorized") return t("notes.uncategorized") || "未分类笔记";
    return t("notes.allNotes") || "全部笔记";
  }, [selectedTag, selectedGroup, filterMode, t]);

  // Filter and sort notes
  const displayedNotes = useMemo(() => {
    return filterAndSortVaultNotes(notes, {
      search: searchQuery,
      group: selectedGroup,
      tag: selectedTag,
      filterMode,
      sort: sortOption,
      hosts,
    });
  }, [notes, searchQuery, selectedGroup, selectedTag, filterMode, sortOption, hosts]);

  return (
    <div
      className={`flex flex-col h-full bg-background border-r border-border select-none ${className}`}
    >
      {/* List Header */}
      <div className="p-3 border-b border-border/80 space-y-2 shrink-0">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 min-w-0">
            <h2 className="font-semibold text-sm text-foreground truncate">
              {headerTitle}
            </h2>
            <span className="text-xs text-muted-foreground bg-muted px-1.5 py-0.2 rounded-full shrink-0">
              {displayedNotes.length}
            </span>
          </div>

          <div className="flex items-center gap-1">
            <select
              value={sortOption}
              onChange={(e) => setSortOption(e.target.value as NoteSortOption)}
              className="text-xs bg-muted/70 hover:bg-muted border border-border/60 rounded px-1.5 py-1 text-foreground cursor-pointer outline-none transition-colors"
              title="排序方式"
            >
              <option value="updatedDesc">最近修改</option>
              <option value="createdDesc">最新创建</option>
              <option value="titleAsc">标题 A-Z</option>
              <option value="titleDesc">标题 Z-A</option>
              <option value="custom">自定义顺序</option>
            </select>

            <button
              type="button"
              className="px-2 py-1 bg-primary text-primary-foreground hover:bg-primary/90 text-xs font-medium rounded-md flex items-center gap-1 transition-colors shadow-sm shrink-0"
              onClick={onCreateNote}
              title={t("notes.newNote") || "新建笔记"}
            >
              <Plus size={14} />
              <span>新建</span>
            </button>
          </div>
        </div>

        {/* Search input */}
        <div className="relative">
          <Search
            size={13}
            className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
          />
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder={t("notes.searchPlaceholder") || "搜索笔记标题、内容、标签..."}
            className="w-full bg-muted/50 focus:bg-background border border-border focus:border-primary/60 rounded-md pl-7 pr-7 py-1.5 text-xs text-foreground placeholder:text-muted-foreground/70 outline-none transition-all"
          />
          {searchQuery && (
            <button
              type="button"
              onClick={() => setSearchQuery("")}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <X size={13} />
            </button>
          )}
        </div>
      </div>

      {/* Cards List */}
      <div className="flex-1 overflow-y-auto p-2 space-y-1.5">
        {displayedNotes.length === 0 ? (
          <div className="py-12 text-center text-xs text-muted-foreground">
            <p>暂无匹配笔记</p>
            {searchQuery ? (
              <p className="mt-1 text-[11px] opacity-70">
                可尝试更改搜索关键词或清除搜索条件
              </p>
            ) : (
              <button
                type="button"
                className="mt-3 px-3 py-1.5 bg-primary/10 hover:bg-primary/20 text-primary rounded text-xs transition-colors font-medium"
                onClick={onCreateNote}
              >
                创建第一篇笔记
              </button>
            )}
          </div>
        ) : (
          displayedNotes.map((note) => {
            const isSelected = selectedNoteId === note.id;
            const snippet = extractNoteSnippet(note.content, 90);
            const title = note.title.trim() || t("notes.untitled") || "无标题笔记";
            const isMenuOpen = activeMenuNoteId === note.id;

            return (
              <div
                key={note.id}
                className={`group relative p-2.5 rounded-lg border transition-all cursor-pointer ${
                  isSelected
                    ? "bg-primary/10 border-primary/50 shadow-sm"
                    : "bg-card hover:bg-muted/50 border-border/70 hover:border-border"
                }`}
                onClick={() => onSelectNote(note.id)}
              >
                {/* Title & Pin Indicator */}
                <div className="flex items-start justify-between gap-1 mb-1">
                  <div className="flex items-center gap-1.5 min-w-0">
                    {note.isPinned && (
                      <Pin
                        size={12}
                        className="text-amber-500 fill-amber-500 shrink-0"
                      />
                    )}
                    <span
                      className={`text-xs font-semibold truncate ${
                        isSelected ? "text-primary" : "text-foreground"
                      }`}
                    >
                      {title}
                    </span>
                  </div>

                  {/* Actions Dropdown Trigger on hover */}
                  <div className="relative shrink-0">
                    <button
                      type="button"
                      className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-opacity"
                      onClick={(e) => {
                        e.stopPropagation();
                        setActiveMenuNoteId(isMenuOpen ? null : note.id);
                      }}
                    >
                      <MoreVertical size={13} />
                    </button>

                    {isMenuOpen && (
                      <div
                        className="absolute right-0 top-full mt-1 w-32 bg-popover border border-border rounded-md shadow-lg py-1 z-50 text-xs text-foreground animate-in fade-in-50 zoom-in-95"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <button
                          type="button"
                          className="w-full px-2.5 py-1 flex items-center gap-2 hover:bg-muted transition-colors text-left"
                          onClick={() => {
                            onTogglePinNote(note.id);
                            setActiveMenuNoteId(null);
                          }}
                        >
                          {note.isPinned ? (
                            <>
                              <PinOff size={13} className="text-muted-foreground" />
                              <span>取消置顶</span>
                            </>
                          ) : (
                            <>
                              <Pin size={13} className="text-amber-500" />
                              <span>置顶笔记</span>
                            </>
                          )}
                        </button>
                        <button
                          type="button"
                          className="w-full px-2.5 py-1 flex items-center gap-2 hover:bg-muted transition-colors text-left"
                          onClick={() => {
                            onDuplicateNote(note);
                            setActiveMenuNoteId(null);
                          }}
                        >
                          <Copy size={13} className="text-muted-foreground" />
                          <span>创建副本</span>
                        </button>
                        <div className="my-1 border-t border-border" />
                        <button
                          type="button"
                          className="w-full px-2.5 py-1 flex items-center gap-2 hover:bg-destructive/10 text-destructive transition-colors text-left"
                          onClick={() => {
                            onDeleteNote(note.id);
                            setActiveMenuNoteId(null);
                          }}
                        >
                          <Trash2 size={13} />
                          <span>删除</span>
                        </button>
                      </div>
                    )}
                  </div>
                </div>

                {/* Snippet Preview */}
                {snippet && (
                  <p className="text-[11px] text-muted-foreground line-clamp-2 leading-relaxed mb-2 font-normal">
                    {snippet}
                  </p>
                )}

                {/* Footer Metadata: Date, Folder, Tags */}
                <div className="flex items-center justify-between text-[10px] text-muted-foreground/80 pt-0.5">
                  <span className="shrink-0">
                    {formatNoteRelativeTime(note.updatedAt)}
                  </span>

                  <div className="flex items-center gap-1 overflow-hidden ml-2">
                    {note.group && (
                      <span className="inline-flex items-center gap-0.5 px-1 py-0.2 bg-muted rounded text-[10px] text-muted-foreground truncate max-w-[80px]">
                        <Folder size={9} className="opacity-70 shrink-0" />
                        <span className="truncate">{getNoteGroupLeafName(note.group)}</span>
                      </span>
                    )}
                    {note.tags && note.tags.length > 0 && (
                      <span className="inline-flex items-center gap-0.5 px-1 py-0.2 bg-muted rounded text-[10px] text-muted-foreground truncate max-w-[70px]">
                        <Hash size={9} className="opacity-70 shrink-0" />
                        <span className="truncate">{note.tags[0]}</span>
                        {note.tags.length > 1 && (
                          <span className="opacity-60">+{note.tags.length - 1}</span>
                        )}
                      </span>
                    )}
                  </div>
                </div>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
};
