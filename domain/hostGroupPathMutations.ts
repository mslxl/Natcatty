import type { Host, ManagedSource } from '../types';

export function groupDisplayName(groupPath: string): string {
  return groupPath.split('/').filter(Boolean).pop() ?? groupPath;
}

export function computeRenamedGroupPath(renameTargetPath: string, nextName: string): string {
  const segments = renameTargetPath.split('/').filter(Boolean);
  const parent = segments.slice(0, -1).join('/');
  return parent ? `${parent}/${nextName}` : nextName;
}

export function allocateUnnamedGroupPath(
  customGroups: string[],
  parentPath: string | null,
  baseName: string,
): { name: string; path: string } {
  let name = baseName;
  let counter = 2;
  while (true) {
    const path = parentPath ? `${parentPath}/${name}` : name;
    if (!customGroups.includes(path)) {
      return { name, path };
    }
    name = `${baseName} ${counter}`;
    counter += 1;
  }
}

export function ensureAncestorPathsExpanded(
  groupPath: string,
  ensurePathExpanded: (path: string) => void,
) {
  const segments = groupPath.split('/').filter(Boolean);
  for (let i = 1; i <= segments.length; i++) {
    ensurePathExpanded(segments.slice(0, i).join('/'));
  }
}

export type GroupPathRenameResult =
  | {
    ok: true;
    nextPath: string;
    updatedGroups: string[];
    updatedHosts: Host[];
    updatedManagedSources: ManagedSource[];
  }
  | { ok: false; error: 'required' | 'invalidChars' | 'duplicatePath' | 'unchanged' };

export function applyGroupPathRename(params: {
  renameTargetPath: string;
  nextName: string;
  customGroups: string[];
  hosts: Host[];
  managedSources: ManagedSource[];
}): GroupPathRenameResult {
  const trimmed = params.nextName.trim();
  if (!trimmed) {
    return { ok: false, error: 'required' };
  }
  if (trimmed.includes('/') || trimmed.includes('\\')) {
    return { ok: false, error: 'invalidChars' };
  }

  const nextPath = computeRenamedGroupPath(params.renameTargetPath, trimmed);
  if (nextPath === params.renameTargetPath) {
    return { ok: false, error: 'unchanged' };
  }
  if (params.customGroups.includes(nextPath)) {
    return { ok: false, error: 'duplicatePath' };
  }

  const { renameTargetPath, customGroups, hosts, managedSources } = params;
  const updatedGroups = customGroups.map((groupPath) => {
    if (groupPath === renameTargetPath) return nextPath;
    if (groupPath.startsWith(`${renameTargetPath}/`)) {
      return nextPath + groupPath.slice(renameTargetPath.length);
    }
    return groupPath;
  });
  const updatedHosts = hosts.map((host) => {
    const group = host.group || '';
    if (group === renameTargetPath) return { ...host, group: nextPath };
    if (group.startsWith(`${renameTargetPath}/`)) {
      return { ...host, group: nextPath + group.slice(renameTargetPath.length) };
    }
    return host;
  });
  const updatedManagedSources = managedSources.map((source) => {
    if (source.groupName === renameTargetPath) return { ...source, groupName: nextPath };
    if (source.groupName.startsWith(`${renameTargetPath}/`)) {
      return { ...source, groupName: nextPath + source.groupName.slice(renameTargetPath.length) };
    }
    return source;
  });

  return {
    ok: true,
    nextPath,
    updatedGroups: Array.from(new Set(updatedGroups)),
    updatedHosts,
    updatedManagedSources,
  };
}
