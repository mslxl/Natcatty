import { X } from 'lucide-react';
import React, { lazy, Suspense, useEffect, useMemo, useState } from 'react';
import { I18nProvider, useI18n } from '../application/i18n/I18nProvider';
import { canReuseTerminalConnection } from '../application/state/terminalConnectionReuse';
import { useSettingsState } from '../application/state/useSettingsState';
import { useTerminalPopupWindow } from '../application/state/useTerminalPopupWindow';
import { useVaultState } from '../application/state/useVaultState';
import { useWindowControls } from '../application/state/useWindowControls';
import type { TerminalPopupPayload } from '../domain/systemManager/types';
import type { Host } from '../types';

const Terminal = lazy(() => import('./Terminal'));

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);

/** Fallback when the parent session's host is no longer in the vault (e.g. quick connect). */
function buildHostFromSession(source: TerminalPopupPayload['sourceSession']): Host {
  return {
    id: source.hostId,
    label: source.hostLabel,
    hostname: source.hostname,
    username: source.username,
    port: source.port ?? (source.protocol === 'local' ? undefined : 22),
    protocol: source.protocol === 'local' ? 'local' : 'ssh',
    tags: [],
    os: 'linux',
    moshEnabled: source.moshEnabled,
    etEnabled: source.etEnabled,
    charset: source.charset,
  };
}

function TerminalPopupPageInner() {
  const { t } = useI18n();
  const { close, setWindowTitle, onPopupConfig } = useTerminalPopupWindow();
  const { notifyRendererReady, onWindowCommandCloseRequested } = useWindowControls();
  const settings = useSettingsState();
  const { isInitialized: vaultInitialized, hosts, keys, identities, knownHosts, snippets, snippetPackages } = useVaultState();
  const [config, setConfig] = useState<TerminalPopupPayload | null>(null);
  const sessionId = useMemo(() => crypto.randomUUID(), []);

  useEffect(() => {
    const unsubscribe = onPopupConfig((payload) => {
      setConfig(payload);
      if (payload.title) {
        void setWindowTitle(payload.title);
      }
    });
    // Main delivers the popup payload as soon as the renderer reports ready
    // (and destroys the window if it never does) — so report ready only after
    // the config listener above is registered.
    notifyRendererReady();
    return unsubscribe;
  }, [notifyRendererReady, onPopupConfig, setWindowTitle]);

  useEffect(() => {
    return onWindowCommandCloseRequested(() => {
      void close();
    });
  }, [close, onWindowCommandCloseRequested]);

  const host = useMemo(() => {
    if (!config) return null;
    const vaultHost = hosts.find((h) => h.id === config.sourceSession.hostId);
    return vaultHost ?? buildHostFromSession(config.sourceSession);
  }, [config, hosts]);

  const reuseId = useMemo(() => {
    if (!config) return undefined;
    return canReuseTerminalConnection(config.sourceSession)
      ? config.parentSessionId
      : undefined;
  }, [config]);

  const ready = Boolean(config && host && vaultInitialized);

  return (
    <div className="h-screen flex flex-col bg-background text-foreground" data-section="terminal-popup">
      <div
        className="app-drag shrink-0 h-9 flex items-center border-b border-border/50"
        data-section="terminal-popup-titlebar"
      >
        <div className={isMac ? 'w-[76px] shrink-0' : 'w-3 shrink-0'} />
        <div className="flex-1 min-w-0 text-center text-xs text-muted-foreground truncate px-2">
          {config?.title ?? ''}
        </div>
        {isMac ? (
          <div className="w-[76px] shrink-0" />
        ) : (
          <button
            type="button"
            onClick={() => void close()}
            className="app-no-drag shrink-0 w-9 h-9 flex items-center justify-center hover:bg-destructive/20 hover:text-destructive transition-colors text-muted-foreground"
            aria-label={t('common.close')}
          >
            <X size={14} />
          </button>
        )}
      </div>
      {!ready || !config || !host ? (
        <div className="flex-1 flex items-center justify-center text-muted-foreground text-sm">
          {t('systemManager.popup.loading')}
        </div>
      ) : (
        <div className="flex-1 min-h-0 flex flex-col">
          <Suspense fallback={<div className="flex-1 flex items-center justify-center text-sm opacity-70">…</div>}>
            <Terminal
              host={host}
              keys={keys}
              identities={identities}
              snippets={snippets}
              snippetPackages={snippetPackages}
              compactToolbar
              knownHosts={knownHosts}
              isVisible
              isFocused
              fontFamilyId={settings.terminalFontFamilyId}
              fontSize={settings.terminalFontSize}
              terminalTheme={settings.currentTerminalTheme}
              followAppTerminalTheme={settings.followAppTerminalTheme}
              accentMode={settings.accentMode}
              customAccent={settings.customAccent}
              terminalSettings={settings.terminalSettings}
              sessionId={sessionId}
              startupCommand={config.startupCommand}
              reuseConnectionFromSessionId={reuseId}
              onCloseSession={() => {
                void close();
              }}
              onSessionExit={() => {
                void close();
              }}
              onStatusChange={() => {}}
            />
          </Suspense>
        </div>
      )}
    </div>
  );
}

export default function TerminalPopupPage() {
  const settings = useSettingsState();
  return (
    <I18nProvider locale={settings.uiLanguage}>
      <TerminalPopupPageInner />
    </I18nProvider>
  );
}
