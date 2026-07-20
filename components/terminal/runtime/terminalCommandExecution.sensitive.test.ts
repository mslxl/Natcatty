import assert from 'node:assert/strict';
import test from 'node:test';

import { recordTerminalCommandExecution } from './terminalCommandExecution.ts';

function createFakeTerm(lineText: string) {
  return {
    buffer: {
      active: {
        cursorX: lineText.length,
        cursorY: 0,
        baseY: 0,
        getLine(line: number) {
          if (line !== 0) return undefined;
          return {
            isWrapped: false,
            translateToString() { return lineText; },
          };
        },
      },
    },
  };
}

test('sensitive challenge input never reaches command history or semantic callbacks', () => {
  const submitted: string[] = [];
  const executed: string[] = [];
  const commandBufferRef = { current: '123456' };
  const result = recordTerminalCommandExecution('123456', {
    host: { id: 'host-1', label: 'Host' },
    sessionId: 'session-1',
    onCommandSubmitted: (command) => submitted.push(command),
    onCommandExecuted: (command) => executed.push(command),
    commandBufferRef,
  }, createFakeTerm('OTP> 123456') as never, { sensitive: false });

  assert.equal(result, null);
  assert.equal(commandBufferRef.current, '');
  assert.deepEqual(submitted, []);
  assert.deepEqual(executed, []);
});
