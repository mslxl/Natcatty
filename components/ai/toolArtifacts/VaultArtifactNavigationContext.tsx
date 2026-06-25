import React, { createContext, useCallback, useContext, useMemo } from 'react';
import type { Host, VaultNote } from '../../../types';
import { useI18n } from '../../../application/i18n/I18nProvider';
import { toast } from '../../ui/toast';
import type { VaultToolArtifact } from './vaultToolArtifact';

export interface VaultArtifactNavigationActions {
  openVaultNote?: (noteId: string) => void;
  openVaultHost?: (hostId: string) => void;
  openVaultSection?: (section: 'notes' | 'hosts') => void;
}

interface CreateVaultArtifactNavigationActionsOptions {
  notes: VaultNote[];
  hosts: Host[];
  t: (key: string) => string;
  onOpenVaultNote?: (noteId: string) => void;
  onOpenVaultHost?: (hostId: string) => void;
  onOpenVaultSection?: (section: 'notes' | 'hosts') => void;
  onUnavailable: (message: string, title: string) => void;
}

interface VaultArtifactNavigationProviderProps {
  notes: VaultNote[];
  hosts: Host[];
  onOpenVaultNote?: (noteId: string) => void;
  onOpenVaultHost?: (hostId: string) => void;
  onOpenVaultSection?: (section: 'notes' | 'hosts') => void;
  children: React.ReactNode;
}

const VaultArtifactNavigationContext = createContext<VaultArtifactNavigationActions | null>(null);

export function createVaultArtifactNavigationActions({
  notes,
  hosts,
  t,
  onOpenVaultNote,
  onOpenVaultHost,
  onOpenVaultSection,
  onUnavailable,
}: CreateVaultArtifactNavigationActionsOptions): VaultArtifactNavigationActions {
  const actions: VaultArtifactNavigationActions = {};

  if (onOpenVaultNote) {
    actions.openVaultNote = (noteId: string) => {
      const exists = notes.some((note) => note.id === noteId);
      if (!exists) {
        onUnavailable(t('ai.chat.artifact.noteMissing'), t('ai.chat.artifact.unavailableTitle'));
        return;
      }
      onOpenVaultNote(noteId);
    };
  }

  if (onOpenVaultHost) {
    actions.openVaultHost = (hostId: string) => {
      const exists = hosts.some((host) => host.id === hostId);
      if (!exists) {
        onUnavailable(t('ai.chat.artifact.hostMissing'), t('ai.chat.artifact.unavailableTitle'));
        return;
      }
      onOpenVaultHost(hostId);
    };
  }

  if (onOpenVaultSection) {
    actions.openVaultSection = onOpenVaultSection;
  }

  return actions;
}

export function VaultArtifactNavigationProvider({
  notes,
  hosts,
  onOpenVaultNote,
  onOpenVaultHost,
  onOpenVaultSection,
  children,
}: VaultArtifactNavigationProviderProps) {
  const { t } = useI18n();

  const onUnavailable = useCallback((message: string, title: string) => {
    toast.warning(message, title);
  }, []);

  const value = useMemo<VaultArtifactNavigationActions>(() => createVaultArtifactNavigationActions({
    notes,
    hosts,
    t,
    onOpenVaultNote,
    onOpenVaultHost,
    onOpenVaultSection,
    onUnavailable,
  }), [hosts, notes, onOpenVaultHost, onOpenVaultNote, onOpenVaultSection, onUnavailable, t]);

  return (
    <VaultArtifactNavigationContext.Provider value={value}>
      {children}
    </VaultArtifactNavigationContext.Provider>
  );
}

export function useVaultArtifactNavigation(): VaultArtifactNavigationActions | null {
  return useContext(VaultArtifactNavigationContext);
}

export function navigateVaultArtifact(
  artifact: VaultToolArtifact,
  navigation: VaultArtifactNavigationActions,
): void {
  switch (artifact.kind) {
    case 'vault.note':
      navigation.openVaultNote?.(artifact.noteId);
      break;
    case 'vault.host':
      navigation.openVaultHost?.(artifact.hostId);
      break;
    case 'vault.hosts.batch':
      navigation.openVaultSection?.('hosts');
      break;
    case 'vault.summary':
      navigation.openVaultSection?.(artifact.section);
      break;
    default:
      break;
  }
}

export function canNavigateVaultArtifact(
  artifact: VaultToolArtifact,
  navigation: VaultArtifactNavigationActions | null,
): boolean {
  if (!navigation) return false;
  switch (artifact.kind) {
    case 'vault.note':
      return Boolean(navigation.openVaultNote);
    case 'vault.host':
      return Boolean(navigation.openVaultHost);
    case 'vault.hosts.batch':
    case 'vault.summary':
      return Boolean(navigation.openVaultSection);
    default:
      return false;
  }
}
