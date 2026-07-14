import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  DEFAULT_HOST_CLICK_BEHAVIOR,
  hostCardFocusClassName,
  isHostClickBehavior,
  resolveGroupActivateAction,
  resolveHostActivateAction,
} from './hostClickBehavior';

test('default host click behavior is connect (legacy single-click)', () => {
  assert.equal(DEFAULT_HOST_CLICK_BEHAVIOR, 'connect');
});

test('isHostClickBehavior accepts only known values', () => {
  assert.equal(isHostClickBehavior('connect'), true);
  assert.equal(isHostClickBehavior('select'), true);
  assert.equal(isHostClickBehavior('double'), false);
  assert.equal(isHostClickBehavior(null), false);
});

test('resolveHostActivateAction: multi-select always toggles', () => {
  assert.equal(
    resolveHostActivateAction({
      behavior: 'select',
      isMultiSelectMode: true,
      focusedHostId: 'a',
      hostId: 'a',
    }),
    'toggle-multi',
  );
  assert.equal(
    resolveHostActivateAction({
      behavior: 'connect',
      isMultiSelectMode: true,
      focusedHostId: null,
      hostId: 'a',
    }),
    'toggle-multi',
  );
});

test('resolveHostActivateAction: connect mode always connects', () => {
  assert.equal(
    resolveHostActivateAction({
      behavior: 'connect',
      isMultiSelectMode: false,
      focusedHostId: null,
      hostId: 'a',
    }),
    'connect',
  );
  assert.equal(
    resolveHostActivateAction({
      behavior: 'connect',
      isMultiSelectMode: false,
      focusedHostId: 'a',
      hostId: 'a',
    }),
    'connect',
  );
});

test('resolveHostActivateAction: select mode focuses then connects', () => {
  assert.equal(
    resolveHostActivateAction({
      behavior: 'select',
      isMultiSelectMode: false,
      focusedHostId: null,
      hostId: 'a',
    }),
    'select',
  );
  assert.equal(
    resolveHostActivateAction({
      behavior: 'select',
      isMultiSelectMode: false,
      focusedHostId: 'b',
      hostId: 'a',
    }),
    'select',
  );
  assert.equal(
    resolveHostActivateAction({
      behavior: 'select',
      isMultiSelectMode: false,
      focusedHostId: 'a',
      hostId: 'a',
    }),
    'connect',
  );
});

test('resolveGroupActivateAction: select mode focuses then opens', () => {
  assert.equal(
    resolveGroupActivateAction({
      behavior: 'connect',
      focusedGroupPath: null,
      groupPath: 'prod',
    }),
    'open',
  );
  assert.equal(
    resolveGroupActivateAction({
      behavior: 'select',
      focusedGroupPath: null,
      groupPath: 'prod',
    }),
    'select',
  );
  assert.equal(
    resolveGroupActivateAction({
      behavior: 'select',
      focusedGroupPath: 'prod',
      groupPath: 'prod',
    }),
    'open',
  );
});

test('hostCardFocusClassName: focused = accent border only, unfocused empty', () => {
  assert.equal(hostCardFocusClassName('grid', false), '');
  assert.equal(hostCardFocusClassName('list', false), '');
  const grid = hostCardFocusClassName('grid', true);
  assert.match(grid, /border-primary/);
  assert.doesNotMatch(grid, /bg-/);
  assert.doesNotMatch(grid, /ring-/);
  const list = hostCardFocusClassName('list', true);
  assert.match(list, /border-primary/);
  assert.doesNotMatch(list, /bg-/);
  assert.doesNotMatch(list, /ring-/);
});
