import type { Host, TerminalSession } from "./models";

export type SftpConnectedHostEntry = {
  host: Host;
  sessionId: string;
  status: Extract<TerminalSession["status"], "connecting" | "connected">;
};

const isSftpEligibleSession = (session: TerminalSession): boolean => {
  if (session.status !== "connected" && session.status !== "connecting") return false;
  const protocol = session.protocol;
  if (protocol === "serial" || protocol === "local" || protocol === "telnet") return false;
  // Missing protocol defaults to SSH (same as host picker filtering).
  return true;
};

/**
 * Build the "currently connected" host list for the SFTP host picker.
 * One entry per hostId — prefers a connected session over connecting,
 * then the most recently listed session for that host.
 */
export const listSftpConnectedHosts = (
  sessions: ReadonlyArray<TerminalSession>,
  hostsById: ReadonlyMap<string, Host>,
): SftpConnectedHostEntry[] => {
  const bestByHostId = new Map<string, SftpConnectedHostEntry>();

  for (const session of sessions) {
    if (!isSftpEligibleSession(session)) continue;
    const host = hostsById.get(session.hostId);
    if (!host) continue;
    if (host.protocol === "serial") continue;

    const next: SftpConnectedHostEntry = {
      host,
      sessionId: session.id,
      status: session.status === "connecting" ? "connecting" : "connected",
    };
    const existing = bestByHostId.get(host.id);
    if (!existing) {
      bestByHostId.set(host.id, next);
      continue;
    }
    // Prefer connected over connecting; otherwise keep the first seen.
    if (existing.status === "connecting" && next.status === "connected") {
      bestByHostId.set(host.id, next);
    }
  }

  return [...bestByHostId.values()].sort((a, b) =>
    a.host.label.localeCompare(b.host.label),
  );
};
