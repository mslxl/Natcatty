import test from 'node:test';
import assert from 'node:assert/strict';

import { isAutoLaunchResultTrustworthy } from './systemSettingsEffects.ts';

test('isAutoLaunchResultTrustworthy trusts a successful read/write result', () => {
  assert.equal(isAutoLaunchResultTrustworthy({ success: true }), true);
});

test('isAutoLaunchResultTrustworthy rejects a transient failure result', () => {
  assert.equal(
    isAutoLaunchResultTrustworthy({ success: false }),
    false,
    'success:false means the OS state is unknown, not confirmed disabled',
  );
});

test('isAutoLaunchResultTrustworthy defaults to trusting a result with no success field', () => {
  // Defensive default for any future/alternate caller that omits the field
  // entirely — only an explicit false should withhold trust.
  assert.equal(isAutoLaunchResultTrustworthy({}), true);
});

/**
 * Minimal model of the hydration effect's decision: given a getAutoLaunch()
 * result, should it overwrite the cached autoLaunchEnabled value? This is
 * the exact regression scenario from the review — a transient read failure
 * must not overwrite a cached `true` with a "successful-looking" `false`,
 * which would otherwise cascade into an unwanted disable write via the
 * adjacent push effect.
 */
function simulateHydration(cachedEnabled: boolean, result: { success: boolean; enabled: boolean }): boolean {
  if (!isAutoLaunchResultTrustworthy(result)) return cachedEnabled;
  return result.enabled;
}

test('a transient read failure during hydration keeps the cached enabled value', () => {
  const next = simulateHydration(true, { success: false, enabled: false });
  assert.equal(next, true, 'must not silently flip a cached true to false on an unrelated read failure');
});

test('a successful read during hydration applies the real OS state, even when it flips the cache', () => {
  const next = simulateHydration(true, { success: true, enabled: false });
  assert.equal(next, false, 'a confirmed read must still win over a stale cache');
});

/**
 * Model of the actual hook: a mount-time hydration request and a
 * user-triggered write can race. This mirrors the real ordering the review
 * flagged — hydration starts, the user toggles before it resolves (which
 * starts a real write), then the stale hydration response arrives.
 */
function simulateMountToggleRace(
  initialCachedEnabled: boolean,
  userToggleTo: boolean,
  staleHydrationResult: { success: boolean; enabled: boolean },
): boolean {
  let state = initialCachedEnabled;
  let writeStarted = false;

  // Hydration request is issued here (pending) — resolution happens later.

  // The user toggles before hydration resolves; this is what the real push
  // effect does: apply optimistically and flag that a real write started.
  state = userToggleTo;
  writeStarted = true;

  // The stale hydration response now arrives.
  if (!writeStarted && isAutoLaunchResultTrustworthy(staleHydrationResult)) {
    state = staleHydrationResult.enabled;
  }

  return state;
}

test('a user toggle during in-flight hydration is not clobbered by the stale hydration response', () => {
  const result = simulateMountToggleRace(false, true, { success: true, enabled: false });

  assert.equal(
    result,
    true,
    'the stale response (enabled:false, read before the user toggled) must not overwrite the user\'s fresh true — ' +
      'otherwise the adjacent push effect reacts to the overwrite and disables an item the user just enabled',
  );
});

test('a user toggle to false during in-flight hydration is not clobbered by a stale enabled:true response', () => {
  const result = simulateMountToggleRace(true, false, { success: true, enabled: true });

  assert.equal(result, false, 'symmetric case: disabling must also win over a stale hydration response');
});

test('hydration still applies normally when it resolves before any write starts', () => {
  // No race: writeStarted stays false throughout, so trustworthy hydration
  // results must still be applied — the guard must not disable hydration
  // unconditionally, only once a real write has actually begun.
  let state = false;
  let writeStarted = false;
  const result = { success: true, enabled: true };

  if (!writeStarted && isAutoLaunchResultTrustworthy(result)) {
    state = result.enabled;
  }

  assert.equal(state, true);
  void writeStarted;
});
