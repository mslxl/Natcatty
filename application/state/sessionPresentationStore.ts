import type { CodingCliProviderId } from '../../domain/codingCliProviders';

export type SessionPresentation = {
  dynamicTitle?: string | null;
  codingCliProviderId?: CodingCliProviderId | null;
};

type Listener = () => void;

/**
 * Presentation-only session chrome (tab title / coding-CLI icon) separate from
 * structural session identity used by TerminalLayer pane equality.
 */
class SessionPresentationStore {
  private bySession = new Map<string, SessionPresentation>();
  private version = 0;
  private listeners = new Set<Listener>();

  getVersion = (): number => this.version;

  getPresentation = (sessionId: string): SessionPresentation | undefined =>
    this.bySession.get(sessionId);

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  setPresentation(sessionId: string, patch: SessionPresentation): void {
    const prev = this.bySession.get(sessionId) ?? {};
    const next: SessionPresentation = { ...prev, ...patch };
    if (
      (prev.dynamicTitle ?? null) === (next.dynamicTitle ?? null)
      && (prev.codingCliProviderId ?? null) === (next.codingCliProviderId ?? null)
    ) {
      return;
    }
    this.bySession.set(sessionId, next);
    this.version += 1;
    for (const listener of this.listeners) listener();
  }

  clearSession(sessionId: string): void {
    if (!this.bySession.has(sessionId)) return;
    this.bySession.delete(sessionId);
    this.version += 1;
    for (const listener of this.listeners) listener();
  }

  prune(validSessionIds: ReadonlySet<string>): void {
    let changed = false;
    for (const id of this.bySession.keys()) {
      if (!validSessionIds.has(id)) {
        this.bySession.delete(id);
        changed = true;
      }
    }
    if (!changed) return;
    this.version += 1;
    for (const listener of this.listeners) listener();
  }
}

export const sessionPresentationStore = new SessionPresentationStore();

export function publishSessionDynamicTitle(sessionId: string, title: string | null): void {
  sessionPresentationStore.setPresentation(sessionId, { dynamicTitle: title });
}

export function publishSessionCodingCliProvider(
  sessionId: string,
  providerId: CodingCliProviderId | null,
): void {
  sessionPresentationStore.setPresentation(sessionId, { codingCliProviderId: providerId });
}

type SessionWithPresentation = {
  id: string;
  dynamicTitle?: string;
  codingCliProviderId?: CodingCliProviderId;
};

/**
 * Overlay live presentation chrome onto a session snapshot.
 * Used by TopTabs for both main sessions and orphanSessionMap so title/provider
 * updates never freeze for either tab surface.
 */
export function applySessionPresentation<T extends SessionWithPresentation>(session: T): T {
  const presentation = sessionPresentationStore.getPresentation(session.id);
  if (!presentation) return session;
  const nextTitle = presentation.dynamicTitle === undefined
    ? session.dynamicTitle
    : (presentation.dynamicTitle ?? undefined);
  const nextProvider = presentation.codingCliProviderId === undefined
    ? session.codingCliProviderId
    : (presentation.codingCliProviderId ?? undefined);
  if (
    nextTitle === session.dynamicTitle
    && nextProvider === session.codingCliProviderId
  ) {
    return session;
  }
  return {
    ...session,
    dynamicTitle: nextTitle,
    codingCliProviderId: nextProvider,
  };
}
