import type { DropEntry } from "./sftpFileUtils";
import { getPathForFile } from "./sftpFileUtils";
import type { Host } from "../types";

export type ZmodemDragDropFile = {
  path?: string;
  name: string;
  remoteName: string;
  data?: ArrayBuffer;
};

export function supportsZmodemTerminalDragDrop(
  host: Host,
  isNetworkDevice = false,
): boolean {
  if (host.protocol === "local" || isNetworkDevice) return false;
  if (host.moshEnabled || host.etEnabled) return true;
  return (
    host.protocol === "ssh" ||
    host.protocol === "telnet" ||
    host.protocol === "mosh" ||
    host.protocol === "et" ||
    host.protocol === "serial"
  );
}

export function getZmodemRemoteName(relativePath: string, fallbackName: string): string {
  const normalized = relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  if (!normalized) return fallbackName;
  const segments = normalized.split("/").filter(Boolean);
  return segments[segments.length - 1] || fallbackName;
}

export async function buildZmodemDragDropFiles(
  dropEntries: DropEntry[],
): Promise<ZmodemDragDropFile[]> {
  const files: ZmodemDragDropFile[] = [];

  for (const entry of dropEntries) {
    if (entry.isDirectory || !entry.file) continue;

    const remoteName = getZmodemRemoteName(entry.relativePath, entry.file.name);
    const localPath = getPathForFile(entry.file);

    if (localPath) {
      files.push({
        path: localPath,
        name: entry.file.name,
        remoteName,
      });
      continue;
    }

    const data = await entry.file.arrayBuffer();
    files.push({
      name: entry.file.name,
      remoteName,
      data,
    });
  }

  return files;
}
