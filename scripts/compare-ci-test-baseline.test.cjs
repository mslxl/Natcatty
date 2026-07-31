'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  compareTapResults,
  parseTapResult,
} = require('./compare-ci-test-baseline.cjs');

const tap = ({ failures = [], fail = failures.length, cancelled = 0 } = {}) => [
  'TAP version 13',
  ...failures.map((name, index) => `not ok ${index + 1} - ${name}`),
  `# fail ${fail}`,
  `# cancelled ${cancelled}`,
].join('\n');

test('accepts a clean candidate even when the base was red', () => {
  const result = compareTapResults(
    parseTapResult(tap({ failures: ['base failure'] }), 1),
    parseTapResult(tap(), 0),
  );
  assert.equal(result.passed, true);
  assert.equal(result.kind, 'clean');
});

test('accepts only failures already present on the exact base', () => {
  const result = compareTapResults(
    parseTapResult(tap({ failures: ['base A', 'base B'] }), 1),
    parseTapResult(tap({ failures: ['base B'] }), 1),
  );
  assert.equal(result.passed, true);
  assert.equal(result.kind, 'baseline_only');
  assert.deepEqual(result.newFailures, []);
});

test('rejects a different candidate failure even when both runs are red', () => {
  const result = compareTapResults(
    parseTapResult(tap({ failures: ['base failure'] }), 1),
    parseTapResult(tap({ failures: ['candidate regression'] }), 1),
  );
  assert.equal(result.passed, false);
  assert.equal(result.kind, 'new_failures');
  assert.deepEqual(result.newFailures, ['candidate regression']);
});

test('rejects added duplicate failures and cancelled tests', () => {
  const duplicate = compareTapResults(
    parseTapResult(tap({ failures: ['same'] }), 1),
    parseTapResult(tap({ failures: ['same', 'same'] }), 1),
  );
  assert.equal(duplicate.passed, false);

  const cancelled = compareTapResults(
    parseTapResult(tap({ failures: ['same'] }), 1),
    parseTapResult(tap({ failures: ['same'], cancelled: 2 }), 1),
  );
  assert.equal(cancelled.passed, false);
  assert.equal(cancelled.kind, 'cancelled_tests');
});

test('fails closed when a red run has no complete TAP summary', () => {
  const result = compareTapResults(
    parseTapResult('runner stopped early', 1),
    parseTapResult('runner stopped early', 1),
  );
  assert.equal(result.passed, false);
  assert.equal(result.kind, 'unclassified_failure');
});
