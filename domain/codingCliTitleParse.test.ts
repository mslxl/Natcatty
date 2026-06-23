import assert from 'node:assert/strict';
import test from 'node:test';
import {
  inferCodingCliProviderFromTitleSignals,
  normalizeCodingCliTitle,
  resolveCodingCliActivityPhase,
  shouldClearCodingCliProviderForTitle,
  titleHasBrailleSpinner,
} from './codingCliTitleParse';

test('inferCodingCliProviderFromTitleSignals detects Claude and Codex titles', () => {
  assert.equal(inferCodingCliProviderFromTitleSignals('✳ Claude Code · refactor auth'), 'claude');
  assert.equal(inferCodingCliProviderFromTitleSignals('⠋ codex · my-project'), 'codex');
  assert.equal(inferCodingCliProviderFromTitleSignals('⠋ Working · netcatty'), 'codex');
});

test('inferCodingCliProviderFromTitleSignals detects Droid and Factory titles', () => {
  assert.equal(inferCodingCliProviderFromTitleSignals('Factory Droid · auth flow'), 'droid');
  assert.equal(inferCodingCliProviderFromTitleSignals('droid · session'), 'droid');
});

test('resolveCodingCliActivityPhase treats spinner titles as busy', () => {
  assert.equal(
    resolveCodingCliActivityPhase('⠋ netcatty', 'codex'),
    'busy',
  );
  assert.equal(
    resolveCodingCliActivityPhase('netcatty', 'codex'),
    'idle',
  );
});

test('resolveCodingCliActivityPhase detects waiting states', () => {
  assert.equal(
    resolveCodingCliActivityPhase('Claude Code · waiting for approval', 'claude'),
    'waiting',
  );
});

test('normalizeCodingCliTitle strips action-required and dot prefixes', () => {
  assert.equal(normalizeCodingCliTitle('[ ! ] Action Required · deploy'), 'deploy');
  assert.equal(normalizeCodingCliTitle('··· my task'), 'my task');
  assert.equal(normalizeCodingCliTitle('∴ hello'), '∴ hello');
});

test('shouldClearCodingCliProviderForTitle clears on shell titles only', () => {
  assert.equal(shouldClearCodingCliProviderForTitle('zsh', 'codex'), true);
  assert.equal(shouldClearCodingCliProviderForTitle('netcatty', 'codex'), false);
  assert.equal(shouldClearCodingCliProviderForTitle('⠋ Working · netcatty', 'codex'), false);
  assert.equal(shouldClearCodingCliProviderForTitle('', 'codex'), true);
});

test('titleHasBrailleSpinner recognizes Codex frames', () => {
  assert.equal(titleHasBrailleSpinner('⠇ my-app'), true);
  assert.equal(titleHasBrailleSpinner('my-app'), false);
});
