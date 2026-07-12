import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./KeychainExportPanel.tsx', import.meta.url), 'utf8');

test('key export forwards the selected host SSH connection timeouts', () => {
  assert.match(source, /resolveHostSshConnectionTimeouts\(effectiveExportHost\)/);
  assert.match(source, /sshTcpConnectTimeoutMs: connectionTimeouts\.tcpConnectTimeoutSeconds \* 1000/);
  assert.match(source, /sshAuthReadyTimeoutMs: connectionTimeouts\.authReadyTimeoutSeconds \* 1000/);
});
