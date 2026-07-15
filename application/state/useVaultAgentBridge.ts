import { useEffect, useRef } from 'react';
import type { Host, Identity, ManagedSource, PortForwardingRule, Snippet, SSHKey, TerminalSettings, VaultNote } from '../../domain/models';
import {
  handleVaultAgentOp,
  registerVaultAgentHandler,
  setupVaultAgentBridge,
  type VaultAgentApiDeps,
} from '../../infrastructure/ai/vaultAgentBridgeClient';
import {
  clearReferenceKeyPassphrases,
  rememberKeyPassphrase,
  removeDefaultKeyPassphrases,
} from '../defaultKeyPassphrases';

export interface UseVaultAgentBridgeInput {
  hosts: Host[];
  snippets: Snippet[];
  portForwardingRules: PortForwardingRule[];
  keys: SSHKey[];
  identities: Identity[];
  managedSources: ManagedSource[];
  terminalSettings?: Pick<TerminalSettings, 'keepaliveInterval' | 'keepaliveCountMax'>;
  resolveEffectiveHost: (host: Host) => Host;
  updateHosts: (hosts: Host[]) => void;
  updateKeys: (keys: SSHKey[]) => Promise<unknown> | unknown;
  updateSnippets: (snippets: Snippet[]) => void;
  customGroups: string[];
  updateCustomGroups: (groups: string[]) => void;
  notes: VaultNote[];
  updateNotes: (notes: VaultNote[]) => void;
  startTunnel: VaultAgentApiDeps['startTunnel'];
  stopTunnel: VaultAgentApiDeps['stopTunnel'];
  openHost?: VaultAgentApiDeps['openHost'];
}

type VaultAgentSnapshot = {
  hosts: Host[];
  keys: SSHKey[];
  notes: VaultNote[];
  snippets: Snippet[];
  customGroups: string[];
};

export function useVaultAgentBridge(input: UseVaultAgentBridgeInput): void {
  const inputRef = useRef(input);
  inputRef.current = input;

  const vaultSnapshotRef = useRef<VaultAgentSnapshot>({
    hosts: input.hosts,
    keys: input.keys,
    notes: input.notes,
    snippets: input.snippets,
    customGroups: input.customGroups,
  });
  const lastSyncedVaultInputRef = useRef({
    hosts: input.hosts,
    keys: input.keys,
    notes: input.notes,
    snippets: input.snippets,
    customGroups: input.customGroups,
  });

  if (
    input.hosts !== lastSyncedVaultInputRef.current.hosts
    || input.keys !== lastSyncedVaultInputRef.current.keys
    || input.notes !== lastSyncedVaultInputRef.current.notes
    || input.snippets !== lastSyncedVaultInputRef.current.snippets
    || input.customGroups !== lastSyncedVaultInputRef.current.customGroups
  ) {
    vaultSnapshotRef.current = {
      hosts: input.hosts,
      keys: input.keys,
      notes: input.notes,
      snippets: input.snippets,
      customGroups: input.customGroups,
    };
    lastSyncedVaultInputRef.current = {
      hosts: input.hosts,
      keys: input.keys,
      notes: input.notes,
      snippets: input.snippets,
      customGroups: input.customGroups,
    };
  }

  useEffect(() => {
    registerVaultAgentHandler(async (op, params) => {
      const current = inputRef.current;
      return handleVaultAgentOp(op, params, {
        getHosts: () => vaultSnapshotRef.current.hosts,
        getNotes: () => vaultSnapshotRef.current.notes,
        getCustomGroups: () => vaultSnapshotRef.current.customGroups,
        snippets: vaultSnapshotRef.current.snippets,
        portForwardingRules: current.portForwardingRules,
        keys: vaultSnapshotRef.current.keys,
        identities: current.identities,
        managedSources: current.managedSources,
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
        updateHosts: (hosts) => {
          vaultSnapshotRef.current.hosts = hosts;
          current.updateHosts(hosts);
        },
        saveKeyPassphrase: (keyPath, passphrase) => rememberKeyPassphrase({
          keyPath,
          passphrase,
          keys: vaultSnapshotRef.current.keys,
          updateKeys: current.updateKeys,
          setCurrentKeys: (keys) => {
            vaultSnapshotRef.current.keys = keys;
          },
        }),
        removeKeyPassphrases: async (keyPaths) => {
          removeDefaultKeyPassphrases(keyPaths);
          const currentKeys = vaultSnapshotRef.current.keys;
          const updatedKeys = clearReferenceKeyPassphrases(currentKeys, keyPaths);
          if (updatedKeys !== currentKeys) {
            vaultSnapshotRef.current.keys = updatedKeys;
            await current.updateKeys(updatedKeys);
          }
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
