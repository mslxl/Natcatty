import type { Terminal as XTerm } from "@xterm/xterm";
import type { SerializeAddon } from "@xterm/addon-serialize";

import {
  serializeTerminalForHibernate,
  type TerminalHibernateSnapshot,
} from "../terminalHibernateRuntime.ts";

export type TerminalCloseCaptureSource = "connection-log" | "hibernate-serialize" | "none";

export type TerminalCloseCapturePayload = {
  data: string;
  source: TerminalCloseCaptureSource;
};

export function scheduleTerminalCloseDataCapture(callback: () => void): void {
  if (typeof requestIdleCallback === "function") {
    requestIdleCallback(() => {
      scheduleTerminalCloseDataCaptureMicrotask(callback);
    });
    return;
  }
  scheduleTerminalCloseDataCaptureMicrotask(callback);
}

function scheduleTerminalCloseDataCaptureMicrotask(callback: () => void): void {
  if (typeof queueMicrotask === "function") {
    queueMicrotask(callback);
    return;
  }
  setTimeout(callback, 0);
}

export function resolveConnectionLogCapturePayload(
  finalizeTerminalLogData: () => string,
): TerminalCloseCapturePayload | null {
  const data = finalizeTerminalLogData();
  if (!data) return null;
  return { data, source: "connection-log" };
}

export function resolveHibernateSnapshotCapturePayload(
  snapshot: TerminalHibernateSnapshot,
): TerminalCloseCapturePayload | null {
  const data = snapshot.snapshot
    || snapshot.contextSnapshot
    || [snapshot.scrollbackSnapshot, snapshot.viewportSnapshot].filter(Boolean).join("");
  if (!data) return null;
  return { data, source: "hibernate-serialize" };
}

export async function serializeTerminalCloseFallback(
  term: XTerm,
  serializeAddon: SerializeAddon,
  options: { preferWasm?: boolean } = {},
): Promise<TerminalCloseCapturePayload | null> {
  const snapshot = await serializeTerminalForHibernate(term, serializeAddon, options);
  return resolveHibernateSnapshotCapturePayload(snapshot);
}
