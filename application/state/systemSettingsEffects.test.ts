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
