/**
 * UI Fonts Configuration
 * Includes general-purpose fonts suitable for application interface
 */

export interface UIFont {
  id: string;
  name: string;
  family: string;
  description: string;
}

/**
 * Fallback fonts for CJK (Chinese, Japanese, Korean) support
 */
const CJK_FALLBACK_FONTS = [
  '"PingFang SC"',
  '"Hiragino Sans GB"',
  '"Microsoft YaHei UI"',
  '"Microsoft YaHei"',
  '"Noto Sans CJK SC"',
  '"Source Han Sans SC"',
  'sans-serif',
];

const CJK_FALLBACK_STACK = CJK_FALLBACK_FONTS.join(', ');

export const withUiCjkFallback = (family: string) => {
  const trimmed = family.trim();
  if (!CJK_FALLBACK_STACK) return trimmed;
  // Avoid double-appending if a custom stack already includes one of these fonts.
  if (CJK_FALLBACK_FONTS.some((f) => trimmed.includes(f.replace(/"/g, '')))) {
    return trimmed;
  }
  return `${trimmed}, ${CJK_FALLBACK_STACK}`;
};

const BASE_UI_FONTS: UIFont[] = [
  {
    id: 'space-grotesk',
    name: 'Space Grotesk',
    family: '"Space Grotesk", system-ui',
    description: 'Default Netcatty font with geometric style',
  },
  {
    id: 'system-ui',
    name: 'System UI',
    family: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial',
    description: 'Native system font for best platform integration',
  },
  {
    id: 'inter',
    name: 'Inter',
    family: '"Inter", system-ui',
    description: 'Modern variable font designed for screens',
  },
  {
    id: 'roboto',
    name: 'Roboto',
    family: '"Roboto", system-ui',
    description: 'Google\'s versatile sans-serif font',
  },
  {
    id: 'open-sans',
    name: 'Open Sans',
    family: '"Open Sans", system-ui',
    description: 'Clean and readable sans-serif font',
  },
  {
    id: 'lato',
    name: 'Lato',
    family: '"Lato", system-ui',
    description: 'Warm and friendly sans-serif font',
  },
  {
    id: 'nunito',
    name: 'Nunito',
    family: '"Nunito", system-ui',
    description: 'Rounded sans-serif with soft appearance',
  },
  {
    id: 'poppins',
    name: 'Poppins',
    family: '"Poppins", system-ui',
    description: 'Geometric sans-serif with modern feel',
  },
  {
    id: 'source-sans-pro',
    name: 'Source Sans Pro',
    family: '"Source Sans Pro", system-ui',
    description: 'Adobe\'s first open-source font family',
  },
  {
    id: 'ubuntu',
    name: 'Ubuntu',
    family: '"Ubuntu", system-ui',
    description: 'Ubuntu\'s official interface font',
  },
  {
    id: 'noto-sans',
    name: 'Noto Sans',
    family: '"Noto Sans", system-ui',
    description: 'Google\'s font with wide language support',
  },
  {
    id: 'work-sans',
    name: 'Work Sans',
    family: '"Work Sans", system-ui',
    description: 'Optimized for on-screen text',
  },
  {
    id: 'dm-sans',
    name: 'DM Sans',
    family: '"DM Sans", system-ui',
    description: 'Low-contrast geometric sans serif',
  },
  {
    id: 'montserrat',
    name: 'Montserrat',
    family: '"Montserrat", system-ui',
    description: 'Urban sans-serif inspired by Buenos Aires',
  },
  {
    id: 'raleway',
    name: 'Raleway',
    family: '"Raleway", system-ui',
    description: 'Elegant sans-serif with thin weight options',
  },
  {
    id: 'quicksand',
    name: 'Quicksand',
    family: '"Quicksand", system-ui',
    description: 'Rounded geometric sans-serif',
  },
  {
    id: 'ibm-plex-sans',
    name: 'IBM Plex Sans',
    family: '"IBM Plex Sans", system-ui',
    description: 'IBM\'s modern corporate typeface',
  },
  {
    id: 'outfit',
    name: 'Outfit',
    family: '"Outfit", system-ui',
    description: 'Geometric sans-serif with friendly appearance',
  },
  {
    id: 'plus-jakarta-sans',
    name: 'Plus Jakarta Sans',
    family: '"Plus Jakarta Sans", system-ui',
    description: 'Fresh and modern variable font',
  },
  {
    id: 'segoe-ui',
    name: 'Segoe UI',
    family: '"Segoe UI", system-ui',
    description: 'Microsoft Windows interface font',
  },
  {
    id: 'sf-pro',
    name: 'SF Pro',
    family: '-apple-system, BlinkMacSystemFont, "SF Pro Display", system-ui',
    description: 'Apple\'s San Francisco system font',
  },
];

export const UI_FONTS: UIFont[] = BASE_UI_FONTS.map((font) => ({
  ...font,
  family: withUiCjkFallback(font.family),
}));

export const DEFAULT_UI_FONT_ID = 'space-grotesk';

export const getUiFontById = (id: string): UIFont => {
  return UI_FONTS.find((f) => f.id === id) || UI_FONTS[0];
};
