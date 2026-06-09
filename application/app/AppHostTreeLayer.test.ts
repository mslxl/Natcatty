import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const storage = new Map<string, string>();
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  },
});

const {
  getAppHostTreeLayerStyle,
  shouldAutoOpenHostTreeOnSurfaceChange,
} = await import('./AppHostTreeLayer');
const hostTreeLayerSource = readFileSync(new URL('./AppHostTreeLayer.tsx', import.meta.url), 'utf8');

test('shared host tree layer is visible above work tabs', () => {
  assert.deepEqual(getAppHostTreeLayerStyle(true), {
    visibility: 'visible',
    pointerEvents: 'auto',
    zIndex: 30,
  });
});

test('shared host tree layer is hidden behind root pages', () => {
  assert.deepEqual(getAppHostTreeLayerStyle(false), {
    visibility: 'hidden',
    pointerEvents: 'none',
    zIndex: 0,
  });
});

test('shared host tree auto-opens when entering a work tab surface', () => {
  assert.equal(shouldAutoOpenHostTreeOnSurfaceChange({
    enabled: true,
    previousSurfaceVisible: false,
    surfaceVisible: true,
  }), true);
});

test('shared host tree does not force reopen while already on work tab surfaces', () => {
  assert.equal(shouldAutoOpenHostTreeOnSurfaceChange({
    enabled: true,
    previousSurfaceVisible: true,
    surfaceVisible: true,
  }), false);
});

test('shared host tree does not auto-open when disabled', () => {
  assert.equal(shouldAutoOpenHostTreeOnSurfaceChange({
    enabled: false,
    previousSurfaceVisible: false,
    surfaceVisible: true,
  }), false);
});

test('host tree layer hides immediately when leaving work tab surfaces', () => {
  assert.match(hostTreeLayerSource, /getAppHostTreeLayerStyle\(surfaceVisible\)/);
  assert.doesNotMatch(hostTreeLayerSource, /layerVisible/);
});
