import test from 'node:test';
import assert from 'node:assert/strict';

import {
  parseTerminalFontSizeRecord,
  serializeTerminalFontSizeRecord,
  shouldApplyTerminalFontSizeRecord,
  shouldBroadcastTerminalFontSizeChange,
  type TerminalFontSizeMutationSource,
  type TerminalFontSizeRecord,
} from './terminalFontSizeSync.ts';

/**
 * Minimal model of the settings ↔ main font-size sync loop that caused #2689.
 * Models both IPC rebroadcast and stale localStorage overwrites.
 */
function simulateFontSizeClicks(options: {
  shouldBroadcast: (
    source: TerminalFontSizeMutationSource,
    persistMounted: boolean,
  ) => { shouldBroadcast: boolean; nextSource: TerminalFontSizeMutationSource };
  shouldApply: (current: TerminalFontSizeRecord, incoming: TerminalFontSizeRecord) => boolean;
  versioned: boolean;
}): { settingsValues: number[]; mainValues: number[]; storageWrites: string[] } {
  let settings: TerminalFontSizeRecord = { fontSize: 16, version: 0 };
  let main: TerminalFontSizeRecord = { fontSize: 16, version: 0 };
  let settingsSource: TerminalFontSizeMutationSource = 'local';
  let mainSource: TerminalFontSizeMutationSource = 'local';
  let storage = options.versioned
    ? serializeTerminalFontSizeRecord(settings)
    : String(settings.fontSize);
  const settingsValues: number[] = [];
  const mainValues: number[] = [];
  const storageWrites: string[] = [];

  const pendingIpc: Array<{ to: 'settings' | 'main'; record: TerminalFontSizeRecord }> = [];

  const writeStorage = (record: TerminalFontSizeRecord) => {
    const next = options.versioned
      ? serializeTerminalFontSizeRecord(record)
      : String(record.fontSize);
    if (next === storage) return;
    storage = next;
    storageWrites.push(next);
  };

  const applyLocal = (window: 'settings' | 'main', fontSize: number) => {
    const bump = (prev: TerminalFontSizeRecord): TerminalFontSizeRecord => (
      options.versioned
        ? { fontSize, version: prev.version + 1 }
        : { fontSize, version: 0 }
    );

    if (window === 'settings') {
      settingsSource = 'local';
      settings = bump(settings);
      settingsValues.push(settings.fontSize);
      writeStorage(settings);
      const decision = options.shouldBroadcast(settingsSource, true);
      settingsSource = decision.nextSource;
      if (decision.shouldBroadcast) pendingIpc.push({ to: 'main', record: { ...settings } });
      return;
    }

    mainSource = 'local';
    main = bump(main);
    mainValues.push(main.fontSize);
    writeStorage(main);
    const decision = options.shouldBroadcast(mainSource, true);
    mainSource = decision.nextSource;
    if (decision.shouldBroadcast) pendingIpc.push({ to: 'settings', record: { ...main } });
  };

  const applyIncoming = (window: 'settings' | 'main', record: TerminalFontSizeRecord) => {
    if (window === 'settings') {
      if (!options.shouldApply(settings, record)) return;
      settingsSource = 'incoming';
      settings = { ...record };
      settingsValues.push(settings.fontSize);
      writeStorage(settings);
      const decision = options.shouldBroadcast(settingsSource, true);
      settingsSource = decision.nextSource;
      if (decision.shouldBroadcast) pendingIpc.push({ to: 'main', record: { ...settings } });
      return;
    }

    if (!options.shouldApply(main, record)) return;
    mainSource = 'incoming';
    main = { ...record };
    mainValues.push(main.fontSize);
    writeStorage(main);
    const decision = options.shouldBroadcast(mainSource, true);
    mainSource = decision.nextSource;
    if (decision.shouldBroadcast) pendingIpc.push({ to: 'settings', record: { ...main } });
  };

  const flushIpc = () => {
    while (pendingIpc.length > 0) {
      const next = pendingIpc.shift()!;
      applyIncoming(next.to, next.record);
      if (settingsValues.length > 40) break;
    }
  };

  const deliverStorageTo = (window: 'settings' | 'main') => {
    const record = parseTerminalFontSizeRecord(storage);
    applyIncoming(window, record);
  };

  // Match reporter steps: + (16→17) then - while a delayed peer echo of 17
  // can still race with a later local 15.
  applyLocal('settings', 17);
  deliverStorageTo('main');
  applyLocal('settings', 15);
  const delayed = pendingIpc.shift();
  flushIpc();
  if (delayed) applyIncoming(delayed.to, delayed.record);
  deliverStorageTo('settings');
  flushIpc();

  return { settingsValues, mainValues, storageWrites };
}

test('parseTerminalFontSizeRecord accepts legacy plain numbers and versioned JSON', () => {
  assert.deepEqual(parseTerminalFontSizeRecord('16'), { fontSize: 16, version: 0 });
  assert.deepEqual(parseTerminalFontSizeRecord(14), { fontSize: 14, version: 0 });
  assert.deepEqual(
    parseTerminalFontSizeRecord({ fontSize: 18, version: 3 }),
    { fontSize: 18, version: 3 },
  );
  assert.deepEqual(
    parseTerminalFontSizeRecord('{"fontSize":15,"version":9}'),
    { fontSize: 15, version: 9 },
  );
  assert.equal(parseTerminalFontSizeRecord('bad').fontSize, 14);
});

test('shouldApplyTerminalFontSizeRecord ignores stale revisions', () => {
  const current = { fontSize: 15, version: 2 };
  assert.equal(shouldApplyTerminalFontSizeRecord(current, { fontSize: 17, version: 1 }), false);
  assert.equal(shouldApplyTerminalFontSizeRecord(current, { fontSize: 18, version: 3 }), true);
  assert.equal(shouldApplyTerminalFontSizeRecord(current, { fontSize: 15, version: 2 }), false);
});

test('shouldBroadcastTerminalFontSizeChange suppresses incoming rebroadcasts', () => {
  assert.deepEqual(
    shouldBroadcastTerminalFontSizeChange('incoming', true),
    { shouldBroadcast: false, nextSource: 'local' },
  );
  assert.deepEqual(
    shouldBroadcastTerminalFontSizeChange('local', true),
    { shouldBroadcast: true, nextSource: 'local' },
  );
  assert.deepEqual(
    shouldBroadcastTerminalFontSizeChange('local', false),
    { shouldBroadcast: false, nextSource: 'local' },
  );
});

test('legacy unversioned always-broadcast font size sync oscillates during rapid +/- clicks', () => {
  const alwaysBroadcast = (
    _source: TerminalFontSizeMutationSource,
    persistMounted: boolean,
  ) => ({
    shouldBroadcast: persistMounted,
    nextSource: 'local' as const,
  });
  const alwaysApply = () => true;

  const { settingsValues } = simulateFontSizeClicks({
    shouldBroadcast: alwaysBroadcast,
    shouldApply: alwaysApply,
    versioned: false,
  });
  const unique = new Set(settingsValues);
  assert.ok(
    unique.has(17) && unique.has(15) && settingsValues.length > 2,
    `expected oscillation between 17 and 15, got ${settingsValues.join(',')}`,
  );
});

test('versioned font size sync ignores stale peer echoes during rapid +/- clicks', () => {
  const { settingsValues, mainValues } = simulateFontSizeClicks({
    shouldBroadcast: shouldBroadcastTerminalFontSizeChange,
    shouldApply: shouldApplyTerminalFontSizeRecord,
    versioned: true,
  });

  assert.deepEqual(settingsValues, [17, 15]);
  assert.ok(mainValues.includes(15));
  assert.equal(mainValues.includes(17) && mainValues[mainValues.length - 1] === 17, false);
});

test('serializeTerminalFontSizeRecord round-trips through parse', () => {
  const raw = serializeTerminalFontSizeRecord({ fontSize: 18, version: 9 });
  assert.deepEqual(parseTerminalFontSizeRecord(JSON.parse(raw)), { fontSize: 18, version: 9 });
});
