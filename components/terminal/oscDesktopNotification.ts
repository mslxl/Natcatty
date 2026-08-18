import {
  DEFAULT_OSC_NOTIFICATION_TITLE,
  OscNotificationLimiter,
  resolveOscNotificationPresentation,
  shouldShowOscDesktopNotification,
  type OscNotification,
} from "../../domain/terminalOscNotifications";
import type { OscNotificationMode } from "../../domain/models/terminal";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";

const sessionLimiters = new Map<string, OscNotificationLimiter>();

const limiterForSession = (sessionId: string): OscNotificationLimiter => {
  const existing = sessionLimiters.get(sessionId);
  if (existing) return existing;
  const limiter = new OscNotificationLimiter();
  sessionLimiters.set(sessionId, limiter);
  return limiter;
};

export function showOscDesktopNotification(options: {
  notification: OscNotification;
  mode: OscNotificationMode | undefined;
  sessionFocused: boolean;
  sessionId: string;
  fallbackTitle?: string;
}): void {
  if (!shouldShowOscDesktopNotification(options.mode, {
    windowFocused: typeof document !== "undefined" && document.hasFocus(),
    sessionFocused: options.sessionFocused,
  })) {
    return;
  }
  if (!limiterForSession(options.sessionId).allow(options.sessionId)) return;

  const presented = resolveOscNotificationPresentation(
    options.notification,
    options.fallbackTitle || DEFAULT_OSC_NOTIFICATION_TITLE,
  );
  void netcattyBridge.get()?.showSystemNotification?.({
    title: presented.title,
    body: presented.body,
    sessionId: options.sessionId,
  });
}
