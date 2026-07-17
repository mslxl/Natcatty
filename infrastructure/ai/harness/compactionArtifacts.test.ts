import assert from 'node:assert/strict';
import test from 'node:test';
import { ToolOutputStore } from './toolOutputStore';
import { storeCompactionArchive, storeCompactionArtifact } from './compactionArtifacts';
import { buildCompactionFailureArchiveNotice } from './cattyRuntime';

test('compaction artifacts retain exact searchable history and summary output', () => {
  const store = new ToolOutputStore();
  const archive = storeCompactionArchive(store, 'chat-1', 'exact E_CONN_RESET_7319 evidence');
  const artifact = storeCompactionArtifact(store, 'chat-1', {
    trigger: '413-retry',
    modelId: 'model-1',
    archiveHandleId: archive.id,
    formattedHistory: 'exact E_CONN_RESET_7319 evidence',
    summary: 'network failure found',
  });

  assert.match(store.read({ handleId: archive.id, mode: 'search', query: 'E_CONN_RESET_7319' }, 'chat-1') ?? '', /E_CONN_RESET_7319/);
  assert.match(store.read({ handleId: artifact.id, mode: 'full' }, 'chat-1') ?? '', /network failure found/);
});

test('compaction failure notice keeps the newly created archive discoverable', () => {
  assert.match(
    buildCompactionFailureArchiveNotice('tool-output-archive', false) ?? '',
    /tool-output-archive/,
  );
  assert.equal(buildCompactionFailureArchiveNotice(undefined, false), undefined);
});
