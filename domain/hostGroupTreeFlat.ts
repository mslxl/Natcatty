import type { GroupNode, Host } from '../types';

export type HostTreeFlatRow =
  | { kind: 'group'; node: GroupNode; depth: number }
  | { kind: 'host'; host: Host; depth: number };

export function hostTreeFlatRowKey(row: HostTreeFlatRow): string {
  return row.kind === 'group' ? `g:${row.node.path}` : `h:${row.host.id}`;
}

export function flattenHostGroupTree(params: {
  groupNodes: GroupNode[];
  ungroupedHosts: Host[];
  expandedPaths: Set<string>;
  searchActive: boolean;
}): HostTreeFlatRow[] {
  const rows: HostTreeFlatRow[] = [];

  const walkGroup = (node: GroupNode, depth: number) => {
    rows.push({ kind: 'group', node, depth });
    const isExpanded = params.searchActive || params.expandedPaths.has(node.path);
    if (!isExpanded) return;

    const sortedHosts = [...node.hosts].sort((a, b) => a.label.localeCompare(b.label));
    for (const host of sortedHosts) {
      rows.push({ kind: 'host', host, depth: depth + 1 });
    }

    const childNodes = (Object.values(node.children) as GroupNode[])
      .sort((a, b) => a.name.localeCompare(b.name));
    for (const child of childNodes) {
      walkGroup(child, depth + 1);
    }
  };

  for (const node of params.groupNodes) {
    walkGroup(node, 0);
  }

  const sortedUngrouped = [...params.ungroupedHosts].sort((a, b) => a.label.localeCompare(b.label));
  for (const host of sortedUngrouped) {
    rows.push({ kind: 'host', host, depth: 0 });
  }

  return rows;
}

export function hostTreeFlatRowContainsHost(row: HostTreeFlatRow, hostId: string | null | undefined): boolean {
  if (!hostId) return false;
  if (row.kind === 'host') return row.host.id === hostId;
  return row.node.hosts.some((host) => host.id === hostId)
    || Object.values(row.node.children).some((child) => {
      const childRow: HostTreeFlatRow = { kind: 'group', node: child as GroupNode, depth: 0 };
      return hostTreeFlatRowContainsHost(childRow, hostId);
    });
}
