import { X } from 'lucide-react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  normalizePluginKeyboardEvent,
  normalizePluginShortcut,
  resolvePluginShortcutPlatform,
} from '../../application/state/pluginKeybindings';
import {
  canRetainPluginViewInScope,
  resolvePluginRetainedViewKey,
  resolvePluginViewWindowScope,
} from '../../application/state/pluginViewScopes';
import { usePluginContributions } from '../../application/state/usePluginContributions';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Button } from '../ui/button';

export const OPEN_PLUGIN_VIEW_EVENT = 'netcatty:open-plugin-view';

interface OpenPluginViewDetail {
  viewId: string;
  context?: Record<string, unknown>;
}

interface HostedPluginView {
  id: string;
  viewId: string;
  scopeId: string;
  retainContextWhenHidden: boolean;
}

export function requestOpenPluginView(detail: OpenPluginViewDetail) {
  window.dispatchEvent(new CustomEvent(OPEN_PLUGIN_VIEW_EVENT, { detail }));
}

export function PluginContributionHost({
  locale,
  theme,
}: {
  locale: string;
  theme: string;
}) {
  const { t } = useI18n();
  const [requested, setRequested] = useState<OpenPluginViewDetail | null>(null);
  const contributions = usePluginContributions({
    context: { 'netcatty.surface': 'keybinding' },
  });
  const viewContributions = usePluginContributions({
    context: requested?.context ?? { 'netcatty.surface': 'view' },
  });
  const {
    snapshot,
    executeCommand,
    openView,
    closeView,
    setViewBounds,
    setViewVisibility,
    setEnvironment,
  } = contributions;
  const [instance, setInstance] = useState<HostedPluginView | null>(null);
  const instanceRef = useRef<HostedPluginView | null>(null);
  const retainedViewsRef = useRef(new Map<string, HostedPluginView>());
  const closeViewRef = useRef(closeView);
  const mountRef = useRef<HTMLDivElement>(null);
  const activeView = useMemo(() => viewContributions.snapshot.plugins
    .flatMap((plugin) => plugin.views.map((view) => ({ plugin, view })))
    .find(({ view }) => view.id === requested?.viewId && view.visible) ?? null,
  [requested?.viewId, viewContributions.snapshot.plugins]);
  const activeViewId = activeView?.view.id;
  const viewScopeId = typeof window === 'undefined'
    ? 'window:server'
    : resolvePluginViewWindowScope(window.location);
  const retainedViewKey = activeViewId
    ? resolvePluginRetainedViewKey(activeViewId, viewScopeId)
    : null;

  useEffect(() => { closeViewRef.current = closeView; }, [closeView]);

  useEffect(() => {
    const bindings = snapshot.plugins.flatMap((plugin) => plugin.keybindings)
      .filter((binding) => binding.enabled)
      .sort((left, right) => `${left.command}:${left.key}`.localeCompare(`${right.command}:${right.key}`));
    const platformKey = resolvePluginShortcutPlatform(navigator.platform);
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest('input, textarea, select, [contenteditable="true"]')) return;
      const pressed = normalizePluginKeyboardEvent(event);
      if (!pressed) return;
      const binding = bindings.find((candidate) => {
        const declared = candidate[platformKey] ?? candidate.key;
        return normalizePluginShortcut(declared, platformKey) === pressed;
      });
      if (!binding) return;
      event.preventDefault();
      void executeCommand(binding.command, binding.args, { 'netcatty.surface': 'keybinding' });
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [executeCommand, snapshot.plugins]);

  useEffect(() => {
    const listener = (event: Event) => setRequested((event as CustomEvent<OpenPluginViewDetail>).detail);
    window.addEventListener(OPEN_PLUGIN_VIEW_EVENT, listener);
    return () => window.removeEventListener(OPEN_PLUGIN_VIEW_EVENT, listener);
  }, []);

  const hideOrClose = useCallback(async (current: HostedPluginView) => {
    if (!current.retainContextWhenHidden) {
      await closeView(current.id);
      return;
    }
    const key = resolvePluginRetainedViewKey(current.viewId, current.scopeId);
    retainedViewsRef.current.set(key, current);
    try {
      await setViewVisibility(current.id, false);
    } catch {
      retainedViewsRef.current.delete(key);
      await closeView(current.id);
    }
  }, [closeView, setViewVisibility]);

  const close = useCallback(async () => {
    const current = instanceRef.current;
    instanceRef.current = null;
    setInstance(null);
    setRequested(null);
    if (current) await hideOrClose(current);
  }, [hideOrClose]);

  useEffect(() => {
    for (const [key, retained] of retainedViewsRef.current) {
      if (canRetainPluginViewInScope(retained.scopeId, viewScopeId)) continue;
      retainedViewsRef.current.delete(key);
      void closeView(retained.id).catch(() => {});
    }
  }, [closeView, viewScopeId]);

  useEffect(() => {
    if (!instance) return;
    if (requested?.viewId === instance.viewId
      && activeViewId === instance.viewId
      && instance.scopeId === viewScopeId) return;
    instanceRef.current = null;
    setInstance(null);
    if (canRetainPluginViewInScope(instance.scopeId, viewScopeId)) {
      void hideOrClose(instance);
    } else {
      void closeView(instance.id).catch(() => {});
    }
  }, [activeViewId, closeView, hideOrClose, instance, requested?.viewId, viewScopeId]);

  useEffect(() => {
    if (!activeViewId || !retainedViewKey || !mountRef.current || instance) return;
    let cancelled = false;
    const bounds = mountRef.current.getBoundingClientRect();
    const nextBounds = {
      x: Math.round(bounds.x),
      y: Math.round(bounds.y),
      width: Math.max(1, Math.round(bounds.width)),
      height: Math.max(1, Math.round(bounds.height)),
    };
    void (async () => {
      let opened = retainedViewsRef.current.get(retainedViewKey) ?? null;
      if (opened) {
        retainedViewsRef.current.delete(retainedViewKey);
        try {
          await setViewBounds(opened.id, nextBounds);
          await setViewVisibility(opened.id, true);
        } catch {
          try { await closeView(opened.id); } catch {}
          opened = null;
        }
      }
      if (!opened) {
        const result = await openView({
          viewId: activeViewId,
          scopeId: viewScopeId,
          bounds: nextBounds,
          context: requested?.context,
        });
        opened = {
          id: result.instanceId,
          viewId: activeViewId,
          scopeId: viewScopeId,
          retainContextWhenHidden: activeView.view.retainContextWhenHidden === true,
        };
      }
      if (cancelled) {
        await hideOrClose(opened);
        return;
      }
      instanceRef.current = opened;
      setInstance(opened);
    })().catch(() => {
      if (!cancelled) setRequested(null);
    });
    return () => { cancelled = true; };
  }, [
    activeView?.view.retainContextWhenHidden,
    activeViewId,
    closeView,
    hideOrClose,
    instance,
    openView,
    retainedViewKey,
    requested?.context,
    setViewBounds,
    setViewVisibility,
    viewScopeId,
  ]);

  useEffect(() => {
    if (!instance || !mountRef.current) return;
    const update = () => {
      const bounds = mountRef.current?.getBoundingClientRect();
      if (!bounds) return;
      void setViewBounds(instance.id, {
        x: Math.round(bounds.x),
        y: Math.round(bounds.y),
        width: Math.max(1, Math.round(bounds.width)),
        height: Math.max(1, Math.round(bounds.height)),
      });
    };
    const observer = new ResizeObserver(update);
    observer.observe(mountRef.current);
    window.addEventListener('resize', update);
    update();
    return () => {
      observer.disconnect();
      window.removeEventListener('resize', update);
    };
  }, [instance, setViewBounds]);

  useEffect(() => {
    if (!contributions.available) return;
    const reducedMotionQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
    const forcedColorsQuery = window.matchMedia('(forced-colors: active)');
    const contrastQuery = window.matchMedia('(prefers-contrast: more)');
    let frame = 0;
    const publish = () => {
      frame = 0;
      const styles = getComputedStyle(document.documentElement);
      const themeTokens = Object.fromEntries([
        '--background', '--foreground', '--muted', '--muted-foreground', '--border', '--primary', '--primary-foreground',
      ].map((name) => [name, styles.getPropertyValue(name).trim()]));
      void setEnvironment({
        locale,
        theme,
        reducedMotion: reducedMotionQuery.matches,
        highContrast: forcedColorsQuery.matches || contrastQuery.matches,
        themeTokens,
      }).catch(() => {});
    };
    const schedulePublish = () => {
      if (frame) cancelAnimationFrame(frame);
      frame = requestAnimationFrame(publish);
    };
    const queries = [reducedMotionQuery, forcedColorsQuery, contrastQuery];
    for (const query of queries) query.addEventListener?.('change', schedulePublish);
    const observer = new MutationObserver(schedulePublish);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style'] });
    publish();
    return () => {
      if (frame) cancelAnimationFrame(frame);
      observer.disconnect();
      for (const query of queries) query.removeEventListener?.('change', schedulePublish);
    };
  }, [contributions.available, locale, setEnvironment, theme]);

  useEffect(() => () => {
    const current = instanceRef.current;
    instanceRef.current = null;
    const retained = [...retainedViewsRef.current.values()];
    retainedViewsRef.current.clear();
    const ids = new Set([current?.id, ...retained.map((view) => view.id)].filter(Boolean));
    for (const id of ids) void closeViewRef.current(id as string);
  }, []);

  if (!requested || !activeView) return null;
  const location = activeView.view.location;
  const containerClass = location === 'aside'
    ? 'absolute inset-y-0 right-0 z-40 w-[420px] border-l border-border bg-background shadow-2xl'
    : location === 'panel'
      ? 'absolute inset-x-0 bottom-0 z-40 h-[42%] border-t border-border bg-background shadow-2xl'
      : location === 'modal'
        ? 'fixed left-1/2 top-1/2 z-50 h-[70vh] w-[min(800px,85vw)] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-border bg-background shadow-2xl'
        : 'absolute inset-0 z-40 bg-background';

  return (
    <section
      className={`${containerClass} flex flex-col`}
      role={location === 'modal' ? 'dialog' : 'region'}
      aria-modal={location === 'modal' ? true : undefined}
      aria-label={activeView.view.title}
    >
      <header className="app-no-drag flex h-11 shrink-0 items-center justify-between border-b border-border px-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{activeView.view.title}</div>
          <div className="truncate text-[10px] text-muted-foreground">{activeView.plugin.displayName}</div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          onClick={() => void close()}
          aria-label={t('common.close')}
          autoFocus={location === 'modal'}
        >
          <X size={14} />
        </Button>
      </header>
      <div ref={mountRef} className="min-h-0 flex-1" />
    </section>
  );
}
