/**
 * ScriptsSidePanel - Lightweight scripts browser for the terminal side panel
 *
 * Shows snippets organized by package hierarchy as a single tree view.
 * Packages expand / collapse via a chevron; clicking a snippet executes it
 * in the focused terminal session. Typing in the search box flattens to a
 * list of matching snippets regardless of package nesting.
 */

import { ChevronRight, Edit2, FileCode, Package, Plus, Search, Trash2, Zap } from 'lucide-react';
import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from '../application/i18n/I18nProvider';
import { cn } from '../lib/utils';
import { Snippet } from '../types';
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from './ui/context-menu';
import { FixedSizeVirtualList } from './ui/FixedSizeVirtualList';
import { Input } from './ui/input';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from './ui/tooltip';

const SCRIPT_ROW_HEIGHT = 34;

const isRootPackagePath = (path: string): boolean => {
  const body = path.startsWith('/') ? path.slice(1) : path;
  return body.length > 0 && !body.includes('/');
};

interface ScriptsSidePanelProps {
  snippets: Snippet[];
  packages: string[];
  onSnippetClick: (snippet: Snippet) => void;
  isVisible?: boolean;
}

type TreeRow =
  | {
      type: 'package';
      id: string;
      path: string;
      name: string;
      depth: number;
      count: number;
      hasChildren: boolean;
      isExpanded: boolean;
    }
  | {
      type: 'snippet';
      id: string;
      depth: number;
      snippet: Snippet;
      packagePath: string;
    };

const pkgDisplayName = (path: string) => {
  const clean = path.startsWith('/') ? path.slice(1) : path;
  const last = clean.split('/').filter(Boolean).pop() ?? clean;
  // Preserve the leading slash on absolute root packages so they stay
  // distinguishable from relative ones (matches the previous breadcrumb UI).
  return path.startsWith('/') && !clean.includes('/') ? `/${last}` : last;
};

const ScriptsSidePanelInner: React.FC<ScriptsSidePanelProps> = ({
  snippets,
  packages,
  onSnippetClick,
  isVisible = true,
}) => {
  const { t } = useI18n();
  const [search, setSearch] = useState('');
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set());

  // Normalize the package list + derive ancestor packages implied by each path
  // (e.g. package "a/b/c" implies roots "a" and "a/b" even when not listed).
  const normalizedPackages = useMemo(() => {
    if (!isVisible) return new Set<string>();
    const set = new Set<string>();
    const addWithAncestors = (raw: string) => {
      const path = raw.trim();
      if (!path) return;
      const isAbs = path.startsWith('/');
      const body = isAbs ? path.slice(1) : path;
      const parts = body.split('/').filter(Boolean);
      for (let i = 1; i <= parts.length; i++) {
        const sub = parts.slice(0, i).join('/');
        set.add(isAbs ? `/${sub}` : sub);
      }
    };
    packages.forEach(addWithAncestors);
    // A snippet may reference a package path that's not in `packages` yet.
    snippets.forEach((s) => {
      if (s.package) addWithAncestors(s.package);
    });
    return set;
  }, [packages, snippets, isVisible]);

  // Track every package we've ever observed so we can tell "new" from
  // "previously-seen-but-user-collapsed". Without this, any unrelated refresh
  // that reduced prev.size (because the user collapsed a row) would
  // incorrectly trip a bulk re-expand.
  const seenPackagesRef = useRef<Set<string>>(new Set());

  // Default: auto-expand packages the first time they appear, so the user sees
  // everything without drilling in. After that, respect the user's collapse
  // choices across unrelated refreshes.
  useEffect(() => {
    if (!isVisible) return;
    const seen = seenPackagesRef.current;
    const newlySeen: string[] = [];
    normalizedPackages.forEach((p) => {
      if (!seen.has(p)) {
        seen.add(p);
        newlySeen.push(p);
      }
    });
    if (newlySeen.length === 0) return;
    setExpandedPaths((prev) => {
      const next = new Set(prev);
      // Only auto-expand root packages on first sight — expanding the full
      // tree upfront was freezing the panel on large snippet libraries.
      newlySeen.filter(isRootPackagePath).forEach((p) => next.add(p));
      return next;
    });
  }, [normalizedPackages, isVisible]);

  const snippetIndex = useMemo(() => {
    if (!isVisible) {
      return {
        snippetsByPackage: new Map<string, Snippet[]>(),
        descendantCountByPackage: new Map<string, number>(),
      };
    }
    const snippetsByPackage = new Map<string, Snippet[]>();
    const descendantCountByPackage = new Map<string, number>();

    const bumpCount = (path: string) => {
      descendantCountByPackage.set(path, (descendantCountByPackage.get(path) ?? 0) + 1);
    };

    for (const snippet of snippets) {
      const pkg = snippet.package || '';
      const bucket = snippetsByPackage.get(pkg);
      if (bucket) bucket.push(snippet);
      else snippetsByPackage.set(pkg, [snippet]);

      if (pkg === '') {
        bumpCount('');
        continue;
      }

      let path = pkg;
      while (true) {
        bumpCount(path);
        const slash = path.lastIndexOf('/');
        if (slash < 0) break;
        path = path.slice(0, slash);
      }
    }

    for (const bucket of snippetsByPackage.values()) {
      bucket.sort((a, b) => a.label.localeCompare(b.label));
    }

    return { snippetsByPackage, descendantCountByPackage };
  }, [snippets, isVisible]);

  const togglePackage = useCallback((path: string) => {
    setExpandedPaths((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);

  // When search is active, flatten everything (no tree, no packages).
  const searchMatches = useMemo(() => {
    if (!isVisible) return null;
    const q = search.trim().toLowerCase();
    if (!q) return null;
    return snippets.filter(
      (s) =>
        s.label.toLowerCase().includes(q) ||
        s.command.toLowerCase().includes(q),
    );
  }, [snippets, search, isVisible]);

  const rows = useMemo<TreeRow[]>(() => {
    if (!isVisible) return [];
    if (searchMatches !== null) return [];

    const out: TreeRow[] = [];
    const paths: string[] = [];
    normalizedPackages.forEach((p) => paths.push(p));

    const childPackagesOf = (parent: string | null): string[] => {
      const prefix = parent === null ? '' : parent + '/';
      return paths
        .filter((p) => {
          if (parent === null) {
            // Root-level: no "/" inside the body
            const body = p.startsWith('/') ? p.slice(1) : p;
            return !body.includes('/');
          }
          if (!p.startsWith(prefix)) return false;
          const rest = p.slice(prefix.length);
          return rest.length > 0 && !rest.includes('/');
        })
        .sort((a, b) => pkgDisplayName(a).localeCompare(pkgDisplayName(b)));
    };

    const snippetsIn = (pkg: string | null): Snippet[] =>
      snippetIndex.snippetsByPackage.get(pkg ?? '') ?? [];

    const walk = (pkg: string, depth: number) => {
      const children = childPackagesOf(pkg);
      const localSnippets = snippetsIn(pkg);
      const hasChildren = children.length > 0 || localSnippets.length > 0;
      const isExpanded = expandedPaths.has(pkg);

      out.push({
        type: 'package',
        id: pkg,
        path: pkg,
        name: pkgDisplayName(pkg),
        depth,
        count: snippetIndex.descendantCountByPackage.get(pkg) ?? 0,
        hasChildren,
        isExpanded,
      });

      if (!isExpanded) return;
      children.forEach((c) => walk(c, depth + 1));
      localSnippets.forEach((s) =>
        out.push({ type: 'snippet', id: s.id, depth: depth + 1, snippet: s, packagePath: pkg }),
      );
    };

    // Orphan / uncategorized snippets first (package === '')
    snippetsIn(null).forEach((s) =>
      out.push({ type: 'snippet', id: s.id, depth: 0, snippet: s, packagePath: '' }),
    );
    childPackagesOf(null).forEach((root) => walk(root, 0));

    return out;
  }, [normalizedPackages, snippetIndex, expandedPaths, searchMatches, isVisible]);

  type ScriptsListItem =
    | { key: string; kind: 'search'; snippet: Snippet }
    | { key: string; kind: 'package'; row: Extract<TreeRow, { type: 'package' }>; countLabel: string }
    | { key: string; kind: 'snippet'; row: Extract<TreeRow, { type: 'snippet' }> };

  const listItems = useMemo((): ScriptsListItem[] => {
    if (!isVisible) return [];
    if (searchMatches !== null) {
      return searchMatches.map((snippet) => ({
        key: `search:${snippet.id}`,
        kind: 'search',
        snippet,
      }));
    }
    return rows.flatMap((row): ScriptsListItem[] => {
      if (row.type === 'package') {
        return [{
          key: `pkg:${row.id}`,
          kind: 'package',
          row,
          countLabel: t('snippets.package.count', { count: row.count }),
        }];
      }
      return [{
        key: `snip:${row.id}`,
        kind: 'snippet',
        row,
      }];
    });
  }, [rows, searchMatches, t, isVisible]);

  const handleSnippetClick = useCallback(
    (snippet: Snippet) => {
      onSnippetClick(snippet);
    },
    [onSnippetClick],
  );

  const handleAddSnippet = useCallback(() => {
    // Let the App shell listen and navigate to the Snippets section with
    // the "add" panel pre-opened, so the user does not have to leave the
    // terminal to jump back and click "New Snippet".
    window.dispatchEvent(new CustomEvent('netcatty:snippets:add'));
  }, []);

  const handleEditSnippet = useCallback((snippet: Snippet) => {
    window.dispatchEvent(
      new CustomEvent('netcatty:snippets:edit', { detail: { snippet } }),
    );
  }, []);

  const handleDeleteSnippet = useCallback((id: string) => {
    window.dispatchEvent(
      new CustomEvent('netcatty:snippets:delete', { detail: { id } }),
    );
  }, []);

  if (!isVisible) return null;

  const hasAnyContent = snippets.length > 0 || packages.length > 0;

  return (
    <TooltipProvider delayDuration={300}>
    <div
      className="h-full flex flex-col bg-background overflow-hidden"
      data-section="snippets-panel"
    >
      {/* Search + Add */}
      <div className="shrink-0 px-2 py-1.5 border-b border-border/50 flex items-center gap-1.5">
        <div className="relative flex-1 min-w-0">
          <Search size={12} className="absolute left-2 top-1/2 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('snippets.searchPlaceholder')}
            className="h-7 pl-7 text-xs bg-muted/30 border-none"
          />
        </div>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={handleAddSnippet}
              aria-label={t('snippets.action.newSnippet')}
              className="shrink-0 h-7 w-7 flex items-center justify-center rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
            >
              <Plus size={14} />
            </button>
          </TooltipTrigger>
          <TooltipContent>{t('snippets.action.newSnippet')}</TooltipContent>
        </Tooltip>
      </div>

      {/* Content */}
      <div className="flex-1 min-h-0">
        {!hasAnyContent ? (
          <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
            <Zap size={24} className="opacity-40 mb-2" />
            <span className="text-xs">{t('terminal.toolbar.noSnippets')}</span>
          </div>
        ) : hasAnyContent && searchMatches !== null && searchMatches.length === 0 ? (
          <div className="px-3 py-4 text-xs text-muted-foreground italic text-center">
            {t('common.noResultsFound')}
          </div>
        ) : (
          <FixedSizeVirtualList
            className="h-full"
            contentClassName="py-1"
            items={listItems}
            itemHeight={SCRIPT_ROW_HEIGHT}
            getItemKey={(item) => item.key}
            renderItem={(item) => {
              if (item.kind === 'search') {
                return (
                  <SnippetRow
                    snippet={item.snippet}
                    depth={0}
                    subtitle={item.snippet.package || t('terminal.toolbar.library')}
                    onClick={() => handleSnippetClick(item.snippet)}
                    onEdit={() => handleEditSnippet(item.snippet)}
                    onDelete={() => handleDeleteSnippet(item.snippet.id)}
                    editLabel={t('action.edit')}
                    deleteLabel={t('action.delete')}
                  />
                );
              }
              if (item.kind === 'package') {
                return (
                  <PackageRow
                    row={item.row}
                    countLabel={item.countLabel}
                    onToggle={() => togglePackage(item.row.path)}
                  />
                );
              }
              return (
                <SnippetRow
                  snippet={item.row.snippet}
                  depth={item.row.depth}
                  onClick={() => handleSnippetClick(item.row.snippet)}
                  onEdit={() => handleEditSnippet(item.row.snippet)}
                  onDelete={() => handleDeleteSnippet(item.row.snippet.id)}
                  editLabel={t('action.edit')}
                  deleteLabel={t('action.delete')}
                />
              );
            }}
          />
        )}
      </div>
    </div>
    </TooltipProvider>
  );
};

interface PackageRowProps {
  row: Extract<TreeRow, { type: 'package' }>;
  countLabel: string;
  onToggle: () => void;
}

const PackageRow = memo<PackageRowProps>(({ row, countLabel, onToggle }) => (
  <button
    type="button"
    onClick={onToggle}
    className="w-full flex items-center gap-1.5 pr-3 py-1.5 text-left hover:bg-accent/50 transition-colors"
    style={{ paddingLeft: 8 + row.depth * 14 }}
  >
    <ChevronRight
      size={12}
      className={cn(
        'shrink-0 text-muted-foreground transition-transform',
        row.isExpanded && 'rotate-90',
        !row.hasChildren && 'opacity-0',
      )}
    />
    <Package size={12} className="shrink-0 text-primary/80" />
    <span className="flex-1 min-w-0 truncate text-xs font-medium">{row.name}</span>
    <span className="shrink-0 text-[10px] text-muted-foreground tabular-nums">{countLabel}</span>
  </button>
));
PackageRow.displayName = 'PackageRow';

interface SnippetRowProps {
  snippet: Snippet;
  depth: number;
  subtitle?: string;
  onClick: () => void;
  onEdit: () => void;
  onDelete: () => void;
  editLabel: string;
  deleteLabel: string;
}

const SnippetRow = memo<SnippetRowProps>(({
  snippet,
  depth,
  subtitle,
  onClick,
  onEdit,
  onDelete,
  editLabel,
  deleteLabel,
}) => (
  <ContextMenu>
    <ContextMenuTrigger asChild>
      <div>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={onClick}
              className="w-full flex items-center gap-1.5 pr-3 py-1.5 text-left hover:bg-accent/50 transition-colors overflow-hidden"
              style={{ paddingLeft: 8 + depth * 14 }}
            >
              {/* Hidden chevron column mirrors PackageRow's layout so the
                  snippet icon lines up exactly with the package icon above. */}
              <ChevronRight size={12} className="shrink-0 opacity-0" aria-hidden />
              <FileCode size={12} className="shrink-0 text-muted-foreground" />
              <span className="flex-1 min-w-0 truncate text-xs font-medium">{snippet.label}</span>
              {subtitle && (
                <span className="shrink-0 max-w-[40%] truncate text-[10px] text-muted-foreground">
                  {subtitle}
                </span>
              )}
            </button>
          </TooltipTrigger>
          <TooltipContent side="right" align="start" className="max-w-[480px]">
            <div className="font-medium text-xs mb-1 break-all">{snippet.label}</div>
            <pre className="font-mono text-[11px] whitespace-pre-wrap break-all leading-snug opacity-90">
              {snippet.command}
            </pre>
          </TooltipContent>
        </Tooltip>
      </div>
    </ContextMenuTrigger>
    <ContextMenuContent>
      <ContextMenuItem onClick={onEdit}>
        <Edit2 className="mr-2 h-4 w-4" /> {editLabel}
      </ContextMenuItem>
      <ContextMenuItem className="text-destructive" onClick={onDelete}>
        <Trash2 className="mr-2 h-4 w-4" /> {deleteLabel}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>
));
SnippetRow.displayName = 'SnippetRow';

export const ScriptsSidePanel = memo(ScriptsSidePanelInner);
ScriptsSidePanel.displayName = 'ScriptsSidePanel';
