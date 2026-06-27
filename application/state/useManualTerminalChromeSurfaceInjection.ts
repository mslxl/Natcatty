import { useLayoutEffect } from 'react';

import type { TerminalTheme } from '../../domain/models';
import { applyTopTabsChromeThemeVars } from '../app/topTabsChromeTheme';
import { injectTerminalLayerChromeSurfaceVars } from '../../infrastructure/theme/terminalAppearanceVars';

/** Manual mode: side panel + host tree follow the focused session theme, not the global default. */
export function useManualTerminalChromeSurfaceInjection(
  theme: TerminalTheme,
  enabled: boolean,
): void {
  useLayoutEffect(() => {
    if (!enabled) return;
    injectTerminalLayerChromeSurfaceVars(theme);
    applyTopTabsChromeThemeVars(theme);
  }, [enabled, theme.id, theme]);
}
