import React, { useEffect } from 'react';

import { I18nProvider } from '../application/i18n/I18nProvider';
import { useAppLockState } from '../application/state/useAppLockState';
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
}

export const AppLockGate: React.FC<AppLockGateProps> = ({ children }) => {
  const settings = useSettingsState();
  const appLock = useAppLockState(settings.appLockSettings);

  useEffect(() => {
    try {
      const splash = document.getElementById('splash');
      if (splash) {
        splash.classList.add('fade-out');
        setTimeout(() => splash.remove(), 200);
      }
      netcattyBridge.get()?.rendererReady?.();
    } catch {
      // ignore
    }
  }, []);

  return (
    <I18nProvider locale={settings.uiLanguage}>
      <ToastProvider>
        <TooltipProvider delayDuration={300}>
          {children({ settings, appLock })}
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
