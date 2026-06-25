import assert from 'node:assert/strict';
import test from 'node:test';

import {
  createVaultArtifactNavigationActions,
  navigateVaultArtifact,
} from './VaultArtifactNavigationContext.tsx';

const t = (key: string) => key;

test('clicking an existing note artifact opens that note', () => {
  const openedNotes: string[] = [];
  const unavailable: Array<{ title: string; message: string }> = [];
  const navigation = createVaultArtifactNavigationActions({
    notes: [{ id: 'note-1', title: 'Runbook', content: '', createdAt: 1, updatedAt: 1 }],
    hosts: [],
    t,
    onOpenVaultNote: (noteId) => openedNotes.push(noteId),
    onOpenVaultHost: () => {},
    onOpenVaultSection: () => {},
    onUnavailable: (message, title) => unavailable.push({ title, message }),
  });

  navigateVaultArtifact({
    kind: 'vault.note',
    noteId: 'note-1',
    title: 'Runbook',
  }, navigation);

  assert.deepEqual(openedNotes, ['note-1']);
  assert.deepEqual(unavailable, []);
});

test('clicking a missing note artifact shows an unavailable message instead of opening', () => {
  const openedNotes: string[] = [];
  const unavailable: Array<{ title: string; message: string }> = [];
  const navigation = createVaultArtifactNavigationActions({
    notes: [],
    hosts: [],
    t,
    onOpenVaultNote: (noteId) => openedNotes.push(noteId),
    onOpenVaultHost: () => {},
    onOpenVaultSection: () => {},
    onUnavailable: (message, title) => unavailable.push({ title, message }),
  });

  navigateVaultArtifact({
    kind: 'vault.note',
    noteId: 'deleted-note',
    title: 'Deleted note',
  }, navigation);

  assert.deepEqual(openedNotes, []);
  assert.deepEqual(unavailable, [{
    title: 'ai.chat.artifact.unavailableTitle',
    message: 'ai.chat.artifact.noteMissing',
  }]);
});
