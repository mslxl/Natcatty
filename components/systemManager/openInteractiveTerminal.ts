import { canReuseTerminalConnection } from '../../application/state/terminalConnectionReuse';
import type { TerminalSession } from '../../types';
import type { useSystemManagerBackend } from '../../application/state/useSystemManagerBackend';

type Backend = ReturnType<typeof useSystemManagerBackend>;

export async function openInteractiveTerminal(
  backend: Backend,
  parentSession: TerminalSession,
  title: string,
  startupCommand: string,
): Promise<{ success: boolean; error?: string }> {
  const result = await backend.openTerminalPopup({
    title,
    parentSessionId: parentSession.id,
    startupCommand,
    sourceSession: {
      ...parentSession,
      startupCommand,
      reuseConnectionFromSessionId: canReuseTerminalConnection(parentSession)
        ? parentSession.id
        : undefined,
    },
  });
  return result;
}
