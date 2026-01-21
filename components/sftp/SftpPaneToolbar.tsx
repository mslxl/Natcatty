import React from "react";
import { ChevronLeft, FilePlus, Folder, FolderPlus, Home, RefreshCw, Search, X } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { cn } from "../../lib/utils";
import { SftpBreadcrumb } from "./index";
import type { SftpFilenameEncoding } from "../../types";
import type { SftpPane } from "../../application/state/sftp/types";

interface SftpPaneToolbarProps {
  t: (key: string, params?: Record<string, unknown>) => string;
  pane: SftpPane;
  onNavigateUp: () => void;
  onNavigateTo: (path: string) => void;
  onSetFilter: (value: string) => void;
  onSetFilenameEncoding: (encoding: SftpFilenameEncoding) => void;
  onRefresh: () => void;
  showFilterBar: boolean;
  setShowFilterBar: (open: boolean) => void;
  filterInputRef: React.RefObject<HTMLInputElement>;
  isEditingPath: boolean;
  editingPathValue: string;
  setEditingPathValue: (value: string) => void;
  setShowPathSuggestions: (open: boolean) => void;
  showPathSuggestions: boolean;
  setPathSuggestionIndex: (value: number) => void;
  pathSuggestions: { path: string; type: "folder" | "history" }[];
  pathSuggestionIndex: number;
  pathInputRef: React.RefObject<HTMLInputElement>;
  pathDropdownRef: React.RefObject<HTMLDivElement>;
  handlePathBlur: () => void;
  handlePathKeyDown: (e: React.KeyboardEvent) => void;
  handlePathDoubleClick: () => void;
  handlePathSubmit: (pathOverride?: string) => void;
  startTransition: React.TransitionStartFunction;
  getNextUntitledName: (existingNames: string[]) => string;
  setNewFileName: (value: string) => void;
  setFileNameError: (value: string | null) => void;
  setShowNewFileDialog: (open: boolean) => void;
  setShowNewFolderDialog: (open: boolean) => void;
}

export const SftpPaneToolbar: React.FC<SftpPaneToolbarProps> = ({
  t,
  pane,
  onNavigateUp,
  onNavigateTo,
  onSetFilter,
  onSetFilenameEncoding,
  onRefresh,
  showFilterBar,
  setShowFilterBar,
  filterInputRef,
  isEditingPath,
  editingPathValue,
  setEditingPathValue,
  setShowPathSuggestions,
  setPathSuggestionIndex,
  showPathSuggestions,
  pathSuggestions,
  pathSuggestionIndex,
  pathInputRef,
  pathDropdownRef,
  handlePathBlur,
  handlePathKeyDown,
  handlePathDoubleClick,
  handlePathSubmit,
  startTransition,
  getNextUntitledName,
  setNewFileName,
  setFileNameError,
  setShowNewFileDialog,
  setShowNewFolderDialog,
}) => (
  <>
    {/* Toolbar - always visible when connected */}
    <div className="h-7 px-2 flex items-center gap-1 border-b border-border/40 bg-secondary/20">
      <Button
        variant="ghost"
        size="icon"
        className="h-5 w-5"
        onClick={onNavigateUp}
        title={t("sftp.goUp")}
      >
        <ChevronLeft size={12} />
      </Button>

      {/* Editable Breadcrumb with autocomplete */}
      {isEditingPath ? (
        <div className="relative flex-1">
          <Input
            ref={pathInputRef}
            value={editingPathValue}
            onChange={(e) => {
              setEditingPathValue(e.target.value);
              setShowPathSuggestions(true);
              setPathSuggestionIndex(-1);
            }}
            onBlur={handlePathBlur}
            onKeyDown={handlePathKeyDown}
            onFocus={() => setShowPathSuggestions(true)}
            className="h-5 w-full text-[10px] bg-background"
            autoFocus
          />
          {showPathSuggestions && pathSuggestions.length > 0 && (
            <div
              ref={pathDropdownRef}
              className="absolute top-full left-0 right-0 mt-1 bg-popover border border-border rounded-md shadow-lg z-50 max-h-48 overflow-auto"
            >
              {pathSuggestions.map((suggestion, idx) => (
                <button
                  key={suggestion.path}
                  type="button"
                  className={cn(
                    "w-full px-3 py-2 text-left text-xs flex items-center gap-2 hover:bg-secondary/60 transition-colors",
                    idx === pathSuggestionIndex && "bg-secondary/80",
                  )}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    handlePathSubmit(suggestion.path);
                  }}
                >
                  {suggestion.type === "folder" ? (
                    <Folder size={12} className="text-primary shrink-0" />
                  ) : (
                    <Home
                      size={12}
                      className="text-muted-foreground shrink-0"
                    />
                  )}
                  <span className="truncate font-mono">
                    {suggestion.path}
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>
      ) : (
        <div
          className="flex-1 cursor-text hover:bg-secondary/50 rounded px-1 transition-colors"
          onDoubleClick={handlePathDoubleClick}
          title={t("sftp.path.doubleClickToEdit")}
        >
          <SftpBreadcrumb
            path={pane.connection.currentPath}
            onNavigate={onNavigateTo}
            onHome={() =>
              pane.connection?.homeDir &&
              onNavigateTo(pane.connection.homeDir)
            }
          />
        </div>
      )}

      <div className="ml-auto flex items-center gap-0.5">
        {!pane.connection?.isLocal && (
          <Select
            value={pane.filenameEncoding}
            onValueChange={(value) => onSetFilenameEncoding(value as SftpFilenameEncoding)}
          >
            <SelectTrigger className="h-6 w-[120px] text-[10px]" title={t("sftp.encoding.label")}>
              <SelectValue placeholder={t("sftp.encoding.label")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">{t("sftp.encoding.auto")}</SelectItem>
              <SelectItem value="utf-8">{t("sftp.encoding.utf8")}</SelectItem>
              <SelectItem value="gb18030">{t("sftp.encoding.gb18030")}</SelectItem>
            </SelectContent>
          </Select>
        )}
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          onClick={() => setShowNewFolderDialog(true)}
          title={t("sftp.newFolder")}
        >
          <FolderPlus size={14} />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          onClick={() => {
            const defaultName = getNextUntitledName(pane.files.map(f => f.name));
            setNewFileName(defaultName);
            setFileNameError(null);
            setShowNewFileDialog(true);
          }}
          title={t("sftp.newFile")}
        >
          <FilePlus size={14} />
        </Button>
        <Button
          variant={showFilterBar || pane.filter ? "secondary" : "ghost"}
          size="icon"
          className={cn("h-6 w-6", pane.filter && "text-primary")}
          onClick={() => {
            setShowFilterBar(!showFilterBar);
            if (!showFilterBar) {
              setTimeout(() => filterInputRef.current?.focus(), 0);
            }
          }}
          title={t("sftp.filter")}
        >
          <Search size={14} />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          onClick={onRefresh}
          title={t("common.refresh")}
        >
          <RefreshCw
            size={14}
            className={
              pane.loading || pane.reconnecting ? "animate-spin" : ""
            }
          />
        </Button>
      </div>
    </div>

    {/* Inline filter bar - appears below toolbar when search is active */}
    {showFilterBar && (
      <div className="h-8 px-3 flex items-center gap-2 border-b border-border/40 bg-secondary/10">
        <div className="relative flex-1">
          <Search
            size={12}
            className="absolute left-2 top-1/2 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            ref={filterInputRef}
            value={pane.filter}
            onChange={(e) =>
              startTransition(() => onSetFilter(e.target.value))
            }
            placeholder={t("sftp.filter.placeholder")}
            className="h-6 w-full pl-7 pr-7 text-xs bg-background"
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                if (pane.filter) {
                  startTransition(() => onSetFilter(""));
                } else {
                  setShowFilterBar(false);
                }
              }
            }}
          />
          {pane.filter && (
            <button
              onClick={() => startTransition(() => onSetFilter(""))}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <X size={12} />
            </button>
          )}
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6 shrink-0"
          onClick={() => {
            startTransition(() => onSetFilter(""));
            setShowFilterBar(false);
          }}
          title={t("common.close")}
        >
          <X size={14} />
        </Button>
      </div>
    )}
  </>
);
