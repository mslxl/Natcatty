'use strict';

const fs = require('node:fs');

const ANSI_RE = /\x1b\[[0-?]*[ -/]*[@-~]/g;

function parseTapResult(text, exitCode) {
  const normalized = String(text || '').replace(ANSI_RE, '').replace(/\r\n?/g, '\n');
  const lines = normalized.split('\n');
  const failures = [];
  const failureRecords = [];
  let failCount = null;
  let cancelledCount = null;
  let testCount = null;
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const failed = line.match(/^\s*not ok \d+ - (.+?)(?:\s+#.*)?$/);
    if (failed) {
      const name = failed[1].trim();
      const diagnostic = [];
      let skippingStack = false;
      let stackIndent = -1;
      for (let detailIndex = index + 1; detailIndex < lines.length; detailIndex += 1) {
        const detail = lines[detailIndex];
        if (/^\s*(?:ok|not ok) \d+ - /.test(detail) || /^\s*# (?:tests|fail|cancelled) \d+\s*$/.test(detail)) {
          break;
        }
        const indent = detail.match(/^\s*/)?.[0].length || 0;
        if (skippingStack) {
          if (!detail.trim() || indent > stackIndent) continue;
          skippingStack = false;
        }
        if (/^\s*stack:\s*(?:\|-)?\s*$/.test(detail)) {
          skippingStack = true;
          stackIndent = indent;
          continue;
        }
        if (/^\s*(?:---|\.\.\.)\s*$/.test(detail)) continue;
        if (/^\s*duration_ms:/.test(detail)) continue;
        if (detail.trim()) diagnostic.push(detail.trimEnd());
      }
      failures.push(name);
      failureRecords.push({
        name,
        identity: diagnostic.length ? `${name}\n${diagnostic.join('\n')}` : name,
      });
    }
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
    failureRecords,
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
      baseline.complete &&
      candidate.complete &&
      candidate.failCount === 0 &&
      candidate.cancelledCount === 0 &&
      candidate.testCount >= baseline.testCount;
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

  const baselineCounts = countFailures(
    baseline.failureRecords.map((failure) => failure.identity),
  );
  const candidateCounts = countFailures(
    candidate.failureRecords.map((failure) => failure.identity),
  );
  const newFailures = [];
  for (const [identity, count] of candidateCounts) {
    const extra = count - (baselineCounts.get(identity) || 0);
    const name = candidate.failureRecords.find(
      (failure) => failure.identity === identity,
    )?.name || identity;
    for (let i = 0; i < extra; i += 1) newFailures.push(name);
  }
  const parsedCountsMatch =
    baseline.failureRecords.length >= Number(baseline.failCount) &&
    candidate.failureRecords.length >= Number(candidate.failCount);
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
