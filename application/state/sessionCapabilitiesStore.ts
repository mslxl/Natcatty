import type { SessionCapabilities } from '../../domain/systemManager/types';

type Listener = () => void;

const capabilitiesBySessionId = new Map<string, SessionCapabilities>();
const listenersBySessionId = new Map<string, Set<Listener>>();

function notifySession(sessionId: string) {
  listenersBySessionId.get(sessionId)?.forEach((listener) => listener());
}

export const sessionCapabilitiesStore = {
  get(sessionId: string): SessionCapabilities | undefined {
    return capabilitiesBySessionId.get(sessionId);
  },

  set(sessionId: string, capabilities: SessionCapabilities) {
    const prev = capabilitiesBySessionId.get(sessionId);
    if (
      prev
      && prev.targetOs === capabilities.targetOs
      && prev.hasTmux === capabilities.hasTmux
      && prev.hasDocker === capabilities.hasDocker
      && prev.probedAt === capabilities.probedAt
    ) {
      return;
    }
    capabilitiesBySessionId.set(sessionId, capabilities);
    notifySession(sessionId);
  },

  delete(sessionId: string) {
    if (!capabilitiesBySessionId.delete(sessionId)) return;
    notifySession(sessionId);
    listenersBySessionId.delete(sessionId);
  },

  /** Drop cached capabilities for sessions that no longer exist. */
  prune(liveSessionIds: ReadonlySet<string>) {
    for (const sessionId of capabilitiesBySessionId.keys()) {
      if (!liveSessionIds.has(sessionId)) {
        capabilitiesBySessionId.delete(sessionId);
        listenersBySessionId.delete(sessionId);
      }
    }
  },

  subscribe(sessionId: string, listener: Listener): () => void {
    let set = listenersBySessionId.get(sessionId);
    if (!set) {
      set = new Set();
      listenersBySessionId.set(sessionId, set);
    }
    set.add(listener);
    return () => {
      set?.delete(listener);
      if (set && set.size === 0) {
        listenersBySessionId.delete(sessionId);
      }
    };
  },
};
