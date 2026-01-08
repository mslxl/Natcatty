import { useEffect, useState } from 'react';
import { TERMINAL_FONTS, type TerminalFont } from '../../infrastructure/config/fonts';
import { getMonospaceFonts } from '../../lib/localFonts';

/**
 * Global font state hook that manages available terminal fonts.
 * Combines default fonts with local monospace fonts from the system.
 */
export const useFontState = () => {
  const [availableFonts, setAvailableFonts] = useState<TerminalFont[]>(TERMINAL_FONTS);
  const [fontsLoading, setFontsLoading] = useState(false);

  useEffect(() => {
    const fetchFonts = async () => {
      setFontsLoading(true);
      try {
        const localFonts = await getMonospaceFonts();
        // Combine default fonts with local fonts, deduplicate by id
        const fontMap = new Map<string, TerminalFont>();

        // Add default fonts first
        TERMINAL_FONTS.forEach(font => fontMap.set(font.id, font));

        // Add local fonts with a distinct ID namespace to avoid collisions
        localFonts.forEach(font => {
          const localId = font.id.startsWith('local-') ? font.id : `local-${font.id}`;
          fontMap.set(localId, { ...font, id: localId });
        });

        setAvailableFonts(Array.from(fontMap.values()));
      } catch (error) {
        // If local fonts API is not available or permission denied, fall back to default fonts
        console.warn('Failed to fetch local fonts, using defaults:', error);
        setAvailableFonts(TERMINAL_FONTS);
      } finally {
        setFontsLoading(false);
      }
    };

    fetchFonts();
  }, []);

  return {
    availableFonts,
    fontsLoading,
  };
};
