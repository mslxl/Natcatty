'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  compareTapResults,
  parseTapResult,
} = require('./compare-ci-test-baseline.cjs');

const tap = ({ failures = [], fail = failures.length, cancelled = 0, skipped = 0, todo = 0, tests = 10 } = {}) => [
  'TAP version 13',
  ...failures.map((name, index) => `not ok ${index + 1} - ${name}`),
  `# fail ${fail}`,
  `# cancelled ${cancelled}`,
  `# skipped ${skipped}`,
  `# todo ${todo}`,
  `# tests ${tests}`,
].join('\n');

test('accepts a clean candidate even when the base was red', () => {
  const result = compareTapResults(
    parseTapResult(tap({ failures: ['base failure'] }), 1),
    parseTapResult(tap(), 0),
  );
  assert.equal(result.passed, true);
  assert.equal(result.kind, 'clean');
});

test('rejects a zero-exit candidate without a complete clean TAP summary', () => {
  const result = compareTapResults(
    parseTapResult(tap(), 0),
    parseTapResult('custom test command completed', 0),
  );
  assert.equal(result.passed, false);
  assert.equal(result.kind, 'unclassified_failure');
});

test('rejects a clean candidate when the exact-base TAP summary is incomplete', () => {
  const result = compareTapResults(
    parseTapResult('base runner stopped early', 1),
    parseTapResult(tap(), 0),
  );
  assert.equal(result.passed, false);
  assert.equal(result.kind, 'unclassified_failure');
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

test('rejects an additional runner failure after comparable TAP output', () => {
  const sameTap = tap({ failures: ['base failure'] });
  const differentExit = compareTapResults(
    parseTapResult(sameTap, 1),
    parseTapResult(`${sameTap}\nrunner crashed after tests`, 2),
  );
  assert.equal(differentExit.passed, false);
  assert.equal(differentExit.kind, 'unclassified_failure');

  const sameExit = compareTapResults(
    parseTapResult(sameTap, 1),
    parseTapResult(`${sameTap}\nrunner crashed after tests`, 1),
  );
  assert.equal(sameExit.passed, false);
  assert.equal(sameExit.kind, 'unclassified_failure');
});

test('distinguishes same-title failures by stable TAP diagnostics', () => {
  const withDiagnostic = (error, location = '/workspace/example.test.js:10:1') => [
    'TAP version 13',
    'not ok 1 - duplicate title',
    '  ---',
    `  location: '${location}'`,
    "  failureType: 'testCodeFailure'",
    `  error: '${error}'`,
    "  code: 'ERR_ASSERTION'",
    '  stack: |- ',
    '    volatile stack line',
    '  ...',
    '# fail 1',
    '# cancelled 0',
    '# skipped 0',
    '# todo 0',
    '# tests 10',
  ].join('\n');
  const same = compareTapResults(
    parseTapResult(withDiagnostic('old failure'), 1),
    parseTapResult(withDiagnostic('old failure'), 1),
  );
  assert.equal(same.passed, true);

  const moved = compareTapResults(
    parseTapResult(withDiagnostic('old failure'), 1),
    parseTapResult(withDiagnostic('old failure', '/workspace/example.test.js:11:1'), 1),
  );
  assert.equal(moved.passed, true);

  const changed = compareTapResults(
    parseTapResult(withDiagnostic('old failure'), 1),
    parseTapResult(withDiagnostic('new regression'), 1),
  );
  assert.equal(changed.passed, false);
  assert.deepEqual(changed.newFailures, ['duplicate title']);

  const dynamicValues = (actual, nextTitle) => [
    'TAP version 13',
    'not ok 1 - duplicate title',
    '  ---',
    "  location: '/workspace/example.test.js:10:1'",
    "  failureType: 'testCodeFailure'",
    "  error: 'old failure'",
    `  actual: ${actual}`,
    '  expected: 1',
    "  code: 'ERR_ASSERTION'",
    '  stack: |- ',
    '    volatile stack line',
    '  ...',
    `# Subtest: ${nextTitle}`,
    `ok 2 - ${nextTitle}`,
    '1..2',
    '# fail 1',
    '# cancelled 0',
    '# skipped 0',
    '# todo 0',
    '# tests 2',
  ].join('\n');
  const dynamic = compareTapResults(
    parseTapResult(dynamicValues(41831, 'later test'), 1),
    parseTapResult(dynamicValues(52942, 'renamed later test'), 1),
  );
  assert.equal(dynamic.passed, true);

  const multilineError = (reason) => [
    'TAP version 13',
    'not ok 1 - duplicate title',
    '  ---',
    "  location: '/workspace/example.test.js:10:1'",
    "  failureType: 'testCodeFailure'",
    '  error: |-',
    `    ${reason}`,
    "  code: 'ERR_ASSERTION'",
    "  operator: 'strictEqual'",
    '  ...',
    '# fail 1',
    '# cancelled 0',
    '# skipped 0',
    '# todo 0',
    '# tests 10',
  ].join('\n');
  const changedMultilineError = compareTapResults(
    parseTapResult(multilineError('old failure'), 1),
    parseTapResult(multilineError('new regression'), 1),
  );
  assert.equal(changedMultilineError.passed, false);
  assert.deepEqual(changedMultilineError.newFailures, ['duplicate title']);

  const assertionDiff = (field, actual) => [
    'TAP version 13',
    'not ok 1 - duplicate title',
    '  ---',
    "  failureType: 'testCodeFailure'",
    '  error: |-',
    '    Expected values to be strictly equal:',
    '',
    '    {',
    `      ${field}: ${actual}`,
    '    }',
    "  code: 'ERR_ASSERTION'",
    "  operator: 'strictEqual'",
    '  ...',
    '# fail 1',
    '# cancelled 0',
    '# skipped 0',
    '# todo 0',
    '# tests 10',
  ].join('\n');
  const changedAssertionValue = compareTapResults(
    parseTapResult(assertionDiff('now', 111), 1),
    parseTapResult(assertionDiff('now', 222), 1),
  );
  assert.equal(changedAssertionValue.passed, true);

  const changedStableAssertionValue = compareTapResults(
    parseTapResult(assertionDiff('x', 1), 1),
    parseTapResult(assertionDiff('x', 3), 1),
  );
  assert.equal(changedStableAssertionValue.passed, false);

  const changedPortAssertion = compareTapResults(
    parseTapResult(assertionDiff('port', 2222), 1),
    parseTapResult(assertionDiff('port', 443), 1),
  );
  assert.equal(changedPortAssertion.passed, false);

  const customMultilineError = (reason) => multilineError(reason).replace(
    "code: 'ERR_ASSERTION'",
    "code: 'ERR_CUSTOM'",
  );
  const changedCustomError = compareTapResults(
    parseTapResult(customMultilineError('service rejected: old reason'), 1),
    parseTapResult(customMultilineError('service rejected: new reason'), 1),
  );
  assert.equal(changedCustomError.passed, false);
});

test('rejects added duplicate failures, cancellations, skipped tests, and TODOs', () => {
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

  const skipped = compareTapResults(
    parseTapResult(tap({ skipped: 1 }), 0),
    parseTapResult(tap({ skipped: 10 }), 0),
  );
  assert.equal(skipped.passed, false);
  assert.equal(skipped.kind, 'unclassified_failure');

  const todo = compareTapResults(
    parseTapResult(tap({ todo: 1 }), 0),
    parseTapResult(tap({ todo: 10 }), 0),
  );
  assert.equal(todo.passed, false);
  assert.equal(todo.kind, 'unclassified_failure');
});

test('fails closed when a red run has no complete TAP summary', () => {
  const result = compareTapResults(
    parseTapResult('runner stopped early', 1),
    parseTapResult('runner stopped early', 1),
  );
  assert.equal(result.passed, false);
  assert.equal(result.kind, 'unclassified_failure');
});

test('rejects non-test failures and a candidate that runs fewer tests', () => {
  const posttestFailure = compareTapResults(
    parseTapResult(tap({ failures: ['existing'] }), 1),
    parseTapResult(tap({ fail: 0 }), 1),
  );
  assert.equal(posttestFailure.passed, false);
  assert.equal(posttestFailure.kind, 'unclassified_failure');

  const fewerTests = compareTapResults(
    parseTapResult(tap({ failures: ['existing'], tests: 20 }), 1),
    parseTapResult(tap({ failures: ['existing'], tests: 19 }), 1),
  );
  assert.equal(fewerTests.passed, false);
  assert.equal(fewerTests.kind, 'unclassified_failure');
});
