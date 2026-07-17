import assert from 'node:assert/strict';
import test from 'node:test';
import { TerminalMonitorGuard } from './terminalMonitorGuard';
import { applyMonitorStopResult } from './capabilityTools';

test('TerminalMonitorGuard bounds lines and batches', () => {
  const guard = new TerminalMonitorGuard();
  const result = guard.process('chat:job', `${'x'.repeat(800)}\n${'line\n'.repeat(1_000)}`);

  assert.equal(result.action, 'deliver');
  assert.ok((result.content?.length ?? Infinity) <= 3_000);
  assert.ok((result.content?.split('\n')[0].length ?? Infinity) <= 500);
});

test('TerminalMonitorGuard suppresses bursts and stops a sustained overload', () => {
  let now = 0;
  const guard = new TerminalMonitorGuard({ now: () => now });
  for (let index = 0; index < 10; index += 1) {
    assert.equal(guard.process('chat:job', `line ${index}`).action, 'deliver');
  }
  assert.equal(guard.process('chat:job', 'burst').action, 'suppress');
  let action = guard.process('chat:job', 'still flooding').action;
  for (now = 1_000; now <= 31_000 && action !== 'stop'; now += 1_000) {
    action = guard.process('chat:job', 'still flooding').action;
  }
  assert.equal(action, 'stop');
});

test('monitor stop result does not claim success when the backend stop failed', () => {
  const failed = applyMonitorStopResult(
    { jobId: 'job-1', status: 'running' },
    { ok: false, error: 'lost worker' },
    12,
  );
  assert.equal(failed.status, 'running');
  assert.match(String(failed.output), /stop failed/);
  assert.match(String(failed.output), /may still be running/);

  const accepted = applyMonitorStopResult(
    { jobId: 'job-1', status: 'running' },
    { ok: true },
    12,
  );
  assert.equal(accepted.status, 'stopping');
  assert.match(String(accepted.output), /stop requested/);
});
