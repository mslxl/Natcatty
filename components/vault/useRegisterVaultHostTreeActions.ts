import { useEffect } from 'react';

import { activeTabStore } from '../../application/state/activeTabStore';
import {
  vaultHostTreeActionsStore,
  type VaultHostTreeActions,
} from '../../application/state/vaultHostTreeActionsStore';
import type { Host } from '../../types';

type RegisterVaultHostTreeActionsParams = {
  handleCopyCredentials: (host: Host) => void;
  onDeleteHost: (hostId: string) => void;
  handleUnmanageGroup?: (groupPath: string) => void;
  moveHostToGroup: (hostId: string, groupPath: string | null) => void;
  moveGroup: (sourcePath: string, targetParent: string | null) => void;
  managedGroupPaths?: Set<string>;
  startInlineNewGroup: (parentPath?: string) => void;
  startInlineRenameGroup: (groupPath: string) => void;
  startInlineDeleteGroup: (groupPath: string) => void;
  commitInlineGroupRename: (name: string) => void;
  cancelInlineGroupEdit: () => void;
};

function focusVaultTab() {
  activeTabStore.setActiveTabId('vault');
}

function withVaultFocus<T extends (...args: never[]) => void>(fn: T): T {
  return ((...args: Parameters<T>) => {
    focusVaultTab();
    fn(...args);
  }) as T;
}

export function useRegisterVaultHostTreeActions({
  handleCopyCredentials,
  onDeleteHost,
  handleUnmanageGroup,
  moveHostToGroup,
  moveGroup,
  managedGroupPaths,
  startInlineNewGroup,
  startInlineRenameGroup,
  startInlineDeleteGroup,
  commitInlineGroupRename,
  cancelInlineGroupEdit,
}: RegisterVaultHostTreeActionsParams) {
  useEffect(() => {
    const actions: VaultHostTreeActions = {
      onCopyCredentials: handleCopyCredentials,
      onDeleteHost: (host) => onDeleteHost(host.id),
      onNewGroup: startInlineNewGroup,
      onRenameGroup: startInlineRenameGroup,
      onDeleteGroup: startInlineDeleteGroup,
      commitInlineGroupRename,
      cancelInlineGroupEdit,
      moveHostToGroup,
      moveGroup,
      managedGroupPaths,
      onUnmanageGroup: handleUnmanageGroup
        ? withVaultFocus(handleUnmanageGroup)
        : undefined,
    };

    vaultHostTreeActionsStore.setActions(actions);
    return () => vaultHostTreeActionsStore.setActions(null);
  }, [
    cancelInlineGroupEdit,
    commitInlineGroupRename,
    handleCopyCredentials,
    handleUnmanageGroup,
    managedGroupPaths,
    moveGroup,
    moveHostToGroup,
    onDeleteHost,
    startInlineDeleteGroup,
    startInlineNewGroup,
    startInlineRenameGroup,
  ]);
}
