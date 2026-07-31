'use strict';

const fs = require('node:fs');

const ANSI_RE = /\x1b\[[0-?]*[ -/]*[@-~]/g;

function parseTapResult(text, exitCode) {
  const normalized = String(text || '').replace(ANSI_RE, '').replace(/\r\n?/g, '\n');
  const failures = [];
  let failCount = null;
  let cancelledCount = null;
  let testCount = null;
  for (const line of normalized.split('\n')) {
    const failed = line.match(/^\s*not ok \d+ - (.+?)(?:\s+#.*)?$/);
    if (failed) failures.push(failed[1].trim());
    const failSummary = line.match(/^\s*# fail (\d+)\s*$/);
    if (failSummary) failCount = Number(failSummary[1]);
    const cancelledSummary = line.match(/^\s*# cancelled (\d+)\s*$/);
    if (cancelledSummary) cancelledCount = Number(cancelledSummary[1]);
    const testSummary = line.match(/^\s*# tests (\d+)\s*$/);
    if (testSummary) testCount = Number(testSummary[1]);
  }
  return {
    exitCode: Number(exitCode),
    failures,
    failCount,
    cancelledCount,
    testCount,
    complete:
      failCount !== null && cancelledCount !== null && testCount !== null,
  };
}

function countFailures(failures) {
  const counts = new Map();
  for (const failure of failures) {
    counts.set(failure, (counts.get(failure) || 0) + 1);
  }
  return counts;
}

function compareTapResults(baseline, candidate) {
  if (candidate.exitCode === 0) {
    const completeCleanRun =
      candidate.complete &&
      candidate.failCount === 0 &&
      candidate.cancelledCount === 0 &&
      (!baseline.complete || candidate.testCount >= baseline.testCount);
    return {
      passed: completeCleanRun,
      kind: completeCleanRun ? 'clean' : 'unclassified_failure',
      baselineFailures: baseline.failures,
      candidateFailures: [],
      newFailures: [],
    };
  }

  if (baseline.exitCode === 0) {
    return {
      passed: false,
      kind: 'new_failures',
      baselineFailures: [],
      candidateFailures: candidate.failures,
      newFailures: candidate.failures,
    };
  }

  if (!baseline.complete || !candidate.complete) {
    return {
      passed: false,
      kind: 'unclassified_failure',
      baselineFailures: baseline.failures,
      candidateFailures: candidate.failures,
      newFailures: candidate.failures,
    };
  }

  if (candidate.failCount === 0 || candidate.testCount < baseline.testCount) {
    return {
      passed: false,
      kind: 'unclassified_failure',
      baselineFailures: baseline.failures,
      candidateFailures: candidate.failures,
      newFailures: candidate.failures,
    };
  }

  if (candidate.cancelledCount > 0) {
    return {
      passed: false,
      kind: 'cancelled_tests',
      baselineFailures: baseline.failures,
      candidateFailures: candidate.failures,
      newFailures: candidate.failures,
    };
  }

  const baselineCounts = countFailures(baseline.failures);
  const candidateCounts = countFailures(candidate.failures);
  const newFailures = [];
  for (const [failure, count] of candidateCounts) {
    const extra = count - (baselineCounts.get(failure) || 0);
    for (let i = 0; i < extra; i += 1) newFailures.push(failure);
  }
  const parsedCountsMatch =
    baseline.failures.length >= Number(baseline.failCount) &&
    candidate.failures.length >= Number(candidate.failCount);
  const passed = newFailures.length === 0 && parsedCountsMatch;
  return {
    passed,
    kind: passed ? 'baseline_only' : 'new_failures',
    baselineFailures: baseline.failures,
    candidateFailures: candidate.failures,
    newFailures,
  };
}

function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i];
    if (!key?.startsWith('--') || argv[i + 1] === undefined) {
      throw new Error('Expected --name value arguments.');
    }
    values[key.slice(2)] = argv[i + 1];
  }
  return values;
}

function main(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  for (const required of [
    'baseline-log',
    'baseline-exit',
    'candidate-log',
    'candidate-exit',
    'output',
  ]) {
    if (!(required in args)) throw new Error(`Missing --${required}.`);
  }
  const baseline = parseTapResult(
    fs.readFileSync(args['baseline-log'], 'utf8'),
    args['baseline-exit'],
  );
  const candidate = parseTapResult(
    fs.readFileSync(args['candidate-log'], 'utf8'),
    args['candidate-exit'],
  );
  const result = compareTapResults(baseline, candidate);
  fs.writeFileSync(args.output, `${JSON.stringify(result, null, 2)}\n`, 'utf8');
  console.log(
    result.passed
      ? `Test comparison accepted: ${result.kind}.`
      : `Test comparison failed: ${result.kind}.`,
  );
  if (result.newFailures.length) {
    console.error(`New failures:\n- ${result.newFailures.join('\n- ')}`);
  }
  return result.passed ? 0 : 1;
}

if (require.main === module) {
  try {
    process.exitCode = main();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 2;
  }
}

module.exports = {
  compareTapResults,
  main,
  parseTapResult,
};
