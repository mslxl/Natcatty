import { useEffect, useRef } from 'react';
import type { GroupConfig, Host, Identity, ManagedSource, PortForwardingRule, ProxyProfile, Snippet, SSHKey, TerminalSettings, VaultNote } from '../../domain/models';
import {
  handleVaultAgentOp,
  registerVaultAgentHandler,
  setupVaultAgentBridge,
  type VaultAgentApiDeps,
} from '../../infrastructure/ai/vaultAgentBridgeClient';

export interface UseVaultAgentBridgeInput {
  hosts: Host[];
  snippets: Snippet[];
  portForwardingRules: PortForwardingRule[];
  keys: SSHKey[];
  identities: Identity[];
  proxyProfiles: ProxyProfile[];
  managedSources: ManagedSource[];
  terminalSettings?: Pick<TerminalSettings, 'keepaliveInterval' | 'keepaliveCountMax'>;
  resolveEffectiveHost: (host: Host) => Host;
  updateHosts: (hosts: Host[]) => void;
  updateSnippets: (snippets: Snippet[]) => void;
  customGroups: string[];
  updateCustomGroups: (groups: string[]) => void;
  groupConfigs: GroupConfig[];
  updateGroupConfigs: (configs: GroupConfig[]) => void;
  updateManagedSources: (sources: ManagedSource[]) => void;
  updatePortForwardingRules: (rules: PortForwardingRule[]) => void;
  notes: VaultNote[];
  updateNotes: (notes: VaultNote[]) => void;
  startTunnel: VaultAgentApiDeps['startTunnel'];
  stopTunnel: VaultAgentApiDeps['stopTunnel'];
  openHost?: VaultAgentApiDeps['openHost'];
}

type VaultAgentSnapshot = {
  hosts: Host[];
  notes: VaultNote[];
  snippets: Snippet[];
  customGroups: string[];
  groupConfigs: GroupConfig[];
  portForwardingRules: PortForwardingRule[];
  managedSources: ManagedSource[];
};

const selectVaultAgentSnapshot = (input: UseVaultAgentBridgeInput): VaultAgentSnapshot => ({
  hosts: input.hosts,
  notes: input.notes,
  snippets: input.snippets,
  customGroups: input.customGroups,
  groupConfigs: input.groupConfigs,
  portForwardingRules: input.portForwardingRules,
  managedSources: input.managedSources,
});

export const haveSameVaultAgentSnapshot = (
  left: VaultAgentSnapshot,
  right: VaultAgentSnapshot,
): boolean => (Object.keys(left) as Array<keyof VaultAgentSnapshot>)
  .every((key) => left[key] === right[key]);

export function useVaultAgentBridge(input: UseVaultAgentBridgeInput): void {
  const inputRef = useRef(input);
  inputRef.current = input;

  const selectedSnapshot = selectVaultAgentSnapshot(input);
  const vaultSnapshotRef = useRef<VaultAgentSnapshot>(selectedSnapshot);
  const lastSyncedVaultInputRef = useRef<VaultAgentSnapshot>(selectedSnapshot);

  if (!haveSameVaultAgentSnapshot(selectedSnapshot, lastSyncedVaultInputRef.current)) {
    vaultSnapshotRef.current = selectedSnapshot;
    lastSyncedVaultInputRef.current = selectedSnapshot;
  }

  useEffect(() => {
    registerVaultAgentHandler(async (op, params) => {
      const current = inputRef.current;
      return handleVaultAgentOp(op, params, {
        getHosts: () => vaultSnapshotRef.current.hosts,
        getNotes: () => vaultSnapshotRef.current.notes,
        getCustomGroups: () => vaultSnapshotRef.current.customGroups,
        getGroupConfigs: () => vaultSnapshotRef.current.groupConfigs,
        getPortForwardingRules: () => vaultSnapshotRef.current.portForwardingRules,
        getManagedSources: () => vaultSnapshotRef.current.managedSources,
        snippets: vaultSnapshotRef.current.snippets,
        keys: current.keys,
        identities: current.identities,
        proxyProfiles: current.proxyProfiles,
        terminalSettings: current.terminalSettings,
        resolveEffectiveHost: current.resolveEffectiveHost,
        updateHostNotes: (hostId, notes) => {
          const nextHosts = vaultSnapshotRef.current.hosts.map((host) => (
            host.id === hostId ? { ...host, notes } : host
          ));
          vaultSnapshotRef.current.hosts = nextHosts;
          current.updateHosts(nextHosts);
        },
        updateCustomGroups: (groups) => {
          vaultSnapshotRef.current.customGroups = groups;
          current.updateCustomGroups(groups);
        },
        updateGroupConfigs: (configs) => {
          vaultSnapshotRef.current.groupConfigs = configs;
          current.updateGroupConfigs(configs);
        },
        updatePortForwardingRules: (rules) => {
          vaultSnapshotRef.current.portForwardingRules = rules;
          current.updatePortForwardingRules(rules);
        },
        updateManagedSources: (sources) => {
          vaultSnapshotRef.current.managedSources = sources;
          current.updateManagedSources(sources);
        },
        updateHosts: (hosts) => {
          vaultSnapshotRef.current.hosts = hosts;
          current.updateHosts(hosts);
        },
        updateNotes: (notes) => {
          vaultSnapshotRef.current.notes = notes;
          current.updateNotes(notes);
        },
        updateSnippets: (nextSnippets) => {
          vaultSnapshotRef.current.snippets = nextSnippets;
          current.updateSnippets(nextSnippets);
        },
        startTunnel: current.startTunnel,
        stopTunnel: current.stopTunnel,
        openHost: current.openHost
          ? (hostId) => current.openHost!(hostId)
          : undefined,
      });
    });
    return setupVaultAgentBridge();
  }, []);
}
