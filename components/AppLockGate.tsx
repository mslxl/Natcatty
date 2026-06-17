import React, { useEffect, useRef } from 'react';

import { I18nProvider } from '../application/i18n/I18nProvider';
import { type AppLockReason, useAppLockState } from '../application/state/useAppLockState';
import { useSettingsState } from '../application/state/useSettingsState';
import { netcattyBridge } from '../infrastructure/services/netcattyBridge';
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
  locked: boolean;
  lockReason: AppLockReason | null;
  hasRenderedChildren: boolean;
}): boolean {
  return !(
    input.locked &&
    input.lockReason === 'startup' &&
    !input.hasRenderedChildren
  );
}

export const AppLockGate: React.FC<AppLockGateProps> = ({
  children,
  notifyRendererReady = true,
}) => {
  const settings = useSettingsState();
  const appLock = useAppLockState(settings.appLockSettings);
  const hasRenderedChildrenRef = useRef(false);
  const renderChildren = shouldRenderAppLockGateChildren({
    locked: appLock.locked,
    lockReason: appLock.lockReason,
    hasRenderedChildren: hasRenderedChildrenRef.current,
  });
  if (renderChildren) {
    hasRenderedChildrenRef.current = true;
  }

  useEffect(() => {
    try {
      const splash = document.getElementById('splash');
      if (splash) {
        splash.classList.add('fade-out');
        setTimeout(() => splash.remove(), 200);
      }
      if (notifyRendererReady) {
        netcattyBridge.get()?.rendererReady?.();
      }
    } catch {
      // ignore
    }
  }, [notifyRendererReady]);

  useEffect(() => {
    const unsubscribe = netcattyBridge.get()?.onAppLockReopen?.(() => {
      appLock.lockNow('startup');
    });
    return () => unsubscribe?.();
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
