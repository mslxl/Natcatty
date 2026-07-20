import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import { applyTerminalKeywordHighlightRules } from './terminalKeywordHighlightRules.ts';

const effectsSource = readFileSync(new URL('./useTerminalEffects.ts', import.meta.url), 'utf8');
const terminalSource = readFileSync(new URL('../Terminal.tsx', import.meta.url), 'utf8');
const xtermRuntimeSource = readFileSync(new URL('./runtime/createXTermRuntime.ts', import.meta.url), 'utf8');

test('hibernate runtime keyword setup restores plugin decoration rules', () => {
  let applied: { rules: unknown[]; enabled: boolean } | undefined;
  const runtime = {
    keywordHighlighter: {
      setRules(rules: unknown[], enabled: boolean) {
        applied = { rules, enabled };
      },
    },
  };
  applyTerminalKeywordHighlightRules(
    runtime as never,
    { current: { keywordHighlightEnabled: false, keywordHighlightRules: [] } } as never,
    { keywordHighlightEnabled: false, keywordHighlightRules: [] } as never,
    [{
      id: 'com.example.decorations:error',
      label: 'Error',
      patterns: ['\\berror\\b'],
      color: '#ff0000',
      enabled: true,
    }],
  );
  assert.deepEqual(applied, {
    enabled: true,
    rules: [{
      id: 'com.example.decorations:error',
      label: 'Error',
      patterns: ['\\berror\\b'],
      color: '#ff0000',
      enabled: true,
    }],
  });
});

test('cwd-triggered plugin decoration refresh reads the live connection status', () => {
  assert.match(
    effectsSource,
    /if \(!pluginTerminalRegistry \|\| statusRef\.current !== 'connected'\s*\|\| !isPluginTerminalProviderAvailable\('terminal\.decoration'\)\)/,
  );
  assert.match(
    effectsSource,
    /void refreshPluginDecorationRules\('session-state'\);\s*\n\s*}, \[refreshPluginDecorationRules, status\]\);/,
  );
});

test('disabled or absent plugin hosts do not receive terminal completion requests', () => {
  const autocompleteSource = readFileSync(new URL('./TerminalAutocomplete.tsx', import.meta.url), 'utf8');
  assert.match(
    autocompleteSource,
    /isPluginCompletionProviderAvailable\?\.\(\) === false\s*\n\s*\? null\s*\n\s*: getWindowPluginTerminalProviderRegistry\(\)/,
  );
  assert.match(
    terminalSource,
    /isPluginTerminalProviderAvailable,/,
  );
});

test('plugin decoration requests retain the workspace identity', () => {
  assert.match(
    effectsSource,
    /\.\.\.\(workspaceId \? \{ workspaceId \} : \{\}\),\s*\n\s*protocol,/,
  );
});

test('plugin decoration responses cannot apply after connection state invalidates the request', () => {
  assert.match(
    effectsSource,
    /const pluginDecorationRefreshGenerationRef = useRef\(0\);/,
  );
  assert.match(
    effectsSource,
    /const refreshGeneration = \+\+pluginDecorationRefreshGenerationRef\.current;/,
  );
  assert.match(
    effectsSource,
    /useEffect\(\(\) => \(\) => \{\s*pluginDecorationRefreshGenerationRef\.current \+= 1;\s*pluginDecorationAbortRef\.current\?\.abort\(\);\s*pluginDecorationAbortRef\.current = null;\s*}, \[\]\);/,
  );
  assert.match(
    effectsSource,
    /pluginTerminalRegistry\.request\([\s\S]*?\{ signal: controller\.signal \}\),\s*PLUGIN_DECORATION_RESPONSE_TIMEOUT_MS,\s*\(\) => controller\.abort\(\)/,
  );
  assert.match(
    effectsSource,
    /response\.stale\s*\|\|\s*refreshGeneration !== pluginDecorationRefreshGenerationRef\.current\s*\|\|\s*statusRef\.current !== 'connected'/,
  );
  assert.match(
    effectsSource,
    /catch \{\s*if \(\s*refreshGeneration !== pluginDecorationRefreshGenerationRef\.current\s*\|\|\s*statusRef\.current !== 'connected'/,
  );
});

test('terminal Provider snapshots use the selected ET or Mosh transport throughout renderer paths', () => {
  assert.match(terminalSource, /protocol: effectiveTerminalProtocol,/);
  assert.match(effectsSource, /protocol: effectiveTerminalProtocol,/);
  assert.match(terminalSource, /protocol: effectiveTerminalProtocol,\s*terminalSettings,/);
  assert.match(terminalSource, /protocol: effectiveTerminalProtocol,\s*status,/);
});

test('password-prompt input is consumed before every semantic command callback', () => {
  assert.match(
    xtermRuntimeSource,
    /const sensitive = ctx\.passwordPromptActiveRef\?\.current === true;[\s\S]*?recordTerminalCommandExecution\([\s\S]*?\{ sensitive \},\s*\);/,
  );
  assert.match(
    terminalSource,
    /const sensitive = passwordPromptActiveRef\.current;[\s\S]*?recordTerminalCommandExecution\([\s\S]*?\{ sensitive \}\);/,
  );
});

test('backend exits are forwarded to the Provider lifecycle with their exit code', () => {
  assert.match(terminalSource, /pluginTerminalSessionExitRef\.current\(evt\.exitCode\);/);
  assert.match(terminalSource, /pluginTerminalLifecycle\.onSessionExited\(evt\.exitCode\);/);
});

test('normal boot and hibernate wake share the refresh-capable runtime cwd handler', () => {
  assert.match(
    terminalSource,
    /const pluginAwareOnRuntimeCwdChange = useCallback/,
  );
  assert.match(
    terminalSource,
    /pluginDecorationRefreshRef\.current\('cwd-changed'\);/,
  );
  assert.match(
    terminalSource,
    /onCwdChange: \(cwd: string\) => \{\s*pluginAwareOnRuntimeCwdChange\(cwd, \{ source: 'osc7' \}\);\s*},/,
  );
  assert.match(
    effectsSource,
    /onPluginRuntimeCwdChange\(cwd, \{ source: 'osc7' \}\);/,
  );
});
