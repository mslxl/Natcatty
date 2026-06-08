import assert from 'node:assert/strict';
import test from 'node:test';

import { terminalLayoutSuppressStore } from './terminalLayoutSuppressStore';

test('terminalLayoutSuppressStore tracks nested begin/end', () => {
  assert.equal(terminalLayoutSuppressStore.getActive(), false);
  terminalLayoutSuppressStore.begin();
  assert.equal(terminalLayoutSuppressStore.getActive(), true);
  terminalLayoutSuppressStore.begin();
  assert.equal(terminalLayoutSuppressStore.getActive(), true);
  terminalLayoutSuppressStore.end();
  assert.equal(terminalLayoutSuppressStore.getActive(), true);
  terminalLayoutSuppressStore.end();
  assert.equal(terminalLayoutSuppressStore.getActive(), false);
});
