import { Host } from "./models";

const DEFAULT_SSH_PORT = 22;

/**
 * Check if a string is an IPv6 address
 */
const isIPv6 = (hostname: string): boolean => {
  // IPv6 addresses contain colons and may be wrapped in brackets
  return hostname.includes(':') && !hostname.startsWith('[');
};

/**
 * Serialize a single jump host to ProxyJump format
 * Format: [user@]host[:port]
 * @param host - The jump host to serialize
 * @param managedHostIds - Set of host IDs that have Host blocks in the managed config
 */
const serializeJumpHost = (host: Host, managedHostIds: Set<string>): string => {
  let result = "";
  if (host.username) {
    result += `${host.username}@`;
  }

  // Only use label as alias if this jump host is in the managed hosts (has a Host block)
  // and sanitize it by removing spaces. Otherwise use hostname directly.
  let hostPart: string;
  if (managedHostIds.has(host.id) && host.label) {
    // Use sanitized label (same as the Host block alias)
    hostPart = host.label.replace(/\s/g, '') || host.hostname;
  } else {
    // Jump host is outside managed config, use hostname directly
    hostPart = host.hostname;
  }

  // For IPv6 addresses with non-default port, wrap in brackets
  if (host.port && host.port !== DEFAULT_SSH_PORT && isIPv6(hostPart)) {
    result += `[${hostPart}]:${host.port}`;
  } else {
    result += hostPart;
    if (host.port && host.port !== DEFAULT_SSH_PORT) {
      result += `:${host.port}`;
    }
  }

  return result;
};

/**
 * Build ProxyJump directive from hostChain
 * @param host - The host with hostChain
 * @param allHosts - All hosts to look up jump host details
 * @param managedHostIds - Set of host IDs that have Host blocks in the managed config
 * @returns ProxyJump value string or null if chain is empty/invalid
 */
const buildProxyJumpValue = (
  host: Host,
  allHosts: Host[],
  managedHostIds: Set<string>,
): string | null => {
  if (!host.hostChain?.hostIds || host.hostChain.hostIds.length === 0) {
    return null;
  }

  const hostMap = new Map(allHosts.map(h => [h.id, h]));
  const jumpParts: string[] = [];

  for (const jumpHostId of host.hostChain.hostIds) {
    const jumpHost = hostMap.get(jumpHostId);
    if (jumpHost) {
      jumpParts.push(serializeJumpHost(jumpHost, managedHostIds));
    }
  }

  return jumpParts.length > 0 ? jumpParts.join(",") : null;
};

export const serializeHostsToSshConfig = (hosts: Host[], allHosts?: Host[]): string => {
  const blocks: string[] = [];
  // Use provided allHosts for jump host lookup, or fall back to hosts array
  const hostsForLookup = allHosts || hosts;

  // Build set of managed host IDs (SSH hosts that will have Host blocks)
  const managedHostIds = new Set(
    hosts
      .filter(h => !h.protocol || h.protocol === "ssh")
      .map(h => h.id)
  );

  for (const host of hosts) {
    if (host.protocol && host.protocol !== "ssh") continue;

    const lines: string[] = [];
    // Sanitize alias by removing spaces (SSH config doesn't allow spaces in Host patterns)
    const alias = (host.label?.replace(/\s/g, '') || host.hostname);
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

    // Serialize ProxyJump if host has a chain
    const proxyJumpValue = buildProxyJumpValue(host, hostsForLookup, managedHostIds);
    if (proxyJumpValue) {
      lines.push(`    ProxyJump ${proxyJumpValue}`);
    }

    blocks.push(lines.join("\n"));
  }

  return blocks.join("\n\n") + "\n";
};

export const mergeWithExistingSshConfig = (
  existingContent: string,
  managedHosts: Host[],
  managedHostnameSet: Set<string>,
  allHosts?: Host[],
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

  const managedContent = serializeHostsToSshConfig(managedHosts, allHosts);
  const preserved = preservedBlocks.join("\n\n");

  if (preserved.trim()) {
    return preserved + "\n\n" + managedContent;
  }
  return managedContent;
};
