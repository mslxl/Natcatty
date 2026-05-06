import type { GroupConfig, Host, ProxyConfig, ProxyProfile } from "./models";

const cloneProxyConfig = (config: ProxyConfig): ProxyConfig => ({
  ...config,
});

export function findProxyProfile(
  proxyProfileId: string | undefined,
  proxyProfiles: ProxyProfile[],
): ProxyProfile | undefined {
  if (!proxyProfileId) return undefined;
  return proxyProfiles.find((profile) => profile.id === proxyProfileId);
}

export function materializeHostProxyProfile<T extends Host>(
  host: T,
  proxyProfiles: ProxyProfile[],
): T {
  if (host.proxyConfig || !host.proxyProfileId) return host;
  const profile = findProxyProfile(host.proxyProfileId, proxyProfiles);
  if (!profile) return host;
  return {
    ...host,
    proxyConfig: cloneProxyConfig(profile.config),
  };
}

const clearProxyProfileId = <T extends { proxyProfileId?: string }>(
  item: T,
  proxyProfileId: string,
): T => {
  if (item.proxyProfileId !== proxyProfileId) return item;
  const { proxyProfileId: _proxyProfileId, ...rest } = item;
  return rest as T;
};

export function removeProxyProfileReferences(
  proxyProfileId: string,
  data: {
    hosts: Host[];
    groupConfigs: GroupConfig[];
  },
): {
  hosts: Host[];
  groupConfigs: GroupConfig[];
} {
  return {
    hosts: data.hosts.map((host) => clearProxyProfileId(host, proxyProfileId)),
    groupConfigs: data.groupConfigs.map((config) => clearProxyProfileId(config, proxyProfileId)),
  };
}
