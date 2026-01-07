/**
 * SFTP File Utilities
 * Helper functions for file type detection and extension handling
 */

// Common text file extensions
const TEXT_EXTENSIONS = new Set([
  // Code/Scripts
  'js', 'jsx', 'ts', 'tsx', 'mjs', 'cjs', 'vue', 'svelte',
  'py', 'pyw', 'pyi',
  'sh', 'bash', 'zsh', 'fish', 'bat', 'cmd', 'ps1', 'psm1',
  'c', 'cpp', 'h', 'hpp', 'cc', 'cxx', 'hh', 'hxx',
  'java', 'scala', 'kt', 'kts', 'groovy', 'gradle',
  'go', 'rs', 'rb', 'php', 'pl', 'pm', 'lua', 'r', 'R',
  'swift', 'dart', 'cs', 'fs', 'vb',
  'ex', 'exs', 'erl', 'hrl', 'clj', 'cljs', 'cljc',
  'hs', 'lhs', 'elm', 'ml', 'mli', 'nim',
  // Web
  'html', 'htm', 'xhtml', 'css', 'scss', 'sass', 'less', 'styl',
  // Config/Data
  'json', 'json5', 'jsonc', 'xml', 'xsl', 'xslt', 'xsd',
  'yml', 'yaml', 'toml', 'ini', 'conf', 'cfg', 'config', 'properties',
  'env', 'gitignore', 'gitattributes', 'editorconfig', 'eslintrc', 'prettierrc',
  'sql', 'graphql', 'gql',
  // Text/Docs
  'md', 'markdown', 'mdx', 'txt', 'text', 'log', 'rst', 'adoc', 'asciidoc',
  'tex', 'latex', 'bib',
  // Data formats
  'csv', 'tsv',
  // System
  'rc', 'bashrc', 'zshrc', 'profile', 'vimrc', 'tmux.conf',
  'dockerfile', 'containerfile', 'makefile', 'cmakelists',
  // Other
  'diff', 'patch', 'htaccess', 'gitmodules',
]);

// Common image file extensions
const IMAGE_EXTENSIONS = new Set([
  'jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp', 'svg',
  'ico', 'tiff', 'tif', 'heic', 'heif', 'avif', 'jfif',
]);

// MIME types for images (for creating blob URLs)
const IMAGE_MIME_TYPES: Record<string, string> = {
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  jfif: 'image/jpeg',
  png: 'image/png',
  gif: 'image/gif',
  bmp: 'image/bmp',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  tiff: 'image/tiff',
  tif: 'image/tiff',
  heic: 'image/heic',
  heif: 'image/heif',
  avif: 'image/avif',
};

// Language IDs for syntax highlighting
const EXTENSION_TO_LANGUAGE: Record<string, string> = {
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  ts: 'typescript',
  tsx: 'typescript',
  py: 'python',
  pyw: 'python',
  pyi: 'python',
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
  fish: 'shell',
  bat: 'batch',
  cmd: 'batch',
  ps1: 'powershell',
  psm1: 'powershell',
  c: 'c',
  cpp: 'cpp',
  h: 'c',
  hpp: 'cpp',
  cc: 'cpp',
  cxx: 'cpp',
  java: 'java',
  kt: 'kotlin',
  kts: 'kotlin',
  go: 'go',
  rs: 'rust',
  rb: 'ruby',
  php: 'php',
  pl: 'perl',
  lua: 'lua',
  r: 'r',
  R: 'r',
  swift: 'swift',
  dart: 'dart',
  cs: 'csharp',
  fs: 'fsharp',
  vb: 'vb',
  html: 'html',
  htm: 'html',
  xhtml: 'html',
  css: 'css',
  scss: 'scss',
  sass: 'sass',
  less: 'less',
  json: 'json',
  jsonc: 'jsonc',
  json5: 'json5',
  xml: 'xml',
  xsl: 'xml',
  xslt: 'xml',
  yml: 'yaml',
  yaml: 'yaml',
  toml: 'toml',
  ini: 'ini',
  conf: 'ini',
  cfg: 'ini',
  sql: 'sql',
  graphql: 'graphql',
  gql: 'graphql',
  md: 'markdown',
  markdown: 'markdown',
  mdx: 'markdown',
  txt: 'plaintext',
  log: 'plaintext',
  vue: 'vue',
  svelte: 'svelte',
  dockerfile: 'dockerfile',
  makefile: 'makefile',
  diff: 'diff',
  patch: 'diff',
};

/**
 * Get the file extension from a filename
 * For files without extension, returns 'file'
 */
export function getFileExtension(fileName: string): string {
  const lastDot = fileName.lastIndexOf('.');
  if (lastDot === -1 || lastDot === 0) {
    return 'file'; // No extension or hidden file without extension
  }
  return fileName.slice(lastDot + 1).toLowerCase();
}

/**
 * Check if a file is a text file based on its extension
 */
export function isTextFile(fileName: string): boolean {
  const ext = getFileExtension(fileName);
  
  // Check known text extensions
  if (TEXT_EXTENSIONS.has(ext)) {
    return true;
  }
  
  // Check common filenames that are text but have no extension
  const baseName = fileName.toLowerCase();
  const textFileNames = [
    'readme', 'license', 'licence', 'changelog', 'authors', 'contributors',
    'copying', 'install', 'news', 'todo', 'history', 'makefile', 'dockerfile',
    '.gitignore', '.gitattributes', '.editorconfig', '.eslintrc', '.prettierrc',
    '.npmrc', '.yarnrc', '.env', '.env.local', '.env.example',
    'procfile', 'gemfile', 'rakefile', 'brewfile',
  ];
  
  return textFileNames.some(name => baseName === name || baseName.endsWith('/' + name));
}

/**
 * Check if binary data appears to be text by analyzing byte patterns
 * This provides a more accurate detection than extension-only checking
 * 
 * @param data - First chunk of file data (ArrayBuffer or Uint8Array)
 * @param maxBytes - Maximum bytes to check (default 512)
 * @returns true if data appears to be text
 */
export function isTextData(data: ArrayBuffer | Uint8Array, maxBytes: number = 512): boolean {
  const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : data;
  const checkLength = Math.min(bytes.length, maxBytes);
  
  if (checkLength === 0) return true; // Empty file is considered text
  
  let controlChars = 0;
  let nullBytes = 0;
  let highBytes = 0;
  let totalBytes = 0;
  
  for (let i = 0; i < checkLength; i++) {
    const byte = bytes[i];
    totalBytes++;
    
    // Null bytes are strong indicators of binary files
    if (byte === 0) {
      nullBytes++;
      if (nullBytes > 0) return false; // Even one null byte suggests binary
    }
    
    // Control characters (except common ones like \t, \n, \r)
    if (byte < 32 && byte !== 9 && byte !== 10 && byte !== 13) {
      controlChars++;
    }
    
    // High-bit characters (non-ASCII) - some are OK for UTF-8
    if (byte > 127) {
      highBytes++;
    }
  }
  
  // If more than 30% are control chars or more than 95% are high-bit chars, likely binary
  const controlRatio = controlChars / totalBytes;
  const highRatio = highBytes / totalBytes;
  
  if (controlRatio > 0.3) return false;
  if (highRatio > 0.95) return false;
  
  return true;
}

/**
 * Enhanced text file detection combining extension and content analysis
 * Use this when you have access to file data for better accuracy
 */
export function isTextFileEnhanced(fileName: string, data?: ArrayBuffer | Uint8Array): boolean {
  // First check by extension
  const extCheck = isTextFile(fileName);
  
  // If we have data, verify it's actually text
  if (data && data.byteLength > 0) {
    return extCheck && isTextData(data);
  }
  
  // Fall back to extension-only check
  return extCheck;
}

/**
 * Check if a file is an image file based on its extension
 */
export function isImageFile(fileName: string): boolean {
  const ext = getFileExtension(fileName);
  return IMAGE_EXTENSIONS.has(ext);
}

/**
 * Get MIME type for an image file
 */
export function getImageMimeType(fileName: string): string {
  const ext = getFileExtension(fileName);
  return IMAGE_MIME_TYPES[ext] || 'application/octet-stream';
}

/**
 * Get language ID for syntax highlighting
 */
export function getLanguageId(fileName: string): string {
  const ext = getFileExtension(fileName);
  return EXTENSION_TO_LANGUAGE[ext] || 'plaintext';
}

/**
 * Get a user-friendly name for a language
 */
export function getLanguageName(languageId: string): string {
  const names: Record<string, string> = {
    javascript: 'JavaScript',
    typescript: 'TypeScript',
    python: 'Python',
    shell: 'Shell',
    batch: 'Batch',
    powershell: 'PowerShell',
    c: 'C',
    cpp: 'C++',
    java: 'Java',
    kotlin: 'Kotlin',
    go: 'Go',
    rust: 'Rust',
    ruby: 'Ruby',
    php: 'PHP',
    perl: 'Perl',
    lua: 'Lua',
    r: 'R',
    swift: 'Swift',
    dart: 'Dart',
    csharp: 'C#',
    fsharp: 'F#',
    vb: 'Visual Basic',
    html: 'HTML',
    css: 'CSS',
    scss: 'SCSS',
    sass: 'Sass',
    less: 'Less',
    json: 'JSON',
    jsonc: 'JSON with Comments',
    json5: 'JSON5',
    xml: 'XML',
    yaml: 'YAML',
    toml: 'TOML',
    ini: 'INI',
    sql: 'SQL',
    graphql: 'GraphQL',
    markdown: 'Markdown',
    plaintext: 'Plain Text',
    vue: 'Vue',
    svelte: 'Svelte',
    dockerfile: 'Dockerfile',
    makefile: 'Makefile',
    diff: 'Diff',
  };
  return names[languageId] || languageId.charAt(0).toUpperCase() + languageId.slice(1);
}

/**
 * File opener application types
 */
export type FileOpenerType = 'builtin-editor' | 'builtin-image-viewer';

/**
 * File association record
 */
export interface FileAssociation {
  extension: string;
  openerType: FileOpenerType;
}

/**
 * Get all supported language IDs for syntax highlighting dropdown
 */
export function getSupportedLanguages(): { id: string; name: string }[] {
  const languageIds = new Set(Object.values(EXTENSION_TO_LANGUAGE));
  languageIds.add('plaintext');
  
  return Array.from(languageIds)
    .map(id => ({ id, name: getLanguageName(id) }))
    .sort((a, b) => a.name.localeCompare(b.name));
}
