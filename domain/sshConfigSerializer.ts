import { Host } from "./models";

const DEFAULT_SSH_PORT = 22;

export const serializeHostsToSshConfig = (hosts: Host[]): string => {
  const blocks: string[] = [];

  for (const host of hosts) {
    if (host.protocol && host.protocol !== "ssh") continue;

    const lines: string[] = [];
    const alias = host.label || host.hostname;
    lines.push(`Host ${alias}`);

    if (host.hostname !== alias) {
      lines.push(`    HostName ${host.hostname}`);
    }

    if (host.username) {
      lines.push(`    User ${host.username}`);
    }

    if (host.port && host.port !== DEFAULT_SSH_PORT) {
      lines.push(`    Port ${host.port}`);
    }

    if (host.hostChain?.hostIds && host.hostChain.hostIds.length > 0) {
      lines.push(`    # ProxyJump requires manual configuration`);
    }

    blocks.push(lines.join("\n"));
  }

  return blocks.join("\n\n") + "\n";
};

export const mergeWithExistingSshConfig = (
  existingContent: string,
  managedHosts: Host[],
  managedHostnameSet: Set<string>,
): string => {
  const lines = existingContent.split(/\r?\n/);
  const preservedBlocks: string[] = [];
  let currentBlock: string[] = [];
  let currentHostPatterns: string[] = [];
  let isManaged = false;

  const flush = () => {
    if (currentBlock.length > 0) {
      if (!isManaged) {
        preservedBlocks.push(currentBlock.join("\n"));
      }
      currentBlock = [];
      currentHostPatterns = [];
      isManaged = false;
    }
  };

  for (const line of lines) {
    const trimmed = line.replace(/#.*/, "").trim();
    if (!trimmed && currentBlock.length === 0) continue;

    const tokens = trimmed.split(/\s+/).filter(Boolean);
    const keyword = tokens[0]?.toLowerCase();

    if (keyword === "host") {
      flush();
      currentHostPatterns = tokens.slice(1);
      isManaged = currentHostPatterns.some((p) => managedHostnameSet.has(p.toLowerCase()));
      currentBlock.push(line);
    } else if (keyword === "match") {
      flush();
      currentBlock.push(line);
    } else {
      currentBlock.push(line);
    }
  }
  flush();

  const managedContent = serializeHostsToSshConfig(managedHosts);
  const preserved = preservedBlocks.join("\n\n");

  if (preserved.trim()) {
    return preserved + "\n\n" + managedContent;
  }
  return managedContent;
};
