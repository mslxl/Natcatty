import { useCallback, useMemo, useRef, useState } from "react";
import { breadcrumbPathAt, getBreadcrumbs, getRootPath, getWindowsDrive, isWindowsPath } from "../pathUtils";

interface UseSftpModalPathParams {
  currentPath: string;
  isLocalSession: boolean;
  localHomePath: string | null;
  onNavigate: (path: string) => void;
  maxVisibleBreadcrumbParts?: number;
}

interface UseSftpModalPathResult {
  isEditingPath: boolean;
  editingPathValue: string;
  setEditingPathValue: (value: string) => void;
  pathInputRef: React.RefObject<HTMLInputElement>;
  handlePathDoubleClick: () => void;
  handlePathSubmit: () => void;
  handlePathKeyDown: (e: React.KeyboardEvent) => void;
  breadcrumbs: string[];
  visibleBreadcrumbs: { part: string; originalIndex: number }[];
  hiddenBreadcrumbs: { part: string; originalIndex: number }[];
  needsBreadcrumbTruncation: boolean;
  breadcrumbPathAtForIndex: (index: number) => string;
  rootLabel: string;
  rootPath: string;
}

export const useSftpModalPath = ({
  currentPath,
  isLocalSession,
  localHomePath,
  onNavigate,
  maxVisibleBreadcrumbParts = 4,
}: UseSftpModalPathParams): UseSftpModalPathResult => {
  const [isEditingPath, setIsEditingPath] = useState(false);
  const [editingPathValue, setEditingPathValue] = useState("");
  const pathInputRef = useRef<HTMLInputElement>(null);

  const handlePathDoubleClick = () => {
    setEditingPathValue(currentPath);
    setIsEditingPath(true);
    setTimeout(() => pathInputRef.current?.select(), 0);
  };

  const handlePathSubmit = () => {
    const fallbackPath = localHomePath || getRootPath(currentPath, isLocalSession);
    const newPath = editingPathValue.trim() || fallbackPath;
    setIsEditingPath(false);
    if (newPath !== currentPath) {
      if (isLocalSession) {
        onNavigate(newPath);
      } else {
        onNavigate(newPath.startsWith("/") ? newPath : `/${newPath}`);
      }
    }
  };

  const handlePathKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      handlePathSubmit();
    } else if (e.key === "Escape") {
      setIsEditingPath(false);
    }
  };

  const breadcrumbs = useMemo(
    () => getBreadcrumbs(currentPath, isLocalSession),
    [currentPath, isLocalSession],
  );

  const { visibleBreadcrumbs, hiddenBreadcrumbs, needsBreadcrumbTruncation } =
    useMemo(() => {
      if (breadcrumbs.length <= maxVisibleBreadcrumbParts) {
        return {
          visibleBreadcrumbs: breadcrumbs.map((part, idx) => ({ part, originalIndex: idx })),
          hiddenBreadcrumbs: [] as { part: string; originalIndex: number }[],
          needsBreadcrumbTruncation: false,
        };
      }

      const firstPart = [{ part: breadcrumbs[0], originalIndex: 0 }];
      const lastPartsCount = maxVisibleBreadcrumbParts - 1;
      const lastParts = breadcrumbs.slice(-lastPartsCount).map((part, idx) => ({
        part,
        originalIndex: breadcrumbs.length - lastPartsCount + idx,
      }));
      const hidden = breadcrumbs.slice(1, -lastPartsCount).map((part, idx) => ({
        part,
        originalIndex: idx + 1,
      }));

      return {
        visibleBreadcrumbs: [...firstPart, ...lastParts],
        hiddenBreadcrumbs: hidden,
        needsBreadcrumbTruncation: true,
      };
    }, [breadcrumbs, maxVisibleBreadcrumbParts]);

  const breadcrumbPathAtForIndex = useCallback(
    (index: number) =>
      breadcrumbPathAt(breadcrumbs, index, currentPath, isLocalSession),
    [breadcrumbs, currentPath, isLocalSession],
  );

  const rootLabel = useMemo(
    () =>
      isLocalSession && isWindowsPath(currentPath)
        ? getWindowsDrive(currentPath) ?? "C:"
        : "/",
    [currentPath, isLocalSession],
  );

  const rootPath = useMemo(
    () => getRootPath(currentPath, isLocalSession),
    [currentPath, isLocalSession],
  );

  return {
    isEditingPath,
    editingPathValue,
    setEditingPathValue,
    pathInputRef,
    handlePathDoubleClick,
    handlePathSubmit,
    handlePathKeyDown,
    breadcrumbs,
    visibleBreadcrumbs,
    hiddenBreadcrumbs,
    needsBreadcrumbTruncation,
    breadcrumbPathAtForIndex,
    rootLabel,
    rootPath,
  };
};
