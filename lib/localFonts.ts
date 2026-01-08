import { TerminalFont } from "../infrastructure/config/fonts"

export async function getMonospaceFonts(): Promise<TerminalFont[]> {
    const fonts = await window.queryLocalFonts();
    // 简单筛选，或结合 Canvas 测量宽度精确判断
    const mono_fonts = fonts.filter(f => f.family.toLowerCase().includes('mono'));
    // 映射为 TerminalFont 结构
    return mono_fonts.map(f => ({
        id: f.family,
        name: f.family,
        family: f.family + ', monospace',
        description: `Local font: ${f.family}`,
        category: 'monospace' as const,
    }));
}