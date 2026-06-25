import { useCallback, useEffect, useState } from "react";
import type { KnownHost } from "../../domain/models";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";
import type { HostKeyInfo } from "../../components/terminal/TerminalHostKeyVerification";
import { toHostKeyInfo, type HostKeyVerificationRequest } from "../../components/terminal/hostKeyVerification";
import {
  createKnownHostFromPortForwardHostKeyInfo,
  isPortForwardHostKeySessionId,
} from "../../components/port-forwarding/hostKeyVerification";

type PortForwardHostKeyRequest = HostKeyVerificationRequest & {
  requestId: string;
  sessionId?: string;
};

interface PendingHostKeyVerification {
  requestId: string;
  hostKeyInfo: HostKeyInfo;
}

export interface PortForwardHostKeyVerificationState {
  hostKeyInfo: HostKeyInfo;
}

export const usePortForwardHostKeyVerification = (
  onAddKnownHost?: (knownHost: KnownHost) => void,
) => {
  const [pending, setPending] = useState<PendingHostKeyVerification | null>(null);

  useEffect(() => {
    const dispose = netcattyBridge.get()?.onHostKeyVerification?.((request: PortForwardHostKeyRequest) => {
      if (!isPortForwardHostKeySessionId(request.sessionId)) return;
      setPending({
        requestId: request.requestId,
        hostKeyInfo: toHostKeyInfo(request),
      });
    });

    return () => {
      dispose?.();
    };
  }, []);

  const respond = useCallback((accept: boolean, addToKnownHosts = false) => {
    if (!pending) return;
    if (accept && addToKnownHosts) {
      onAddKnownHost?.(createKnownHostFromPortForwardHostKeyInfo(pending.hostKeyInfo));
    }
    void netcattyBridge.get()?.respondHostKeyVerification?.(
      pending.requestId,
      accept,
      addToKnownHosts,
    );
    setPending(null);
  }, [onAddKnownHost, pending]);

  return {
    hostKeyVerification: pending ? { hostKeyInfo: pending.hostKeyInfo } : null,
    rejectHostKeyVerification: () => respond(false),
    acceptHostKeyVerification: () => respond(true, false),
    acceptAndSaveHostKeyVerification: () => respond(true, true),
  };
};
