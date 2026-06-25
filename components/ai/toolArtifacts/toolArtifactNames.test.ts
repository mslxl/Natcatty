import test from 'node:test';
import assert from 'node:assert/strict';

import {
  inferArtifactToolNameFromCliArgs,
  normalizeArtifactToolName,
} from './toolArtifactNames.ts';

test('normalizeArtifactToolName unwraps MCP server prefixes', () => {
  assert.equal(
    normalizeArtifactToolName('mcp__netcatty__vault_notes_create'),
    'vault_notes_create',
  );
  assert.equal(
    normalizeArtifactToolName('mcp__netcatty-remote-hosts__terminal_read_context'),
    'terminal_read_context',
  );
});

test('inferArtifactToolNameFromCliArgs maps Netcatty CLI artifact commands', () => {
  assert.equal(
    inferArtifactToolNameFromCliArgs({
      command: `/bin/zsh -lc '"/Applications/Netcatty.app/netcatty-tool-cli" vault host get --host-id host_1 --json'`,
    }),
    'host_get',
  );
});
