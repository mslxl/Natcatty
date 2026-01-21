import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { RemoteFile } from "../../types";

interface UseSftpModalVirtualListParams {
  open: boolean;
  sortedFiles: RemoteFile[];
}

interface UseSftpModalVirtualListResult {
  fileListRef: React.RefObject<HTMLDivElement>;
  rowHeight: number;
  handleFileListScroll: (e: React.UIEvent<HTMLDivElement>) => void;
  shouldVirtualize: boolean;
  totalHeight: number;
  visibleRows: { file: RemoteFile; index: number; top: number }[];
}

export const useSftpModalVirtualList = ({
  open,
  sortedFiles,
}: UseSftpModalVirtualListParams): UseSftpModalVirtualListResult => {
  const fileListRef = useRef<HTMLDivElement>(null);
  const scrollFrameRef = useRef<number | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  const [rowHeight, setRowHeight] = useState(40);

  useLayoutEffect(() => {
    const container = fileListRef.current;
    if (!container || !open) return;
    const update = () => setViewportHeight(container.clientHeight);
    update();
    const raf = window.requestAnimationFrame(update);
    const resizeObserver = new ResizeObserver(update);
    resizeObserver.observe(container);
    return () => {
      resizeObserver.disconnect();
      window.cancelAnimationFrame(raf);
    };
  }, [open, sortedFiles.length]);

  useLayoutEffect(() => {
    const container = fileListRef.current;
    if (!container || !open || sortedFiles.length === 0) return;
    const raf = window.requestAnimationFrame(() => {
      const rowElement = container.querySelector(
        '[data-sftp-modal-row="true"]',
      ) as HTMLElement | null;
      if (!rowElement) return;
      const nextHeight = Math.round(rowElement.getBoundingClientRect().height);
      if (nextHeight && Math.abs(nextHeight - rowHeight) > 1) {
        setRowHeight(nextHeight);
      }
    });
    return () => window.cancelAnimationFrame(raf);
  }, [open, rowHeight, sortedFiles.length]);

  useEffect(() => {
    return () => {
      if (scrollFrameRef.current !== null) {
        window.cancelAnimationFrame(scrollFrameRef.current);
      }
    };
  }, []);

  const handleFileListScroll = useCallback(
    (e: React.UIEvent<HTMLDivElement>) => {
      const nextTop = e.currentTarget.scrollTop;
      if (scrollFrameRef.current !== null) return;
      scrollFrameRef.current = window.requestAnimationFrame(() => {
        scrollFrameRef.current = null;
        setScrollTop(nextTop);
      });
    },
    [],
  );

  const { shouldVirtualize, totalHeight, visibleRows } = useMemo(() => {
    const overscan = 6;
    const canVirtualize = open && viewportHeight > 0 && rowHeight > 0;
    const shouldVirtualizeLocal = canVirtualize && sortedFiles.length > 50;
    const totalHeightLocal = shouldVirtualizeLocal
      ? sortedFiles.length * rowHeight
      : 0;
    const startIndex = shouldVirtualizeLocal
      ? Math.max(0, Math.floor(scrollTop / rowHeight) - overscan)
      : 0;
    const endIndex = shouldVirtualizeLocal
      ? Math.min(
        sortedFiles.length - 1,
        Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan,
      )
      : sortedFiles.length - 1;
    const visibleRowsLocal = shouldVirtualizeLocal
      ? sortedFiles
        .slice(startIndex, endIndex + 1)
        .map((file, idx) => ({
          file,
          index: startIndex + idx,
          top: (startIndex + idx) * rowHeight,
        }))
      : sortedFiles.map((file, index) => ({
        file,
        index,
        top: 0,
      }));

    return {
      shouldVirtualize: shouldVirtualizeLocal,
      totalHeight: totalHeightLocal,
      visibleRows: visibleRowsLocal,
    };
  }, [open, rowHeight, scrollTop, sortedFiles, viewportHeight]);

  return {
    fileListRef,
    rowHeight,
    handleFileListScroll,
    shouldVirtualize,
    totalHeight,
    visibleRows,
  };
};
