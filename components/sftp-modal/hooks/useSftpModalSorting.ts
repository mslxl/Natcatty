import { useCallback, useRef, useState } from "react";

export type SortField = "name" | "size" | "modified";
export type SortOrder = "asc" | "desc";

interface UseSftpModalSortingResult {
  sortField: SortField;
  sortOrder: SortOrder;
  columnWidths: { name: number; size: number; modified: number; actions: number };
  handleSort: (field: SortField) => void;
  handleResizeStart: (field: string, e: React.MouseEvent) => void;
}

export const useSftpModalSorting = (): UseSftpModalSortingResult => {
  const [sortField, setSortField] = useState<SortField>("name");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");
  const [columnWidths, setColumnWidths] = useState({
    name: 45,
    size: 15,
    modified: 25,
    actions: 15,
  });

  const resizingRef = useRef<{
    field: string;
    startX: number;
    startWidth: number;
  } | null>(null);

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortOrder((prev) => (prev === "asc" ? "desc" : "asc"));
    } else {
      setSortField(field);
      setSortOrder("asc");
    }
  };

  const handleResizeMove = useCallback((e: MouseEvent) => {
    if (!resizingRef.current) return;
    const diff = e.clientX - resizingRef.current.startX;
    const newWidth = Math.max(
      10,
      Math.min(60, resizingRef.current.startWidth + diff / 5),
    );
    setColumnWidths((prev) => ({
      ...prev,
      [resizingRef.current!.field]: newWidth,
    }));
  }, []);

  const handleResizeEnd = useCallback(() => {
    resizingRef.current = null;
    document.removeEventListener("mousemove", handleResizeMove);
    document.removeEventListener("mouseup", handleResizeEnd);
  }, [handleResizeMove]);

  const handleResizeStart = (field: string, e: React.MouseEvent) => {
    e.preventDefault();
    resizingRef.current = {
      field,
      startX: e.clientX,
      startWidth: columnWidths[field as keyof typeof columnWidths],
    };
    document.addEventListener("mousemove", handleResizeMove);
    document.addEventListener("mouseup", handleResizeEnd);
  };

  return {
    sortField,
    sortOrder,
    columnWidths,
    handleSort,
    handleResizeStart,
  };
};
