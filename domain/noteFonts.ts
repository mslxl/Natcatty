export interface FontOption {
  label: string;
  value: string;
  category: "推荐常用" | "现代无衬线" | "优雅衬线" | "代码等宽";
}

export const COMPREHENSIVE_NOTE_FONTS: FontOption[] = [
  // 推荐常用
  { label: "默认系统字体 (System UI)", value: "", category: "推荐常用" },
  { label: "霞鹜文楷 (LXGW WenKai)", value: '"LXGW WenKai", "霞鹜文楷", cursive, sans-serif', category: "推荐常用" },
  { label: "苹方 (PingFang SC)", value: '"PingFang SC", "Hiragino Sans GB", sans-serif', category: "推荐常用" },
  { label: "微软雅黑 (Microsoft YaHei)", value: '"Microsoft YaHei", "微软雅黑", sans-serif', category: "推荐常用" },
  { label: "鸿蒙字体 (HarmonyOS Sans)", value: '"HarmonyOS Sans SC", "HarmonyOS Sans", sans-serif', category: "推荐常用" },
  { label: "思源黑体 (Noto Sans SC)", value: '"Noto Sans SC", "Source Han Sans SC", sans-serif', category: "推荐常用" },
  { label: "思源宋体 (Noto Serif SC)", value: '"Noto Serif SC", "Source Han Serif SC", serif', category: "推荐常用" },

  // 现代无衬线
  { label: "Inter", value: '"Inter", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Roboto", value: '"Roboto", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Mona Sans", value: '"Mona Sans", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Space Grotesk", value: '"Space Grotesk", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Open Sans", value: '"Open Sans", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Segoe UI", value: '"Segoe UI", system-ui, sans-serif', category: "现代无衬线" },
  { label: "Helvetica Neue", value: '"Helvetica Neue", Helvetica, Arial, sans-serif', category: "现代无衬线" },

  // 优雅衬线
  { label: "宋体 (SimSun / Songti)", value: 'SimSun, "Songti SC", STSong, serif', category: "优雅衬线" },
  { label: "仿宋 (FangSong)", value: 'FangSong, "FangSong SC", STFangsong, serif', category: "优雅衬线" },
  { label: "楷体 (KaiTi)", value: 'KaiTi, "Kaiti SC", STKaiti, serif', category: "优雅衬线" },
  { label: "Georgia", value: 'Georgia, serif', category: "优雅衬线" },
  { label: "Merriweather", value: 'Merriweather, Georgia, serif', category: "优雅衬线" },
  { label: "Garamond", value: 'Garamond, "EB Garamond", serif', category: "优雅衬线" },
  { label: "Times New Roman", value: '"Times New Roman", Times, serif', category: "优雅衬线" },

  // 代码等宽
  { label: "JetBrains Mono", value: '"JetBrains Mono", monospace', category: "代码等宽" },
  { label: "Fira Code", value: '"Fira Code", monospace', category: "代码等宽" },
  { label: "Cascadia Code", value: '"Cascadia Code", monospace', category: "代码等宽" },
  { label: "Source Code Pro", value: '"Source Code Pro", monospace', category: "代码等宽" },
  { label: "Hack", value: '"Hack", monospace', category: "代码等宽" },
  { label: "Monaco", value: 'Monaco, monospace', category: "代码等宽" },
  { label: "Consolas", value: 'Consolas, monospace', category: "代码等宽" },
  { label: "SF Mono", value: '"SF Mono", monospace', category: "代码等宽" },
  { label: "Courier New", value: '"Courier New", monospace', category: "代码等宽" },
];

export const NOTE_FONT_CATEGORIES = ["全部", "推荐常用", "现代无衬线", "优雅衬线", "代码等宽"] as const;
export type NoteFontCategory = (typeof NOTE_FONT_CATEGORIES)[number];
