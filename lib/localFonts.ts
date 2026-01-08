import { TerminalFont } from "../infrastructure/config/fonts"

export async function getMonospaceFonts(): Promise<TerminalFont[]> {
    const fonts = await window.queryLocalFonts();
    // Simple filter; optionally combine with Canvas width measurement for precise detection
    const mono_fonts = fonts.filter(f => f.family.toLowerCase().includes('mono'));
    // Map to TerminalFont structure
    return mono_fonts.map(f => ({
        id: f.family,
        name: f.family,
        family: f.family + ', monospace',
        description: `Local font: ${f.family}`,
        category: 'monospace' as const,
    }));
}