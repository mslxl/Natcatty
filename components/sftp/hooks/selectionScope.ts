import type { SftpStateApi } from "../../../application/state/useSftpState";
import { sftpTreeSelectionStore } from "./useSftpTreeSelectionStore";

export interface SftpSelectionTarget {
  side: "left" | "right";
  tabId: string;
}

export const keepOnlyPaneSelections = (
  sftp: SftpStateApi,
  target: SftpSelectionTarget | null,
) => {
  sftp.clearSelectionsExcept(target);
  if (target) {
    sftpTreeSelectionStore.clearAllExcept([target.tabId]);
    return;
  }
  sftpTreeSelectionStore.clearAllExcept();
};

export const keepOnlyActivePaneSelections = (
  sftp: SftpStateApi,
  side: "left" | "right",
): SftpSelectionTarget | null => {
  const tabId = sftp.getActiveTabId(side);
  if (!tabId) {
    keepOnlyPaneSelections(sftp, null);
    return null;
  }

  const target = { side, tabId } as const;
  keepOnlyPaneSelections(sftp, target);
  return target;
};
