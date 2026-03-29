import React, { useCallback, useMemo, useState } from "react";
import type { MutableRefObject } from "react";
import type { Host } from "../../../types";
import type { SftpStateApi } from "../../../application/state/useSftpState";

interface UseSftpViewTabsParams {
  sftp: SftpStateApi;
  sftpRef: MutableRefObject<SftpStateApi>;
}

interface UseSftpViewTabsResult {
  leftPanes: SftpStateApi["leftPane"][];
  rightPanes: SftpStateApi["rightPane"][];
  leftTabsInfo: { id: string; label: string; isLocal: boolean; hostId: string | null }[];
  rightTabsInfo: { id: string; label: string; isLocal: boolean; hostId: string | null }[];
  showHostPickerLeft: boolean;
  showHostPickerRight: boolean;
  hostSearchLeft: string;
  hostSearchRight: string;
  setShowHostPickerLeft: React.Dispatch<React.SetStateAction<boolean>>;
  setShowHostPickerRight: React.Dispatch<React.SetStateAction<boolean>>;
  setHostSearchLeft: React.Dispatch<React.SetStateAction<string>>;
  setHostSearchRight: React.Dispatch<React.SetStateAction<string>>;
  handleAddTabLeft: () => void;
  handleAddTabRight: () => void;
  handleCloseTabLeft: (tabId: string) => void;
  handleCloseTabRight: (tabId: string) => void;
  handleSelectTabLeft: (tabId: string) => void;
  handleSelectTabRight: (tabId: string) => void;
  handleReorderTabsLeft: (draggedId: string, targetId: string, position: "before" | "after") => void;
  handleReorderTabsRight: (draggedId: string, targetId: string, position: "before" | "after") => void;
  handleMoveTabFromLeftToRight: (tabId: string) => void;
  handleMoveTabFromRightToLeft: (tabId: string) => void;
  handleHostSelectLeft: (host: Host | "local") => void;
  handleHostSelectRight: (host: Host | "local") => void;
}

export const useSftpViewTabs = ({ sftp, sftpRef }: UseSftpViewTabsParams): UseSftpViewTabsResult => {
  const [showHostPickerLeft, setShowHostPickerLeft] = useState(false);
  const [showHostPickerRight, setShowHostPickerRight] = useState(false);
  const [hostSearchLeft, setHostSearchLeft] = useState("");
  const [hostSearchRight, setHostSearchRight] = useState("");

  const handleAddTabLeft = useCallback(() => {
    sftpRef.current.addTab("left");
    setShowHostPickerLeft(true);
  }, [sftpRef]);

  const handleAddTabRight = useCallback(() => {
    sftpRef.current.addTab("right");
    setShowHostPickerRight(true);
  }, [sftpRef]);

  const handleCloseTabLeft = useCallback((tabId: string) => {
    sftpRef.current.closeTab("left", tabId);
  }, [sftpRef]);

  const handleCloseTabRight = useCallback((tabId: string) => {
    sftpRef.current.closeTab("right", tabId);
  }, [sftpRef]);

  const handleSelectTabLeft = useCallback((tabId: string) => {
    sftpRef.current.selectTab("left", tabId);
  }, [sftpRef]);

  const handleSelectTabRight = useCallback((tabId: string) => {
    sftpRef.current.selectTab("right", tabId);
  }, [sftpRef]);

  const leftPanes = useMemo(
    () => (sftp.leftTabs.tabs.length > 0 ? sftp.leftTabs.tabs : [sftp.leftPane]),
    [sftp.leftTabs.tabs, sftp.leftPane],
  );
  const rightPanes = useMemo(
    () => (sftp.rightTabs.tabs.length > 0 ? sftp.rightTabs.tabs : [sftp.rightPane]),
    [sftp.rightTabs.tabs, sftp.rightPane],
  );

  const handleReorderTabsLeft = useCallback(
    (draggedId: string, targetId: string, position: "before" | "after") => {
      sftpRef.current.reorderTabs("left", draggedId, targetId, position);
    },
    [sftpRef],
  );

  const handleReorderTabsRight = useCallback(
    (draggedId: string, targetId: string, position: "before" | "after") => {
      sftpRef.current.reorderTabs("right", draggedId, targetId, position);
    },
    [sftpRef],
  );

  const handleMoveTabFromLeftToRight = useCallback((tabId: string) => {
    sftpRef.current.moveTabToOtherSide("left", tabId);
  }, [sftpRef]);

  const handleMoveTabFromRightToLeft = useCallback((tabId: string) => {
    sftpRef.current.moveTabToOtherSide("right", tabId);
  }, [sftpRef]);

  const handleHostSelectLeft = useCallback((host: Host | "local") => {
    sftpRef.current.connect("left", host);
    setShowHostPickerLeft(false);
  }, [sftpRef]);

  const handleHostSelectRight = useCallback((host: Host | "local") => {
    sftpRef.current.connect("right", host);
    setShowHostPickerRight(false);
  }, [sftpRef]);

  const leftTabsInfo = useMemo(
    () =>
      sftp.leftTabs.tabs.map((pane) => ({
        id: pane.id,
        label: pane.connection?.hostLabel || "New Tab",
        isLocal: pane.connection?.isLocal || false,
        hostId: pane.connection?.hostId || null,
      })),
    [sftp.leftTabs.tabs],
  );

  const rightTabsInfo = useMemo(
    () =>
      sftp.rightTabs.tabs.map((pane) => ({
        id: pane.id,
        label: pane.connection?.hostLabel || "New Tab",
        isLocal: pane.connection?.isLocal || false,
        hostId: pane.connection?.hostId || null,
      })),
    [sftp.rightTabs.tabs],
  );

  return {
    leftPanes,
    rightPanes,
    leftTabsInfo,
    rightTabsInfo,
    showHostPickerLeft,
    showHostPickerRight,
    hostSearchLeft,
    hostSearchRight,
    setShowHostPickerLeft,
    setShowHostPickerRight,
    setHostSearchLeft,
    setHostSearchRight,
    handleAddTabLeft,
    handleAddTabRight,
    handleCloseTabLeft,
    handleCloseTabRight,
    handleSelectTabLeft,
    handleSelectTabRight,
    handleReorderTabsLeft,
    handleReorderTabsRight,
    handleMoveTabFromLeftToRight,
    handleMoveTabFromRightToLeft,
    handleHostSelectLeft,
    handleHostSelectRight,
  };
};
