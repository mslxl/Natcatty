import assert from 'node:assert/strict';
import test from 'node:test';

import { isSessionReconnectDisabled } from './SessionTabContextMenuContent';

test('reconnect is disabled while a session is still connecting', () => {
  assert.equal(isSessionReconnectDisabled('connecting'), true);
  assert.equal(isSessionReconnectDisabled('connected'), false);
  assert.equal(isSessionReconnectDisabled('disconnected'), false);
});
