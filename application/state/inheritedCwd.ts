import type { TerminalSession } from "../../domain/models";

export type SessionPwdProbe = (
  sessionId: string,
  options?: { allowHomeFallback?: boolean },
) => Promise<{ success: boolean; cwd?: string }>;

type CaptureSession = Pick<TerminalSession, "id" | "protocol" | "status" | "lastCwd" | "localStartDir">;

/**
 * Resolve the working directory a clone/split should inherit from its source.
 * Priority: tracked lastCwd -> live SSH /proc probe -> local startDir. Returns
 * undefined when nothing is known (caller then behaves as before: login dir).
 */
export async function captureInheritedCwd(
  session: CaptureSession,
  getSessionPwd: SessionPwdProbe,
): Promise<string | undefined> {
  const tracked = session.lastCwd?.trim();
  if (tracked) return tracked;

  const protocol = session.protocol ?? "ssh";
  const isRemoteSsh = protocol === "ssh" || protocol === undefined;
  if (isRemoteSsh && session.status === "connected") {
    try {
      const res = await getSessionPwd(session.id, { allowHomeFallback: false });
      const probed = res?.success ? res.cwd?.trim() : undefined;
      if (probed) return probed;
    } catch {
      /* probe failed — fall through to undefined */
    }
  }

  if (protocol === "local") return session.localStartDir;
  return undefined;
}
