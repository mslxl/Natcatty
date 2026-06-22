import type { Host } from "./models";

export interface SshDeepLinkTarget {
  rawUrl: string;
  username?: string;
  hostname: string;
  port?: number;
}

export interface SshDeepLinkDraftOptions {
  id: string;
  now: number;
}

const DEFAULT_SSH_PORT = 22;

const normalizeHostname = (value: string): string =>
  value.trim().replace(/^\[(.*)\]$/, "$1").toLowerCase();

const decodeUrlComponent = (value: string): string => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};

const getHostPort = (host: Host): number => host.port ?? DEFAULT_SSH_PORT;

const isPrimarySshHost = (host: Host): boolean =>
  host.protocol === undefined || host.protocol === "ssh";

export const parseSshDeepLink = (rawUrl: string): SshDeepLinkTarget | null => {
  if (typeof rawUrl !== "string") return null;
  const trimmed = rawUrl.trim();
  if (!trimmed) return null;

  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return null;
  }

  if (parsed.protocol !== "ssh:") return null;

  const hostname = normalizeHostname(parsed.hostname);
  if (!hostname) return null;

  const portText = parsed.port;
  const port = portText ? Number(portText) : undefined;
  if (port !== undefined && (!Number.isInteger(port) || port < 1 || port > 65535)) {
    return null;
  }

  const username = parsed.username
    ? decodeUrlComponent(parsed.username).trim()
    : undefined;

  return {
    rawUrl: trimmed,
    ...(username ? { username } : {}),
    hostname,
    ...(port ? { port } : {}),
  };
};

export const shouldHandleSshDeepLink = (rawUrl: string, enabled: boolean): boolean =>
  enabled && parseSshDeepLink(rawUrl) !== null;

export const findSshDeepLinkHost = (
  hosts: Host[],
  target: SshDeepLinkTarget,
): Host | null => {
  const targetHost = normalizeHostname(target.hostname);
  const targetPort = target.port ?? DEFAULT_SSH_PORT;
  const candidates = hosts.filter((host) => {
    if (!isPrimarySshHost(host)) return false;
    if (normalizeHostname(host.hostname) !== targetHost) return false;
    if (target.username && (host.username || "").trim() !== target.username) return false;
    if (getHostPort(host) !== targetPort) return false;
    return true;
  });

  return candidates.length === 1 ? candidates[0] : null;
};

export const buildSshDeepLinkConnectionHost = (host: Host): Host => ({
  ...host,
  protocol: "ssh",
  moshEnabled: false,
  etEnabled: false,
});

export const buildSshDeepLinkHostDraft = (
  target: SshDeepLinkTarget,
  options: SshDeepLinkDraftOptions,
): Host => ({
  id: options.id,
  label: target.username ? `${target.username}@${target.hostname}` : target.hostname,
  hostname: target.hostname,
  username: target.username || "",
  ...(target.port !== undefined ? { port: target.port } : {}),
  group: "",
  tags: [],
  os: "linux",
  protocol: "ssh",
  createdAt: options.now,
});
