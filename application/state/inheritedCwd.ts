import type { TerminalSession } from "../../domain/models";

export type SessionPwdProbe = (
  sessionId: string,
  options?: { allowHomeFallback?: boolean },
) => Promise<{ success: boolean; cwd?: string }>;

type CaptureSession = Pick<TerminalSession, "id" | "protocol" | "status" | "lastCwd" | "localStartDir">;

/**
 * Resolve the working directory a clone/split should inherit from its source.
 *
 * For a live SSH session the freshest cwd is the backend's `/proc` value, so we
 * probe it first: `session.lastCwd` is only a startup snapshot for restored
 * tabs (live cwd updates are tracked off-session and never mutate `lastCwd`),
 * so preferring it would inherit a stale directory. Priority: live SSH probe ->
 * tracked `lastCwd` -> local `localStartDir`. Returns undefined when nothing is
 * known (caller then behaves as before: login dir).
 */
export async function captureInheritedCwd(
  session: CaptureSession,
  getSessionPwd: SessionPwdProbe,
): Promise<string | undefined> {
  const protocol = session.protocol ?? "ssh";
  const isRemoteSsh = protocol === "ssh" || protocol === undefined;

  if (isRemoteSsh && session.status === "connected") {
    try {
      const res = await getSessionPwd(session.id, { allowHomeFallback: false });
      const probed = res?.success ? res.cwd?.trim() : undefined;
      if (probed) return probed;
    } catch {
      /* probe failed — fall through to the tracked snapshot */
    }
  }

  const tracked = session.lastCwd?.trim();
  if (tracked) return tracked;

  if (protocol === "local") return session.localStartDir;
  return undefined;
}
