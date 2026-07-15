import test from 'node:test';
import assert from 'node:assert/strict';
import { encodeSdkSessionIdentity, parseSdkSessionIdentity } from './sdkSessionIdentity';

test('SDK session identities preserve Codex runtime and default legacy values to sdk', () => {
  const encoded = encodeSdkSessionIdentity('thread-1', 'codex', '/bin/codex', 'app-server');
  assert.deepEqual(parseSdkSessionIdentity(encoded), {
    v: 1,
    id: 'thread-1',
    backend: 'codex',
    binPath: '/bin/codex',
    runtime: 'app-server',
  });

  const legacy = encodeSdkSessionIdentity('thread-2', 'codex', '/bin/codex');
  const payload = JSON.parse(decodeURIComponent(legacy.slice('netcatty-sdk-session:'.length)));
  delete payload.runtime;
  const legacyWithoutRuntime = `netcatty-sdk-session:${encodeURIComponent(JSON.stringify(payload))}`;
  assert.equal(parseSdkSessionIdentity(legacyWithoutRuntime)?.runtime, 'sdk');
});
