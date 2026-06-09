import { TERMINAL_HOST_TREE_ANIMATION_MS } from './terminalHostTreeAnimation';

export const THEME_TRANSITION_ATTR = 'data-theme-transition';
export const THEME_TRANSITION_MS = TERMINAL_HOST_TREE_ANIMATION_MS;

type DocumentWithViewTransition = Document & {
  startViewTransition?: (callback: () => void | Promise<void>) => {
    finished: Promise<void>;
    skipTransition: () => void;
  };
};

let cancelThemeTransitionReset: (() => void) | null = null;

export function runThemeTransition(
  apply: () => void,
  root: HTMLElement = document.documentElement,
): void {
  cancelThemeTransitionReset?.();

  const cleanup = () => {
    root.removeAttribute(THEME_TRANSITION_ATTR);
    cancelThemeTransitionReset = null;
  };

  const doc = root.ownerDocument as DocumentWithViewTransition | null;
  const startViewTransition = doc?.startViewTransition?.bind(doc);

  if (startViewTransition) {
    let transition: ReturnType<NonNullable<DocumentWithViewTransition['startViewTransition']>> | null = null;
    try {
      transition = startViewTransition(() => {
        apply();
      });
    } catch {
      root.setAttribute(THEME_TRANSITION_ATTR, 'true');
      apply();
      const timer = globalThis.setTimeout(cleanup, THEME_TRANSITION_MS + 40);
      cancelThemeTransitionReset = () => {
        globalThis.clearTimeout(timer);
        cleanup();
      };
      return;
    }

    cancelThemeTransitionReset = () => {
      transition?.skipTransition();
      cleanup();
    };
    void transition.finished.finally(cleanup);
    return;
  }

  root.setAttribute(THEME_TRANSITION_ATTR, 'true');
  apply();
  const timer = globalThis.setTimeout(cleanup, THEME_TRANSITION_MS + 40);
  cancelThemeTransitionReset = () => {
    globalThis.clearTimeout(timer);
    cleanup();
  };
}
