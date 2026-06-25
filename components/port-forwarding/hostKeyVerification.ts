import type { KnownHost } from "../../domain/models";
import type { HostKeyInfo } from "../terminal/TerminalHostKeyVerification";

export const isPortForwardHostKeySessionId = (sessionId?: string): boolean => {
  return typeof sessionId === "string" && sessionId.startsWith("pf-");
};

export const createKnownHostFromPortForwardHostKeyInfo = (
  hostKeyInfo: HostKeyInfo,
  now = Date.now(),
  idSuffix = Math.random().toString(36).slice(2, 11),
): KnownHost => ({
  id: hostKeyInfo.knownHostId || `kh-${now}-${idSuffix}`,
  hostname: hostKeyInfo.hostname,
  port: hostKeyInfo.port || 22,
  keyType: hostKeyInfo.keyType,
  publicKey: hostKeyInfo.publicKey || `SHA256:${hostKeyInfo.fingerprint}`,
  fingerprint: hostKeyInfo.fingerprint,
  discoveredAt: now,
});
