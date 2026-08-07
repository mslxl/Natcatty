import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const appSource = readFileSync(new URL('../../App.tsx', import.meta.url), 'utf8');
const appSideEffectsSource = readFileSync(
  new URL('./AppSideEffects.tsx', import.meta.url),
  'utf8',
);
const indexSource = readFileSync(new URL('../../index.tsx', import.meta.url), 'utf8');
const startupEffectsSource = readFileSync(
  new URL('./useAppStartupEffects.ts', import.meta.url),
  'utf8',
);
const updateCheckSource = readFileSync(
  new URL('../state/useUpdateCheck.ts', import.meta.url),
  'utf8',
);
const portForwardingAutoStartSource = readFileSync(
  new URL('../state/usePortForwardingAutoStart.ts', import.meta.url),
  'utf8',
);

test('every renderer root mounts under StrictMode', () => {
  assert.match(indexSource, /import \{ StrictMode, Suspense, lazy \} from 'react'/);

  const renderCalls = indexSource.match(/root\.render\(/g) ?? [];
  assert.equal(renderCalls.length, 4, 'main, settings, tray and terminal-popup roots');

  let cursor = 0;
  for (let index = 0; index < renderCalls.length; index += 1) {
    const renderAt = indexSource.indexOf('root.render(', cursor);
    assert.notEqual(renderAt, -1);
    const opener = indexSource.slice(renderAt, renderAt + 60);
    assert.match(opener, /root\.render\(\s*<StrictMode>/, `root.render #${index + 1} lacks StrictMode`);
    cursor = renderAt + 'root.render('.length;
  }
});

test('clone-session payload is consumed once even if the effect re-runs', () => {
  const effectStart = appSideEffectsSource.indexOf('consumedNewWindowSessionRef');
  assert.notEqual(effectStart, -1, 'clone-session effect must latch on payload identity');
  const effectEnd = appSideEffectsSource.indexOf(
    '}, [createSessionFromCloneSource, isVaultInitialized, pendingNewWindowSession]);',
    effectStart,
  );
  assert.notEqual(effectEnd, -1);
  const body = appSideEffectsSource.slice(effectStart, effectEnd);

  // A ref comparison is required: clearing the state only lands on the next
  // render, so a re-invoked effect still closes over the same payload.
  assert.match(body, /if \(consumedNewWindowSessionRef\.current === pendingNewWindowSession\) return;/);
  assert.ok(
    body.indexOf('consumedNewWindowSessionRef.current = pending')
      < body.indexOf('createSessionFromCloneSource(pending.sourceSession'),
    'payload must be marked consumed before the clone is created',
  );
});

test('rendererReady is notified once per renderer process', () => {
  assert.match(appSource, /^let rendererReadySent = false;$/m);
  const guardAt = appSource.indexOf('if (!rendererReadySent) {');
  assert.notEqual(guardAt, -1);
  const guarded = appSource.slice(guardAt, guardAt + 200);
  assert.match(guarded, /rendererReadySent = true;/);
  assert.match(guarded, /netcattyBridge\.get\(\)\?\.rendererReady\?\.\(\)/);
  assert.ok(
    guarded.indexOf('rendererReadySent = true;')
      < guarded.indexOf('netcattyBridge.get()?.rendererReady?.()'),
    'the latch must be set before the IPC call so a re-entrant effect is blocked',
  );
});

test('update-available toast latches on the release version', () => {
  const latchAt = startupEffectsSource.indexOf('toastedUpdateVersionRef');
  assert.notEqual(latchAt, -1);
  assert.match(startupEffectsSource, /const toastedUpdateVersionRef = useRef<string \| null>\(null\)/);
  assert.match(startupEffectsSource, /if \(toastedUpdateVersionRef\.current === version\) return;/);

  const guardAt = startupEffectsSource.indexOf('if (toastedUpdateVersionRef.current === version) return;');
  const toastAt = startupEffectsSource.indexOf('toast.info(', guardAt);
  assert.notEqual(toastAt, -1);
  assert.ok(guardAt < toastAt, 'the version latch must gate the toast call');
});

test('port-forward auto-start runs once across a StrictMode double effect', () => {
  assert.match(
    portForwardingAutoStartSource,
    /const autoStartExecutedRef = useRef\(false\);/,
    'the launch auto-start needs a module-render latch, not just an effect dep list',
  );

  const effectStart = portForwardingAutoStartSource.indexOf('if (autoStartExecutedRef.current) return;');
  assert.notEqual(effectStart, -1, 'the effect must bail out when the latch is already set');
  const effectEnd = portForwardingAutoStartSource.indexOf(
    '}, [\n    enabled,\n    isVaultInitialized,\n    runAutoStart,\n  ]);',
    effectStart,
  );
  assert.notEqual(effectEnd, -1, 'auto-start effect dep list moved; update this contract');
  const body = portForwardingAutoStartSource.slice(effectStart, effectEnd);

  // StrictMode invokes the effect twice with the same render's closure, so the
  // latch has to be written before the async run is kicked off — awaiting or
  // deferring the write would let the second invoke start a duplicate tunnel.
  const latchAt = body.indexOf('autoStartExecutedRef.current = true;');
  const runAt = body.indexOf('void runAutoStart();');
  assert.notEqual(latchAt, -1);
  assert.notEqual(runAt, -1);
  assert.ok(latchAt < runAt, 'the latch must be set before runAutoStart() is called');

  // The vault gate must also sit before the latch: latching on a pre-hydration
  // invoke would permanently suppress the real auto-start.
  assert.ok(
    body.indexOf('if (!isVaultInitialized) return;') < latchAt,
    'the vault gate must precede the latch write',
  );
});

test('cancelled startup update check resets its latch instead of skipping forever', () => {
  const scheduleAt = updateCheckSource.indexOf('let checkArmed = true;');
  assert.notEqual(scheduleAt, -1);
  const tail = updateCheckSource.slice(scheduleAt);

  // The latch is only meaningful once the timer actually fires; a cleanup that
  // cancels it beforehand must let the next effect schedule again.
  assert.match(tail, /startupCheckTimeoutRef\.current = setTimeout\(async \(\) => \{\s*\n\s*checkArmed = false;/);
  assert.match(tail, /if \(checkArmed\) \{\s*\n\s*hasCheckedOnStartupRef\.current = false;\s*\n\s*\}/);
});
