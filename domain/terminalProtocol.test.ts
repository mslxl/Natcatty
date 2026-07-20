import assert from 'node:assert/strict';
import test from 'node:test';

import { resolveEffectiveTerminalProtocol } from './terminalProtocol.ts';

test('effective terminal protocol reflects enabled ET and Mosh transports', () => {
  assert.equal(resolveEffectiveTerminalProtocol({ protocol: 'ssh' }), 'ssh');
  assert.equal(resolveEffectiveTerminalProtocol({ protocol: 'ssh', moshEnabled: true }), 'mosh');
  assert.equal(resolveEffectiveTerminalProtocol({ protocol: 'ssh', etEnabled: true }), 'et');
  assert.equal(
    resolveEffectiveTerminalProtocol({ protocol: 'ssh', moshEnabled: true, etEnabled: true }),
    'mosh',
  );
  assert.equal(resolveEffectiveTerminalProtocol({ protocol: 'serial', moshEnabled: true }), 'serial');
});
