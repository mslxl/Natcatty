import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./useSessionState.ts', import.meta.url), 'utf8');

test('workspace creation preallocates workspace ids outside session updaters', () => {
  const createStart = source.indexOf('const createWorkspaceFromSessions = useCallback');
  const createSetSessions = source.indexOf('setSessions(prevSessions => {', createStart);
  const createWorkspace = source.indexOf('const newWorkspace = createWorkspaceEntity(baseSessionId, joiningSessionId, hint);', createStart);

  assert.notEqual(createStart, -1);
  assert.notEqual(createSetSessions, -1);
  assert.ok(createWorkspace > createStart && createWorkspace < createSetSessions);

  const createBlock = source.slice(createStart, source.indexOf('const addSessionToWorkspace = useCallback', createStart));
  assert.match(
    createBlock,
    /setWorkspaces\(prev => prev\.some\(ws => ws\.id === newWorkspace\.id\) \? prev : \[\.\.\.prev, newWorkspace\]\)/,
  );
});

test('split session workspace creation is idempotent under repeated updater execution', () => {
  const splitStart = source.indexOf('const splitSession = useCallback');
  const splitSetSessions = source.indexOf('setSessions(prevSessions => {', splitStart);
  const newSessionId = source.indexOf('const newSessionId = crypto.randomUUID();', splitStart);
  const standaloneWorkspace = source.indexOf('const standaloneWorkspace = createWorkspaceEntity(sessionId, newSessionId, standaloneHint);', splitStart);

  assert.notEqual(splitStart, -1);
  assert.notEqual(splitSetSessions, -1);
  assert.ok(newSessionId > splitStart && newSessionId < splitSetSessions);
  assert.ok(standaloneWorkspace > splitStart && standaloneWorkspace < splitSetSessions);

  const splitBlock = source.slice(splitStart, source.indexOf('// Toggle workspace view mode', splitStart));
  assert.match(splitBlock, /collectSessionIds\(ws\.root\)\.includes\(newSession\.id\)/);
  assert.match(
    splitBlock,
    /setWorkspaces\(prev => prev\.some\(ws => ws\.id === standaloneWorkspace\.id\) \? prev : \[\.\.\.prev, standaloneWorkspace\]\)/,
  );
});

test('workspace pane insertion paths skip panes that are already present', () => {
  const addStart = source.indexOf('const addSessionToWorkspace = useCallback');
  const appendHostStart = source.indexOf('const appendHostToWorkspace = useCallback');
  const appendLocalStart = source.indexOf('const appendLocalTerminalToWorkspace = useCallback');
  const splitStart = source.indexOf('const splitSession = useCallback');

  assert.notEqual(addStart, -1);
  assert.notEqual(appendHostStart, -1);
  assert.notEqual(appendLocalStart, -1);
  assert.notEqual(splitStart, -1);

  const addBlock = source.slice(addStart, appendHostStart);
  const appendHostBlock = source.slice(appendHostStart, appendLocalStart);
  const appendLocalBlock = source.slice(appendLocalStart, splitStart);

  assert.match(addBlock, /collectSessionIds\(ws\.root\)\.includes\(sessionId\)/);
  assert.match(appendHostBlock, /collectSessionIds\(ws\.root\)\.includes\(newSessionId\)/);
  assert.match(appendLocalBlock, /collectSessionIds\(ws\.root\)\.includes\(newSessionId\)/);
});

test('copy session does not add the same copied tab to tab order twice', () => {
  const copyStart = source.indexOf('const copySession = useCallback');
  const createCloneStart = source.indexOf('const createSessionFromCloneSource = useCallback');

  assert.notEqual(copyStart, -1);
  assert.notEqual(createCloneStart, -1);

  const copyBlock = source.slice(copyStart, createCloneStart);
  assert.match(copyBlock, /if \(prevTabOrder\.includes\(newSessionId\)\) return prevTabOrder;/);
});
