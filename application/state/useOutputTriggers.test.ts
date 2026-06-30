import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createDeferredOutputTriggerEventProcessor,
  createOutputTriggerScanBuffer,
  findMatchEndingAfter,
  hasApplicableOutputTriggerSnippet,
} from './useOutputTriggers.ts';
import type { Snippet } from '@/domain/models';

test('findMatchEndingAfter skips stale overlap matches and finds current output', () => {
  const text = 'NETCATTY_TRIGGER_PROBE old\r\nprompt# NETCATTY_TRIGGER_PROBE: command not found\r\n';
  const minEndOffset = 'NETCATTY_TRIGGER_PROBE old\r\n'.length;
  const match = findMatchEndingAfter(text, 'NETCATTY_TRIGGER_PROBE', minEndOffset);
  assert.deepEqual(match, {
    value: 'NETCATTY_TRIGGER_PROBE',
    endOffset: 'NETCATTY_TRIGGER_PROBE old\r\nprompt# NETCATTY_TRIGGER_PROBE'.length,
  });
});

test('findMatchEndingAfter supports regex patterns', () => {
  const text = 'old done\r\nservice ready: 200\r\n';
  const minEndOffset = 'old done\r\n'.length;
  const match = findMatchEndingAfter(text, 'ready:\\s+\\d+', minEndOffset);
  assert.deepEqual(match, {
    value: 'ready: 200',
    endOffset: 'old done\r\nservice ready: 200'.length,
  });
});

test('findMatchEndingAfter returns null when matches are only stale', () => {
  const text = 'NETCATTY_TRIGGER_PROBE old\r\nnew output\r\n';
  const minEndOffset = 'NETCATTY_TRIGGER_PROBE old\r\n'.length;
  assert.equal(findMatchEndingAfter(text, 'NETCATTY_TRIGGER_PROBE', minEndOffset), null);
});

test('hasApplicableOutputTriggerSnippet ignores non-output-trigger snippets', () => {
  const snippets: Snippet[] = [
    {
      id: 'plain',
      label: 'Plain',
      command: 'echo hello',
      kind: 'snippet',
    },
    {
      id: 'manual-script',
      label: 'Manual',
      command: '',
      kind: 'script',
      trigger: 'manual',
      triggerPattern: 'READY',
    },
  ];

  assert.equal(hasApplicableOutputTriggerSnippet(snippets, 'host-a'), false);
});

test('hasApplicableOutputTriggerSnippet requires a runnable output trigger for this host', () => {
  const snippets: Snippet[] = [
    {
      id: 'wrong-host',
      label: 'Wrong host',
      command: '',
      kind: 'script',
      trigger: 'onOutput',
      triggerPattern: 'READY',
      targets: ['host-b'],
    },
    {
      id: 'missing-pattern',
      label: 'Missing pattern',
      command: '',
      kind: 'script',
      trigger: 'onOutput',
    },
  ];

  assert.equal(hasApplicableOutputTriggerSnippet(snippets, 'host-a'), false);

  snippets.push({
    id: 'right-host',
    label: 'Right host',
    command: '',
    kind: 'script',
    trigger: 'onOutput',
    triggerPattern: 'READY',
    targets: ['host-a'],
  });

  assert.equal(hasApplicableOutputTriggerSnippet(snippets, 'host-a'), true);
});

test('output trigger scan buffer consumes scanned content and only keeps overlap', () => {
  const buffer = createOutputTriggerScanBuffer(4);

  assert.deepEqual(buffer.append('abcdef'), {
    text: 'abcdef',
    minEndOffset: 0,
    baseOffset: 0,
  });

  assert.deepEqual(buffer.append('gh'), {
    text: 'cdefgh',
    minEndOffset: 4,
    baseOffset: 2,
  });
});

test('output trigger scan buffer scans all new large output instead of only the tail', () => {
  const buffer = createOutputTriggerScanBuffer(4);

  assert.deepEqual(buffer.append('abc'), {
    text: 'abc',
    minEndOffset: 0,
    baseOffset: 0,
  });

  assert.deepEqual(buffer.append('defghijkl'), {
    text: 'abcdefghijkl',
    minEndOffset: 3,
    baseOffset: 0,
  });
});

test('deferred output trigger processor schedules output instead of processing synchronously', () => {
  const processed: string[] = [];
  const scheduled: Array<() => void> = [];
  const processor = createDeferredOutputTriggerEventProcessor({
    processOutput: (chunk) => processed.push(chunk),
    processInput: () => {},
    schedule: (callback) => {
      scheduled.push(callback);
      return () => {};
    },
  });

  processor.enqueueOutput('READY');

  assert.deepEqual(processed, []);
  assert.equal(scheduled.length, 1);

  scheduled.shift()?.();

  assert.deepEqual(processed, ['READY']);
});

test('deferred output trigger processor coalesces output while preserving input order', () => {
  const events: string[] = [];
  const scheduled: Array<() => void> = [];
  const processor = createDeferredOutputTriggerEventProcessor({
    processOutput: (chunk) => events.push(`output:${chunk}`),
    processInput: (data) => events.push(`input:${data}`),
    schedule: (callback) => {
      scheduled.push(callback);
      return () => {};
    },
  });

  processor.enqueueOutput('he');
  processor.enqueueOutput('llo');
  processor.enqueueInput('cmd');
  processor.enqueueOutput('world');

  scheduled.shift()?.();

  assert.deepEqual(events, ['output:hello', 'input:cmd', 'output:world']);
});

test('deferred output trigger processor splits large output and yields between flushes', () => {
  const processed: string[] = [];
  const scheduled: Array<() => void> = [];
  const processor = createDeferredOutputTriggerEventProcessor({
    maxOutputChunkBytes: 4,
    maxOutputBytesPerFlush: 8,
    processOutput: (chunk) => processed.push(chunk),
    processInput: () => {},
    schedule: (callback) => {
      scheduled.push(callback);
      return () => {};
    },
  });

  processor.enqueueOutput('abcdefghijkl');

  scheduled.shift()?.();

  assert.deepEqual(processed, ['abcd', 'efgh']);
  assert.equal(scheduled.length, 1);

  scheduled.shift()?.();

  assert.deepEqual(processed, ['abcd', 'efgh', 'ijkl']);
});
