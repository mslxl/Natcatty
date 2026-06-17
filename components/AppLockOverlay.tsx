import React from 'react';

import type { AppLockReason, AppLockUnlockResult } from '../application/state/useAppLockState';

interface AppLockOverlayProps {
  locked: boolean;
  reason: AppLockReason | null;
  onUnlock: (password: string) => Promise<AppLockUnlockResult>;
}

export const AppLockOverlay: React.FC<AppLockOverlayProps> = ({ locked }) => {
  if (!locked) return null;
  return (
    <div
      className="fixed inset-0 z-[200000] flex items-center justify-center bg-background text-foreground"
      role="dialog"
      aria-modal="true"
    />
  );
};
