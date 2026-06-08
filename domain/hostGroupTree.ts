import type { GroupNode, Host } from '../types';

function countAllHostsInNode(node: GroupNode): number {
  let count = node.hosts.length;
  for (const child of Object.values(node.children)) {
    count += countAllHostsInNode(child);
  }
  node.totalHostCount = count;
  return count;
}

export function buildHostGroupTree(
  hosts: Host[],
  customGroups: string[],
): { groupTree: GroupNode[]; ungroupedHosts: Host[] } {
  const root: Record<string, GroupNode> = {};
  const insertPath = (path: string, host?: Host) => {
    const parts = path.split('/').filter(Boolean);
    let currentLevel = root;
    let currentPath = '';
    parts.forEach((part, index) => {
      currentPath = currentPath ? `${currentPath}/${part}` : part;
      if (!currentLevel[part]) {
        currentLevel[part] = {
          name: part,
          path: currentPath,
          children: {},
          hosts: [],
        };
      }
      if (host && index === parts.length - 1) {
        currentLevel[part].hosts.push(host);
      }
      currentLevel = currentLevel[part].children;
    });
  };

  customGroups.forEach((path) => insertPath(path));
  const ungroupedHosts: Host[] = [];
  for (const host of hosts) {
    const group = host.group?.trim();
    if (group) {
      insertPath(group, host);
    } else {
      ungroupedHosts.push(host);
    }
  }

  Object.values(root).forEach(countAllHostsInNode);
  const groupTree = Object.values(root).sort((a, b) => a.name.localeCompare(b.name));
  ungroupedHosts.sort((a, b) => a.label.localeCompare(b.label));
  return { groupTree, ungroupedHosts };
}

export function groupNodeContainsHost(node: GroupNode, hostId: string | null | undefined): boolean {
  if (!hostId) return false;
  if (node.hosts.some((host) => host.id === hostId)) return true;
  return Object.values(node.children).some((child) => groupNodeContainsHost(child, hostId));
}

export function collectGroupTreePaths(nodes: GroupNode[]): string[] {
  const paths: string[] = [];
  const walk = (node: GroupNode) => {
    if (node.path) paths.push(node.path);
    for (const child of Object.values(node.children)) {
      walk(child);
    }
  };
  for (const node of nodes) walk(node);
  return paths;
}
