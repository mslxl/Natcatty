export const isWindowsPath = (path: string): boolean => /^[A-Za-z]:/.test(path);

export const normalizeWindowsRoot = (path: string): string => {
  const normalized = path.replace(/\//g, "\\");
  if (/^[A-Za-z]:\\$/.test(normalized)) return normalized;
  if (/^[A-Za-z]:$/.test(normalized)) return `${normalized}\\`;
  return normalized;
};

export const joinPath = (base: string, name: string, isLocalSession: boolean): string => {
  if (isLocalSession && isWindowsPath(base)) {
    const normalizedBase = normalizeWindowsRoot(base).replace(/[\\/]+$/, "");
    return `${normalizedBase}\\${name}`;
  }
  if (base === "/") return `/${name}`;
  return `${base}/${name}`;
};

export const isRootPath = (path: string, isLocalSession: boolean): boolean => {
  if (isLocalSession && isWindowsPath(path)) {
    return /^[A-Za-z]:\\?$/.test(path.replace(/\//g, "\\"));
  }
  return path === "/";
};

export const getParentPath = (path: string, isLocalSession: boolean): string => {
  if (isLocalSession && isWindowsPath(path)) {
    const normalized = normalizeWindowsRoot(path).replace(/[\\]+$/, "");
    const drive = normalized.slice(0, 2);
    if (/^[A-Za-z]:$/.test(normalized) || /^[A-Za-z]:\\$/.test(normalized)) {
      return `${drive}\\`;
    }
    const rest = normalized.slice(2).replace(/^[\\]+/, "");
    const parts = rest ? rest.split(/[\\]+/).filter(Boolean) : [];
    if (parts.length <= 1) return `${drive}\\`;
    parts.pop();
    return `${drive}\\${parts.join("\\")}`;
  }
  if (path === "/") return "/";
  const parts = path.split("/").filter(Boolean);
  parts.pop();
  return parts.length ? `/${parts.join("/")}` : "/";
};

export const getRootPath = (path: string, isLocalSession: boolean): string => {
  if (isLocalSession && isWindowsPath(path)) {
    const drive = path.replace(/\//g, "\\").slice(0, 2);
    return `${drive}\\`;
  }
  return "/";
};

export const getWindowsDrive = (path: string): string | null => {
  if (!isWindowsPath(path)) return null;
  const normalized = path.replace(/\//g, "\\");
  return /^[A-Za-z]:/.test(normalized) ? normalized.slice(0, 2) : null;
};

export const getBreadcrumbs = (path: string, isLocalSession: boolean): string[] => {
  if (isLocalSession && isWindowsPath(path)) {
    const normalized = normalizeWindowsRoot(path).replace(/[\\]+$/, "");
    const rest = normalized.slice(2).replace(/^[\\]+/, "");
    const parts = rest ? rest.split(/[\\]+/).filter(Boolean) : [];
    return parts;
  }
  return path === "/" ? [] : path.split("/").filter(Boolean);
};

export const breadcrumbPathAt = (
  breadcrumbs: string[],
  idx: number,
  currentPath: string,
  isLocalSession: boolean,
): string => {
  if (isLocalSession) {
    const drive = getWindowsDrive(currentPath);
    if (drive) {
      const rest = breadcrumbs.slice(0, idx + 1).join("\\");
      return rest ? `${drive}\\${rest}` : `${drive}\\`;
    }
  }
  return "/" + breadcrumbs.slice(0, idx + 1).join("/");
};
