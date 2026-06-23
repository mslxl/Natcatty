import { useEffect, useRef } from "react";

import {
  type TerminalHibernateWakePayload,
} from "../../domain/terminalHibernate";
import { logger } from "../../lib/logger";
import {
  getPaneVisible,
  subscribePaneVisible,
} from "./paneVisibilityStore";
import type { TerminalSession } from "../../types";

type UseTerminalHibernateEffectOptions = {
  sessionId: string;
  isVisibleRef: React.MutableRefObject<boolean>;
  status: TerminalSession["status"];
  isSearchOpen: boolean;
  hibernateEnabled: boolean;
  hibernateDelayMs: number;
  fileTransferActive: boolean;
  hibernatedRef: React.MutableRefObject<boolean>;
  hibernatePendingBufferRef: React.MutableRefObject<string>;
  hibernateSnapshotRef: React.MutableRefObject<string>;
  hibernateAlternateScreenRef: React.MutableRefObject<boolean>;
  hasRuntimeRef: React.MutableRefObject<boolean>;
  onHibernate: () => void;
  onWake: (payload: TerminalHibernateWakePayload) => void;
};

export function useTerminalHibernateEffect({
  sessionId,
  isVisibleRef,
  status,
  isSearchOpen,
  hibernateEnabled,
  hibernateDelayMs,
  fileTransferActive,
  hibernatedRef,
  hibernatePendingBufferRef,
  hibernateSnapshotRef,
  hibernateAlternateScreenRef,
  hasRuntimeRef,
  onHibernate,
  onWake,
}: UseTerminalHibernateEffectOptions): void {
  const hiddenSinceRef = useRef<number | null>(null);
  const hibernateTimerRef = useRef<number | null>(null);
  const paneVisibleRef = useRef(getPaneVisible(sessionId));
  const onHibernateRef = useRef(onHibernate);
  const onWakeRef = useRef(onWake);
  onHibernateRef.current = onHibernate;
  onWakeRef.current = onWake;

  useEffect(() => {
    const clearHibernateTimer = () => {
      if (hibernateTimerRef.current !== null) {
        window.clearTimeout(hibernateTimerRef.current);
        hibernateTimerRef.current = null;
      }
    };

    const tryWake = () => {
      if (!hibernatedRef.current) return;

      const payload: TerminalHibernateWakePayload = {
        snapshot: hibernateSnapshotRef.current,
        pendingBuffer: hibernatePendingBufferRef.current,
        alternateScreen: hibernateAlternateScreenRef.current,
      };
      hibernateSnapshotRef.current = "";
      hibernatePendingBufferRef.current = "";
      hibernateAlternateScreenRef.current = false;
      hibernatedRef.current = false;
      logger.info("[Terminal] Waking from hibernate", {
        sessionId,
        snapshotChars: payload.snapshot.length,
        pendingChars: payload.pendingBuffer.length,
      });
      onWakeRef.current(payload);
    };

    if (!hibernateEnabled) {
      clearHibernateTimer();
      if (hibernatedRef.current && getPaneVisible(sessionId)) {
        tryWake();
      }
      const unsubscribeDisabled = subscribePaneVisible(sessionId, () => {
        if (hibernatedRef.current && getPaneVisible(sessionId)) {
          tryWake();
        }
      });
      return () => {
        unsubscribeDisabled();
      };
    }

    const scheduleHibernate = () => {
      clearHibernateTimer();
      if (hibernatedRef.current || !hasRuntimeRef.current) return;
      if (status !== "connected") return;
      if (isSearchOpen) return;
      if (fileTransferActive) return;

      hiddenSinceRef.current = Date.now();
      const hiddenAt = hiddenSinceRef.current;
      hibernateTimerRef.current = window.setTimeout(() => {
        hibernateTimerRef.current = null;
        if (hiddenSinceRef.current !== hiddenAt) return;
        if (getPaneVisible(sessionId)) return;
        onHibernateRef.current();
      }, hibernateDelayMs);
    };

    const applyVisibility = (visible: boolean) => {
      paneVisibleRef.current = visible;
      isVisibleRef.current = visible;

      if (visible) {
        hiddenSinceRef.current = null;
        clearHibernateTimer();
        tryWake();
        return;
      }

      scheduleHibernate();
    };

    applyVisibility(getPaneVisible(sessionId));

    const unsubscribe = subscribePaneVisible(sessionId, () => {
      const visible = getPaneVisible(sessionId);
      if (visible === paneVisibleRef.current) return;
      applyVisibility(visible);
    });

    return () => {
      clearHibernateTimer();
      unsubscribe();
    };
  }, [
    fileTransferActive,
    hasRuntimeRef,
    hibernateDelayMs,
    hibernateEnabled,
    hibernatePendingBufferRef,
    hibernateSnapshotRef,
    hibernateAlternateScreenRef,
    hibernatedRef,
    isSearchOpen,
    isVisibleRef,
    sessionId,
    status,
  ]);
}
