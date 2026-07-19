import { X } from 'lucide-react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { usePluginContributions } from '../../application/state/usePluginContributions';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Button } from '../ui/button';

export const OPEN_PLUGIN_VIEW_EVENT = 'netcatty:open-plugin-view';

interface OpenPluginViewDetail {
  viewId: string;
  context?: Record<string, unknown>;
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
  const contributions = usePluginContributions();
  const {
    snapshot,
    executeCommand,
    openView,
    closeView,
    setViewBounds,
    setEnvironment,
  } = contributions;
  const [requested, setRequested] = useState<OpenPluginViewDetail | null>(null);
  const [instance, setInstance] = useState<{ id: string; viewId: string } | null>(null);
  const instanceRef = useRef<{ id: string; viewId: string } | null>(null);
  const closeViewRef = useRef(closeView);
  const mountRef = useRef<HTMLDivElement>(null);
  const activeView = useMemo(() => snapshot.plugins
    .flatMap((plugin) => plugin.views.map((view) => ({ plugin, view })))
    .find(({ view }) => view.id === requested?.viewId) ?? null, [snapshot.plugins, requested?.viewId]);
  const activeViewId = activeView?.view.id;

  useEffect(() => { closeViewRef.current = closeView; }, [closeView]);

  useEffect(() => {
    const bindings = snapshot.plugins.flatMap((plugin) => plugin.keybindings)
      .filter((binding) => binding.enabled)
      .sort((left, right) => `${left.command}:${left.key}`.localeCompare(`${right.command}:${right.key}`));
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest('input, textarea, select, [contenteditable="true"]')) return;
      const platformKey = /Mac|iPhone|iPad/.test(navigator.platform)
        ? 'mac'
        : /Win/.test(navigator.platform)
          ? 'windows'
          : 'linux';
      const pressed = [
        event.metaKey ? 'meta' : '',
        event.ctrlKey ? 'ctrl' : '',
        event.altKey ? 'alt' : '',
        event.shiftKey ? 'shift' : '',
        event.key.length === 1 ? event.key.toLowerCase() : event.key.toLowerCase(),
      ].filter(Boolean).join('+');
      const binding = bindings.find((candidate) => {
        const declared = candidate[platformKey] ?? candidate.key;
        const normalized = declared.toLowerCase()
          .replace(/commandorcontrol|cmdorctrl|mod/gu, /Mac|iPhone|iPad/.test(navigator.platform) ? 'meta' : 'ctrl')
          .replace(/command|cmd/gu, 'meta')
          .replace(/control/gu, 'ctrl')
          .replace(/option/gu, 'alt')
          .replace(/\s*\+\s*/gu, '+');
        return normalized === pressed;
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

  const close = useCallback(async () => {
    const current = instanceRef.current;
    instanceRef.current = null;
    setInstance(null);
    setRequested(null);
    if (current) await closeView(current.id);
  }, [closeView]);

  useEffect(() => {
    if (!instance) return;
    if (requested?.viewId === instance.viewId && activeViewId === instance.viewId) return;
    instanceRef.current = null;
    setInstance(null);
    void closeView(instance.id);
  }, [activeViewId, closeView, instance, requested?.viewId]);

  useEffect(() => {
    if (!activeViewId || !mountRef.current || instance) return;
    let cancelled = false;
    const bounds = mountRef.current.getBoundingClientRect();
    void openView({
      viewId: activeViewId,
      scopeId: `window:${window.location.pathname || 'main'}`,
      bounds: {
        x: Math.round(bounds.x),
        y: Math.round(bounds.y),
        width: Math.max(1, Math.round(bounds.width)),
        height: Math.max(1, Math.round(bounds.height)),
      },
      context: requested?.context,
    }).then((result) => {
      if (cancelled) {
        void closeView(result.instanceId);
        return;
      }
      const opened = { id: result.instanceId, viewId: activeViewId };
      instanceRef.current = opened;
      setInstance(opened);
    }).catch(() => {
      if (!cancelled) setRequested(null);
    });
    return () => { cancelled = true; };
  }, [activeViewId, closeView, instance, openView, requested?.context]);

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
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const highContrast = window.matchMedia('(forced-colors: active)').matches
      || window.matchMedia('(prefers-contrast: more)').matches;
    const styles = getComputedStyle(document.documentElement);
    const themeTokens = Object.fromEntries([
      '--background', '--foreground', '--muted', '--muted-foreground', '--border', '--primary', '--primary-foreground',
    ].map((name) => [name, styles.getPropertyValue(name).trim()]));
    void setEnvironment({ locale, theme, reducedMotion, highContrast, themeTokens }).catch(() => {});
  }, [contributions.available, locale, setEnvironment, theme]);

  useEffect(() => () => {
    const current = instanceRef.current;
    instanceRef.current = null;
    if (current) void closeViewRef.current(current.id);
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
