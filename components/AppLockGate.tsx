import React, { useEffect, useRef } from 'react';

import { I18nProvider } from '../application/i18n/I18nProvider';
import { useAppLockBridge } from '../application/state/useAppLockBridge';
import { type AppLockReason, useAppLockState } from '../application/state/useAppLockState';
import { useSettingsState } from '../application/state/useSettingsState';
import { ToastProvider } from './ui/toast';
import { TooltipProvider } from './ui/tooltip';
import { AppLockOverlay } from './AppLockOverlay';

type SettingsState = ReturnType<typeof useSettingsState>;
type AppLockState = ReturnType<typeof useAppLockState>;

export interface AppLockGateRenderContext {
  settings: SettingsState;
  appLock: AppLockState;
}

interface AppLockGateProps {
  children: (ctx: AppLockGateRenderContext) => React.ReactNode;
  notifyRendererReady?: boolean;
}

export function shouldRenderAppLockGateChildren(input: {
  initialized: boolean;
  locked: boolean;
  lockReason: AppLockReason | null;
  hasRenderedChildren: boolean;
}): boolean {
  if (!input.initialized && !input.hasRenderedChildren) return false;
  return !(
    input.locked &&
    input.lockReason === 'startup' &&
    !input.hasRenderedChildren
  );
}

export function shouldNotifyAppLockGateRendererReady(input: {
  notifyRendererReady: boolean;
  renderChildren: boolean;
}): boolean {
  return input.notifyRendererReady && input.renderChildren;
}

export const AppLockGate: React.FC<AppLockGateProps> = ({
  children,
  notifyRendererReady = true,
}) => {
  const settings = useSettingsState();
  const appLock = useAppLockState(settings.appLockSettings);
  const {
    notifyRendererReady: notifyAppLockRendererReady,
    onAppLockReopen,
  } = useAppLockBridge();
  const hasRenderedChildrenRef = useRef(false);
  const renderChildren = shouldRenderAppLockGateChildren({
    initialized: appLock.initialized,
    locked: appLock.locked,
    lockReason: appLock.lockReason,
    hasRenderedChildren: hasRenderedChildrenRef.current,
  });
  if (renderChildren) {
    hasRenderedChildrenRef.current = true;
  }
  const shouldNotifyRendererReady = shouldNotifyAppLockGateRendererReady({
    notifyRendererReady,
    renderChildren,
  });

  useEffect(() => {
    try {
      const splash = document.getElementById('splash');
      if (splash) {
        splash.classList.add('fade-out');
        setTimeout(() => splash.remove(), 200);
      }
      if (shouldNotifyRendererReady) {
        notifyAppLockRendererReady();
      }
    } catch {
      // ignore
    }
  }, [notifyAppLockRendererReady, shouldNotifyRendererReady]);

  useEffect(() => {
    const unsubscribe = onAppLockReopen(() => {
      void appLock.resync?.();
    });
    return () => unsubscribe?.();
  }, [appLock, onAppLockReopen]);

  useEffect(() => {
    const handleVisibilityOrFocus = () => {
      void appLock.resync?.();
    };
    window.addEventListener('focus', handleVisibilityOrFocus);
    document.addEventListener('visibilitychange', handleVisibilityOrFocus);
    return () => {
      window.removeEventListener('focus', handleVisibilityOrFocus);
      document.removeEventListener('visibilitychange', handleVisibilityOrFocus);
    };
  }, [appLock]);

  return (
    <I18nProvider locale={settings.uiLanguage}>
      <ToastProvider>
        <TooltipProvider delayDuration={300}>
          {renderChildren ? children({ settings, appLock }) : null}
          <AppLockOverlay
            locked={appLock.locked}
            reason={appLock.lockReason}
            onUnlock={appLock.unlock}
          />
        </TooltipProvider>
      </ToastProvider>
    </I18nProvider>
  );
};
