import { useCallback, useMemo, useState } from "react";
import type { MutableRefObject } from "react";
import type { SftpStateApi } from "../../../application/state/useSftpState";
import type { SftpDragCallbacks } from "../SftpContext";

interface UseSftpViewPaneActionsParams {
  sftpRef: MutableRefObject<SftpStateApi>;
}

interface UseSftpViewPaneActionsResult {
  dragCallbacks: SftpDragCallbacks;
  draggedFiles: { name: string; isDirectory: boolean; side: "left" | "right" }[] | null;
  onConnectLeft: (host: Parameters<SftpStateApi["connect"]>[1]) => void;
  onConnectRight: (host: Parameters<SftpStateApi["connect"]>[1]) => void;
  onDisconnectLeft: () => void;
  onDisconnectRight: () => void;
  onNavigateToLeft: (path: string) => void;
  onNavigateToRight: (path: string) => void;
  onNavigateUpLeft: () => void;
  onNavigateUpRight: () => void;
  onRefreshLeft: () => void;
  onRefreshRight: () => void;
  onSetFilenameEncodingLeft: (encoding: Parameters<SftpStateApi["setFilenameEncoding"]>[1]) => void;
  onSetFilenameEncodingRight: (encoding: Parameters<SftpStateApi["setFilenameEncoding"]>[1]) => void;
  onToggleSelectionLeft: (name: string, multi: boolean) => void;
  onToggleSelectionRight: (name: string, multi: boolean) => void;
  onRangeSelectLeft: (fileNames: string[]) => void;
  onRangeSelectRight: (fileNames: string[]) => void;
  onClearSelectionLeft: () => void;
  onClearSelectionRight: () => void;
  onSetFilterLeft: (filter: string) => void;
  onSetFilterRight: (filter: string) => void;
  onCreateDirectoryLeft: (name: string) => void;
  onCreateDirectoryRight: (name: string) => void;
  onCreateFileLeft: (name: string) => void;
  onCreateFileRight: (name: string) => void;
  onDeleteFilesLeft: (names: string[]) => void;
  onDeleteFilesRight: (names: string[]) => void;
  onRenameFileLeft: (old: string, newName: string) => void;
  onRenameFileRight: (old: string, newName: string) => void;
  onCopyToOtherPaneLeft: (files: { name: string; isDirectory: boolean }[]) => void;
  onCopyToOtherPaneRight: (files: { name: string; isDirectory: boolean }[]) => void;
  onReceiveFromOtherPaneLeft: (files: { name: string; isDirectory: boolean }[]) => void;
  onReceiveFromOtherPaneRight: (files: { name: string; isDirectory: boolean }[]) => void;
}

export const useSftpViewPaneActions = ({
  sftpRef,
}: UseSftpViewPaneActionsParams): UseSftpViewPaneActionsResult => {
  const [draggedFiles, setDraggedFiles] = useState<
    { name: string; isDirectory: boolean; side: "left" | "right" }[] | null
  >(null);

  const handleDragStart = useCallback(
    (
      files: { name: string; isDirectory: boolean }[],
      side: "left" | "right",
    ) => {
      setDraggedFiles(files.map((f) => ({ ...f, side })));
    },
    [],
  );

  const handleDragEnd = useCallback(() => {
    setDraggedFiles(null);
  }, []);

  const onCopyToOtherPaneLeft = useCallback(
    (files: { name: string; isDirectory: boolean }[]) =>
      sftpRef.current.startTransfer(files, "left", "right"),
    [],
  );
  const onCopyToOtherPaneRight = useCallback(
    (files: { name: string; isDirectory: boolean }[]) =>
      sftpRef.current.startTransfer(files, "right", "left"),
    [],
  );
  const onReceiveFromOtherPaneLeft = useCallback(
    (files: { name: string; isDirectory: boolean }[]) =>
      sftpRef.current.startTransfer(files, "right", "left"),
    [],
  );
  const onReceiveFromOtherPaneRight = useCallback(
    (files: { name: string; isDirectory: boolean }[]) =>
      sftpRef.current.startTransfer(files, "left", "right"),
    [],
  );

  const onConnectLeft = useCallback(
    (host: Parameters<SftpStateApi["connect"]>[1]) => sftpRef.current.connect("left", host),
    [],
  );
  const onConnectRight = useCallback(
    (host: Parameters<SftpStateApi["connect"]>[1]) => sftpRef.current.connect("right", host),
    [],
  );
  const onDisconnectLeft = useCallback(() => sftpRef.current.disconnect("left"), []);
  const onDisconnectRight = useCallback(() => sftpRef.current.disconnect("right"), []);
  const onNavigateToLeft = useCallback(
    (path: string) => sftpRef.current.navigateTo("left", path),
    [],
  );
  const onNavigateToRight = useCallback(
    (path: string) => sftpRef.current.navigateTo("right", path),
    [],
  );
  const onNavigateUpLeft = useCallback(() => sftpRef.current.navigateUp("left"), []);
  const onNavigateUpRight = useCallback(() => sftpRef.current.navigateUp("right"), []);
  const onRefreshLeft = useCallback(() => sftpRef.current.refresh("left"), []);
  const onRefreshRight = useCallback(() => sftpRef.current.refresh("right"), []);
  const onSetFilenameEncodingLeft = useCallback(
    (encoding: Parameters<SftpStateApi["setFilenameEncoding"]>[1]) =>
      sftpRef.current.setFilenameEncoding("left", encoding),
    [],
  );
  const onSetFilenameEncodingRight = useCallback(
    (encoding: Parameters<SftpStateApi["setFilenameEncoding"]>[1]) =>
      sftpRef.current.setFilenameEncoding("right", encoding),
    [],
  );
  const onToggleSelectionLeft = useCallback(
    (name: string, multi: boolean) => sftpRef.current.toggleSelection("left", name, multi),
    [],
  );
  const onToggleSelectionRight = useCallback(
    (name: string, multi: boolean) => sftpRef.current.toggleSelection("right", name, multi),
    [],
  );
  const onRangeSelectLeft = useCallback(
    (fileNames: string[]) => sftpRef.current.rangeSelect("left", fileNames),
    [],
  );
  const onRangeSelectRight = useCallback(
    (fileNames: string[]) => sftpRef.current.rangeSelect("right", fileNames),
    [],
  );
  const onClearSelectionLeft = useCallback(() => sftpRef.current.clearSelection("left"), []);
  const onClearSelectionRight = useCallback(() => sftpRef.current.clearSelection("right"), []);
  const onSetFilterLeft = useCallback(
    (filter: string) => sftpRef.current.setFilter("left", filter),
    [],
  );
  const onSetFilterRight = useCallback(
    (filter: string) => sftpRef.current.setFilter("right", filter),
    [],
  );
  const onCreateDirectoryLeft = useCallback(
    (name: string) => sftpRef.current.createDirectory("left", name),
    [],
  );
  const onCreateDirectoryRight = useCallback(
    (name: string) => sftpRef.current.createDirectory("right", name),
    [],
  );
  const onCreateFileLeft = useCallback(
    (name: string) => sftpRef.current.createFile("left", name),
    [],
  );
  const onCreateFileRight = useCallback(
    (name: string) => sftpRef.current.createFile("right", name),
    [],
  );
  const onDeleteFilesLeft = useCallback(
    (names: string[]) => sftpRef.current.deleteFiles("left", names),
    [],
  );
  const onDeleteFilesRight = useCallback(
    (names: string[]) => sftpRef.current.deleteFiles("right", names),
    [],
  );
  const onRenameFileLeft = useCallback(
    (old: string, newName: string) => sftpRef.current.renameFile("left", old, newName),
    [],
  );
  const onRenameFileRight = useCallback(
    (old: string, newName: string) => sftpRef.current.renameFile("right", old, newName),
    [],
  );

  const dragCallbacks = useMemo<SftpDragCallbacks>(
    () => ({
      onDragStart: handleDragStart,
      onDragEnd: handleDragEnd,
    }),
    [],
  );

  return {
    dragCallbacks,
    draggedFiles,
    onConnectLeft,
    onConnectRight,
    onDisconnectLeft,
    onDisconnectRight,
    onNavigateToLeft,
    onNavigateToRight,
    onNavigateUpLeft,
    onNavigateUpRight,
    onRefreshLeft,
    onRefreshRight,
    onSetFilenameEncodingLeft,
    onSetFilenameEncodingRight,
    onToggleSelectionLeft,
    onToggleSelectionRight,
    onRangeSelectLeft,
    onRangeSelectRight,
    onClearSelectionLeft,
    onClearSelectionRight,
    onSetFilterLeft,
    onSetFilterRight,
    onCreateDirectoryLeft,
    onCreateDirectoryRight,
    onCreateFileLeft,
    onCreateFileRight,
    onDeleteFilesLeft,
    onDeleteFilesRight,
    onRenameFileLeft,
    onRenameFileRight,
    onCopyToOtherPaneLeft,
    onCopyToOtherPaneRight,
    onReceiveFromOtherPaneLeft,
    onReceiveFromOtherPaneRight,
  };
};
