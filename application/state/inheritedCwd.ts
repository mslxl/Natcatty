import type { TerminalSession } from "../../domain/models";

export type SessionPwdProbe = (
  sessionId: string,
  options?: { allowHomeFallback?: boolean },
) => Promise<{ success: boolean; cwd?: string }>;

type CaptureSession = Pick<TerminalSession, "id" | "protocol" | "status" | "lastCwd" | "localStartDir">;

/** Max time to wait on the live SSH cwd probe before falling back to lastCwd. */
export const DEFAULT_INHERITED_CWD_PROBE_TIMEOUT_MS = 1500;

/**
 * Resolve the working directory a clone/split should inherit from its source.
 *
 * For a live SSH session the freshest cwd is the backend's `/proc` value, so we
 * probe it first: `session.lastCwd` is only a startup snapshot for restored
 * tabs (live cwd updates are tracked off-session and never mutate `lastCwd`),
 * so preferring it would inherit a stale directory. The probe is raced against
 * a short timeout so a slow/wedged connection can't block tab/pane creation —
 * on timeout (or failure) we fall back to the tracked snapshot. Priority: live
 * SSH probe -> tracked `lastCwd` -> local `localStartDir`. Returns undefined
 * when nothing is known (caller then behaves as before: login dir).
 */
export async function captureInheritedCwd(
  session: CaptureSession,
  getSessionPwd: SessionPwdProbe,
  probeTimeoutMs: number = DEFAULT_INHERITED_CWD_PROBE_TIMEOUT_MS,
): Promise<string | undefined> {
  const protocol = session.protocol ?? "ssh";
  const isRemoteSsh = protocol === "ssh" || protocol === undefined;

  if (isRemoteSsh && session.status === "connected") {
    // Never rejects: a failed/absent probe resolves to undefined so the race
    // below can't leave a dangling unhandled rejection when the timeout wins.
    const probePromise = getSessionPwd(session.id, { allowHomeFallback: false })
      .then((res) => (res?.success ? res.cwd?.trim() : undefined))
      .catch(() => undefined);

    let timer: ReturnType<typeof setTimeout> | undefined;
    const timeoutPromise = new Promise<undefined>((resolve) => {
      timer = setTimeout(() => resolve(undefined), probeTimeoutMs);
    });

    const probed = await Promise.race([probePromise, timeoutPromise]);
    if (timer) clearTimeout(timer);
    if (probed) return probed;
  }

  const tracked = session.lastCwd?.trim();
  if (tracked) return tracked;

  if (protocol === "local") return session.localStartDir;
  return undefined;
}
