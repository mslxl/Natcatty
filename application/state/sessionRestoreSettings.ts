export const DEFAULT_RESTORE_PREVIOUS_SESSION = true;

export const resolveRestorePreviousSessionSetting = (stored: boolean | null): boolean =>
  stored ?? DEFAULT_RESTORE_PREVIOUS_SESSION;
