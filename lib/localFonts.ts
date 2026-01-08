import { TerminalFont, withCjkFallback } from "../infrastructure/config/fonts"

/**
 * Type definition for Local Font Access API
 * @see https://developer.mozilla.org/en-US/docs/Web/API/Local_Font_Access_API
 */
interface LocalFontData {
    family: string;
}

/**
 * Checks if a font family name indicates a monospace font.
 * Uses word boundary matching to avoid false positives like 'Monaco' or 'Lemonada'.
 */
function isMonospaceFont(familyName: string): boolean {
    const familyLower = familyName.toLowerCase();
    const monoIndicators = ['mono', 'monospace', 'code', 'terminal'];

    return monoIndicators.some(indicator => {
        return (
            familyLower === indicator ||
            familyLower.endsWith(' ' + indicator) ||
            familyLower.endsWith('-' + indicator)
        );
    });
}

/**
 * Queries local monospace fonts from the system using the Font Access API.
 * Returns an empty array if the API is not available or permission is denied.
 */
export async function getMonospaceFonts(): Promise<TerminalFont[]> {
    // Check if the Font Access API is available
    if (typeof window === "undefined" || !("queryLocalFonts" in window)) {
        return [];
    }

    try {
        const queryLocalFonts = (window as unknown as { queryLocalFonts: () => Promise<LocalFontData[]> }).queryLocalFonts;
        const fonts = await queryLocalFonts();

        // Filter monospace fonts using robust word boundary matching
        const monoFonts = fonts.filter(f => isMonospaceFont(f.family));

        // Deduplicate by family name (API may return multiple entries per family)
        const uniqueFamilies = new Set<string>();
        const dedupedFonts = monoFonts.filter(f => {
            if (uniqueFamilies.has(f.family)) return false;
            uniqueFamilies.add(f.family);
            return true;
        });

        // Map to TerminalFont structure with CJK fallback applied
        return dedupedFonts.map(f => ({
            id: f.family,
            name: f.family,
            family: withCjkFallback(f.family + ', monospace'),
            description: `Local font: ${f.family}`,
            category: 'monospace' as const,
        }));
    } catch (error) {
        // Handle permission denied or other errors gracefully
        console.warn('Failed to query local fonts:', error);
        return [];
    }
}