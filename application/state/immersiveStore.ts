import { useSyncExternalStore } from 'react';

/**
 * Tiny external store for "immersive mode active" (whether the active terminal
 * tab's theme is driving the app chrome). Kept out of the App component's render
 * so that toggling immersive — and tab switches in general — do not force a
 * full App re-render. The owner (AppActiveTabChrome) calls setImmersiveActive;
 * AppView/TopTabs read it via useImmersiveActive without re-rendering App.
 */
type Listener = () => void;

let immersiveActive = false;
const listeners = new Set<Listener>();

export function setImmersiveActive(active: boolean): void {
  if (immersiveActive === active) return;
  immersiveActive = active;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(): boolean {
  return immersiveActive;
}

export function useImmersiveActive(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
