export interface HostedPluginViewState {
  id: string;
  viewId: string;
  scopeId: string;
  retainContextWhenHidden: boolean;
  tabId?: string;
}

export function reconcileClosedPluginView<T extends HostedPluginViewState>({
  current,
  retained,
  instanceId,
}: {
  current: T | null;
  retained: ReadonlyMap<string, T>;
  instanceId: string;
}): {
  current: T | null;
  retained: Map<string, T>;
  matchedCurrent: boolean;
  matchedRetained: boolean;
  closedTabId?: string;
} {
  const matchedCurrent = current?.id === instanceId;
  let matchedRetained = false;
  const nextRetained = new Map<string, T>();
  for (const [key, view] of retained) {
    if (view.id === instanceId) matchedRetained = true;
    else nextRetained.set(key, view);
  }
  return {
    current: matchedCurrent ? null : current,
    retained: nextRetained,
    matchedCurrent,
    matchedRetained,
    ...(matchedCurrent && current?.tabId ? { closedTabId: current.tabId } : {}),
  };
}

export function rememberClosedPluginViewInstance(
  tombstones: Set<string>,
  instanceId: string,
  limit = 256,
): void {
  if (tombstones.size >= limit) {
    const oldest = tombstones.values().next().value;
    if (typeof oldest === 'string') tombstones.delete(oldest);
  }
  tombstones.add(instanceId);
}

export function consumeClosedPluginViewInstance(tombstones: Set<string>, instanceId: string): boolean {
  return tombstones.delete(instanceId);
}
