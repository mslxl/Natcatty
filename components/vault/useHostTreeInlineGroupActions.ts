import { useCallback } from 'react';

import { hostTreeInlineGroupDeleteStore } from '../../application/state/hostTreeInlineGroupDeleteStore';
import { hostTreeInlineGroupEditStore } from '../../application/state/hostTreeInlineGroupEditStore';
import { hostTreeInlineHostEditStore } from '../../application/state/hostTreeInlineHostEditStore';
import {
  allocateUnnamedGroupPath,
  applyGroupPathRename,
  ensureAncestorPathsExpanded,
  groupDisplayName,
} from '../../domain/hostGroupPathMutations';
import type { Host, ManagedSource } from '../../types';
import { toast } from '../ui/toast';

type UseHostTreeInlineGroupActionsParams = {
  customGroups: string[];
  hosts: Host[];
  managedSources: ManagedSource[];
  onUpdateCustomGroups: (groups: string[]) => void;
  onCommitGroupPathChange: (
    sourcePath: string,
    nextPath: string,
  ) => Promise<{ ok: true } | { ok: false; error?: string }>;
  selectedGroupPath: string | null;
  setSelectedGroupPath: (path: string | null) => void;
  ensurePathExpanded: (path: string) => void;
  unnamedGroupLabel: string;
  t: (key: string) => string;
};

export function useHostTreeInlineGroupActions({
  customGroups,
  hosts,
  managedSources,
  onUpdateCustomGroups,
  onCommitGroupPathChange,
  selectedGroupPath,
  setSelectedGroupPath,
  ensurePathExpanded,
  unnamedGroupLabel,
  t,
}: UseHostTreeInlineGroupActionsParams) {
  const startInlineNewGroup = useCallback((parentPath?: string) => {
    hostTreeInlineHostEditStore.clear();
    const parent = parentPath ?? null;
    const { name, path } = allocateUnnamedGroupPath(customGroups, parent, unnamedGroupLabel);
    onUpdateCustomGroups(Array.from(new Set([...customGroups, path])));
    if (parent) {
      ensureAncestorPathsExpanded(parent, ensurePathExpanded);
      ensurePathExpanded(parent);
    }
    hostTreeInlineGroupEditStore.startEdit({
      groupPath: path,
      initialName: name,
      isNew: true,
    });
  }, [customGroups, ensurePathExpanded, onUpdateCustomGroups, unnamedGroupLabel]);

  const startInlineRenameGroup = useCallback((groupPath: string) => {
    hostTreeInlineHostEditStore.clear();
    hostTreeInlineGroupEditStore.startEdit({
      groupPath,
      initialName: groupDisplayName(groupPath),
      isNew: false,
    });
  }, []);

  const cancelInlineGroupEdit = useCallback(() => {
    const edit = hostTreeInlineGroupEditStore.getEdit();
    if (!edit) return;
    if (edit.isNew) {
      onUpdateCustomGroups(customGroups.filter((groupPath) => groupPath !== edit.groupPath));
    }
    hostTreeInlineGroupEditStore.clear();
  }, [customGroups, onUpdateCustomGroups]);

  const commitInlineGroupRename = useCallback(async (rawName: string) => {
    const edit = hostTreeInlineGroupEditStore.getEdit();
    if (!edit) return;

    const result = applyGroupPathRename({
      renameTargetPath: edit.groupPath,
      nextName: rawName,
      customGroups,
      hosts,
      managedSources,
    });

    if (result.ok === false) {
      if (result.error === 'unchanged') {
        hostTreeInlineGroupEditStore.clear();
        return;
      }
      if (result.error === 'required') {
        if (edit.isNew) {
          cancelInlineGroupEdit();
          return;
        }
        toast.error(t('vault.groups.errors.required'));
        return;
      }
      if (result.error === 'invalidChars') {
        toast.error(t('vault.groups.errors.invalidChars'));
        return;
      }
      if (result.error === 'duplicatePath') {
        toast.error(t('vault.groups.errors.duplicatePath'));
        return;
      }
      return;
    }

    const committed = await onCommitGroupPathChange(edit.groupPath, result.nextPath);
    if ("error" in committed) {
      toast.error(committed.error || t('common.error'));
      return;
    }

    if (
      selectedGroupPath
      && (selectedGroupPath === edit.groupPath
        || selectedGroupPath.startsWith(`${edit.groupPath}/`))
    ) {
      const suffix = selectedGroupPath === edit.groupPath
        ? ''
        : selectedGroupPath.slice(edit.groupPath.length);
      setSelectedGroupPath(result.nextPath + suffix);
    }

    hostTreeInlineGroupEditStore.clear();
  }, [
    cancelInlineGroupEdit,
    customGroups,
    hosts,
    managedSources,
    onCommitGroupPathChange,
    selectedGroupPath,
    setSelectedGroupPath,
    t,
  ]);

  const startInlineDeleteGroup = useCallback((groupPath: string) => {
    hostTreeInlineGroupDeleteStore.open(groupPath);
  }, []);

  return {
    startInlineNewGroup,
    startInlineRenameGroup,
    commitInlineGroupRename,
    cancelInlineGroupEdit,
    startInlineDeleteGroup,
  };
}
