import { useSyncExternalStore } from 'react';

type Listener = () => void;

let suppressDepth = 0;
const listeners = new Set<Listener>();

function emit() {
  listeners.forEach((listener) => listener());
}

export const terminalLayoutSuppressStore = {
  getActive: () => suppressDepth > 0,

  subscribe: (listener: Listener) => {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },

  begin: () => {
    suppressDepth += 1;
    emit();
  },

  end: () => {
    const wasActive = suppressDepth > 0;
    suppressDepth = Math.max(0, suppressDepth - 1);
    if (wasActive) {
      emit();
    }
  },
};

export function useTerminalLayoutSuppressActive(): boolean {
  return useSyncExternalStore(
    terminalLayoutSuppressStore.subscribe,
    terminalLayoutSuppressStore.getActive,
    terminalLayoutSuppressStore.getActive,
  );
}
