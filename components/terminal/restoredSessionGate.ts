import type { TerminalSession } from "../../domain/models";

type RestoredGateSession = Pick<TerminalSession, "status"> & {
  restoreState?: string;
};

export const isRestoredDisconnectedTerminal = (session: RestoredGateSession): boolean =>
  session.status === "disconnected" && session.restoreState === "restored-disconnected";

export const getInitialTerminalStatus = (
  session: RestoredGateSession,
): TerminalSession["status"] => (
  isRestoredDisconnectedTerminal(session) ? "disconnected" : "connecting"
);

export const shouldStartTerminalBackend = (session: RestoredGateSession): boolean =>
  !isRestoredDisconnectedTerminal(session);
