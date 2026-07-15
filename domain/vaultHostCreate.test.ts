import test from 'node:test';
import assert from 'node:assert/strict';

import type { Host } from './models.ts';
import {
  applyVaultHostDelete,
  applyVaultHostCreates,
  applyVaultHostUpdate,
  buildVaultHostFromDraft,
  buildVaultHostsFromDrafts,
  parseVaultHostDraftsInput,
} from './vaultHostCreate.ts';

test('buildVaultHostFromDraft maps minimal unstructured fields to a vault host', () => {
  const built = buildVaultHostFromDraft({
    hostname: '192.168.1.10',
    username: 'ubuntu',
    label: 'prod web',
    group: 'infra/prod',
    tags: 'web, nginx',
  });

  assert.equal(built.ok, true);
  if (!built.ok) return;
  assert.equal(built.host.hostname, '192.168.1.10');
  assert.equal(built.host.username, 'ubuntu');
  assert.equal(built.host.group, 'infra/prod');
  assert.deepEqual(built.host.tags, ['web', 'nginx']);
});

test('buildVaultHostFromDraft accepts host aliases and a referenced key path', () => {
  const built = buildVaultHostFromDraft({
    name: 'prod api',
    ip: '10.0.0.10',
    username: 'deploy',
    keyPath: '~/.ssh/id_ed25519',
  });

  assert.equal(built.ok, true);
  if (!built.ok) return;
  assert.equal(built.host.label, 'prod api');
  assert.equal(built.host.hostname, '10.0.0.10');
  assert.equal(built.host.authMethod, 'key');
  assert.deepEqual(built.host.identityFilePaths, ['~/.ssh/id_ed25519']);
  assert.equal(built.host.password, undefined);
});

test('parseVaultHostDraftsInput accepts JSON array strings', () => {
  const parsed = parseVaultHostDraftsInput(JSON.stringify([
    { hostname: '10.0.0.1', username: 'root' },
    { hostname: '10.0.0.2', username: 'deploy', port: 2222 },
  ]));

  assert.equal(parsed.ok, true);
  if (!parsed.ok) return;
  assert.equal(parsed.drafts.length, 2);
});

test('applyVaultHostCreates writes sanitized hosts into the vault list', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'db',
    hostname: '10.0.0.99',
    username: 'root',
    port: 22,
    tags: [],
    os: 'linux',
  };
  const { hosts: built } = buildVaultHostsFromDrafts([
    { hostname: '10.0.0.10', username: 'deploy', group: 'prod' },
  ]);
  const merged = applyVaultHostCreates([existing], ['legacy'], built);

  assert.equal(merged.addedCount, 1);
  assert.equal(merged.hosts.length, 2);
  assert.ok(merged.customGroups.includes('prod'));
});

test('applyVaultHostUpdate changes only provided fields and adds a new group', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'old label',
    hostname: '10.0.0.1',
    username: 'root',
    port: 22,
    tags: ['keep'],
    os: 'linux',
    notes: 'old notes',
  };

  const result = applyVaultHostUpdate([existing], ['legacy'], 'host-1', {
    name: 'new label',
    host: 'server.example.com',
    port: 2222,
    group: 'prod/api',
    notes: '',
  });

  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.equal(result.updatedHost.label, 'new label');
  assert.equal(result.updatedHost.hostname, 'server.example.com');
  assert.equal(result.updatedHost.port, 2222);
  assert.equal(result.updatedHost.username, 'root');
  assert.deepEqual(result.updatedHost.tags, ['keep']);
  assert.equal(result.updatedHost.notes, undefined);
  assert.ok(result.customGroups.includes('prod/api'));
});

test('applyVaultHostUpdate can switch a host to a referenced key path', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'host',
    hostname: '10.0.0.1',
    username: 'root',
    port: 22,
    tags: [],
    os: 'linux',
    password: 'secret',
    authMethod: 'password',
  };

  const result = applyVaultHostUpdate([existing], [], 'host-1', {
    keyPath: '/Users/alice/.ssh/id_ed25519',
  });

  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.equal(result.updatedHost.authMethod, 'key');
  assert.deepEqual(result.updatedHost.identityFilePaths, ['/Users/alice/.ssh/id_ed25519']);
  assert.equal(result.updatedHost.identityId, '');
});

test('applyVaultHostUpdate parses JSON array tag strings', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'host',
    hostname: '10.0.0.1',
    username: 'root',
    tags: [],
    os: 'linux',
  };

  const result = applyVaultHostUpdate([existing], [], 'host-1', {
    tags: '["prod", "api", "prod"]',
  });

  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.deepEqual(result.updatedHost.tags, ['prod', 'api']);
});

test('applyVaultHostUpdate rejects malformed JSON tag strings', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'host',
    hostname: '10.0.0.1',
    username: 'root',
    tags: ['keep'],
    os: 'linux',
  };

  const result = applyVaultHostUpdate([existing], [], 'host-1', {
    tags: '["prod"',
  });

  assert.equal(result.ok, false);
  if (result.ok) return;
  assert.match(result.error, /valid JSON array/i);
});

test('applyVaultHostUpdate clears only the local key path when another identity is selected', () => {
  const identityHost: Host = {
    id: 'identity-host',
    label: 'identity host',
    hostname: 'identity.example.com',
    username: 'root',
    tags: [],
    os: 'linux',
    identityId: 'identity-1',
    identityFilePaths: ['~/.ssh/old'],
    authMethod: 'key',
  };
  const keychainHost: Host = {
    id: 'keychain-host',
    label: 'keychain host',
    hostname: 'keychain.example.com',
    username: 'root',
    tags: [],
    os: 'linux',
    identityFileId: 'key-1',
    identityFilePaths: ['~/.ssh/old'],
    authMethod: 'key',
  };

  const identityResult = applyVaultHostUpdate([identityHost], [], identityHost.id, { keyPath: '' });
  const keychainResult = applyVaultHostUpdate([keychainHost], [], keychainHost.id, { keyPath: '' });

  assert.equal(identityResult.ok, true);
  assert.equal(keychainResult.ok, true);
  if (!identityResult.ok || !keychainResult.ok) return;
  assert.equal(identityResult.updatedHost.identityId, 'identity-1');
  assert.equal(identityResult.updatedHost.authMethod, 'key');
  assert.equal(identityResult.updatedHost.identityFilePaths, undefined);
  assert.equal(keychainResult.updatedHost.identityFileId, 'key-1');
  assert.equal(keychainResult.updatedHost.authMethod, 'key');
  assert.equal(keychainResult.updatedHost.identityFilePaths, undefined);
});

test('applyVaultHostUpdate rejects saved password changes when password saving is disabled', () => {
  const existing: Host = {
    id: 'host-1',
    label: 'host',
    hostname: '10.0.0.1',
    username: 'root',
    tags: [],
    os: 'linux',
    savePassword: false,
  };

  const result = applyVaultHostUpdate([existing], [], 'host-1', {
    password: 'do-not-persist',
  });

  assert.equal(result.ok, false);
  if (result.ok) return;
  assert.match(result.error, /not to save passwords/i);
});

test('applyVaultHostDelete removes only the requested host', () => {
  const hosts: Host[] = [
    { id: 'host-1', label: 'one', hostname: 'one', username: 'root', tags: [], os: 'linux' },
    { id: 'host-2', label: 'two', hostname: 'two', username: 'root', tags: [], os: 'linux' },
  ];

  const result = applyVaultHostDelete(hosts, 'host-1');
  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.equal(result.deletedHost.id, 'host-1');
  assert.deepEqual(result.hosts.map((host) => host.id), ['host-2']);
});
