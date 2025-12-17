export interface QuickConnectTarget {
  hostname: string;
  username?: string;
  port?: number;
}

// Parse user@host:port format
export function parseQuickConnectInput(
  input: string,
): QuickConnectTarget | null {
  const trimmed = input.trim();
  if (!trimmed) return null;

  // Pattern: [user@]hostname[:port]
  // Hostname can be IP (v4 or v6) or domain name
  const regex = /^(?:([^@]+)@)?([^\s:]+|\[[^\]]+\])(?::(\d+))?$/;
  const match = trimmed.match(regex);
  if (!match) return null;

  const [, username, hostname, portStr] = match;

  // Validate hostname looks like an IP or domain
  const ipv4Regex = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
  const ipv6Regex = /^\[?[a-fA-F0-9:]+\]?$/;
  const domainRegex =
    /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$/;

  if (
    !ipv4Regex.test(hostname) &&
    !ipv6Regex.test(hostname) &&
    !domainRegex.test(hostname)
  ) {
    return null;
  }

  const port = portStr ? parseInt(portStr, 10) : undefined;
  if (port !== undefined && (isNaN(port) || port < 1 || port > 65535)) {
    return null;
  }

  return {
    hostname: hostname.replace(/^\[|\]$/g, ""), // Remove IPv6 brackets
    username: username || undefined,
    port,
  };
}

// Check if input looks like a quick connect address
export function isQuickConnectInput(input: string): boolean {
  return parseQuickConnectInput(input) !== null;
}

