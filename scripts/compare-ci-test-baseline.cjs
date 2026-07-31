'use strict';

const fs = require('node:fs');

const ANSI_RE = /\x1b\[[0-?]*[ -/]*[@-~]/g;

function normalizeAssertionDetail(detail) {
  return detail
    .replace(
      /\b(now|timestamp|pid|nonce|random(?:Value)?)\b(['"]?\s*[:=]\s*)[^,\s}\]]+/gi,
      '$1$2<volatile>',
    )
    .replace(
      /\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\b/g,
      '<timestamp>',
    );
}

function collectNonTapOutput(lines) {
  const output = [];
  let inDiagnostic = false;
  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (/^---$/.test(line)) {
      inDiagnostic = true;
      continue;
    }
    if (inDiagnostic) {
      if (/^\.\.\.$/.test(line)) inDiagnostic = false;
      continue;
    }
    if (
      !line ||
      /^TAP version \d+$/.test(line) ||
      /^(?:ok|not ok) \d+ - /.test(line) ||
      /^# (?:Subtest:|tests |suites |pass |fail |cancelled |skipped |todo |duration_ms )/.test(line) ||
      /^1\.\.\d+$/.test(line)
    ) {
      continue;
    }
    output.push(
      line
        .replace(/\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\b/g, '<timestamp>')
        .replace(/(?:\/private)?\/var\/folders\/\S+|\/tmp\/\S+/g, '<tmp-path>')
        .replace(/:\d+:\d+\b/g, ':<line>:<column>'),
    );
  }
  return output;
}

function parseTapResult(text, exitCode) {
  const normalized = String(text || '').replace(ANSI_RE, '').replace(/\r\n?/g, '\n');
  const lines = normalized.split('\n');
  const failures = [];
  const failureRecords = [];
  const successes = [];
  let failCount = null;
  let cancelledCount = null;
  let skippedCount = null;
  let todoCount = null;
  let testCount = null;
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const succeeded = line.match(/^\s*ok \d+ - (.+)$/);
    const tapDirective = /\s+#\s*(?:SKIP|TODO)\b/i;
    if (succeeded && !tapDirective.test(line)) {
      successes.push(succeeded[1].trim());
    }
    const failed = line.match(/^\s*not ok \d+ - (.+)$/);
    if (failed && !tapDirective.test(line)) {
      const name = failed[1].trim();
      const diagnostic = [];
      const errorDetails = [];
      let inDiagnostic = false;
      let errorBlockIndent = -1;
      let skippingStack = false;
      let stackIndent = -1;
      for (let detailIndex = index + 1; detailIndex < lines.length; detailIndex += 1) {
        const detail = lines[detailIndex];
        if (/^\s*(?:ok|not ok) \d+ - /.test(detail) || /^\s*# (?:tests|fail|cancelled|skipped|todo) \d+\s*$/.test(detail)) {
          break;
        }
        if (/^\s*---\s*$/.test(detail)) {
          inDiagnostic = true;
          continue;
        }
        if (inDiagnostic && /^\s*\.\.\.\s*$/.test(detail)) break;
        if (!inDiagnostic) continue;
        const indent = detail.match(/^\s*/)?.[0].length || 0;
        if (errorBlockIndent >= 0) {
          if (!detail.trim()) continue;
          if (indent > errorBlockIndent) {
            errorDetails.push(detail.trim());
            continue;
          }
          errorBlockIndent = -1;
        }
        if (skippingStack) {
          if (!detail.trim() || indent > stackIndent) continue;
          skippingStack = false;
        }
        if (/^\s*stack:\s*(?:\|-)?\s*$/.test(detail)) {
          skippingStack = true;
          stackIndent = indent;
          continue;
        }
        if (/^\s*duration_ms:/.test(detail)) continue;
        if (/^\s*error:\s*(?:\|-|>)\s*$/.test(detail)) {
          diagnostic.push('error:');
          errorBlockIndent = indent;
          continue;
        }
        if (/^\s*(?:type|location|failureType|error|code|operator):/.test(detail)) {
          diagnostic.push(
            detail.trimEnd().replace(
              /^(\s*location:\s*['"]?.*?):\d+:\d+(['"]?\s*)$/,
              '$1$2',
            ),
          );
        }
      }
      const assertionFailure = diagnostic.some((detail) =>
        /^\s*code:\s*['"]?ERR_ASSERTION['"]?\s*$/.test(detail),
      );
      const stableErrorDetails = assertionFailure
        ? errorDetails.map(normalizeAssertionDetail)
        : errorDetails;
      diagnostic.push(...stableErrorDetails.map((detail) => `error-detail: ${detail}`));
      failures.push(name);
      failureRecords.push({
        name,
        identity: diagnostic.length ? `${name}\n${diagnostic.join('\n')}` : name,
      });
    }
    const failSummary = line.match(/^\s*# fail (\d+)\s*$/);
    if (failSummary) {
      failCount = Number(failSummary[1]);
    }
    const cancelledSummary = line.match(/^\s*# cancelled (\d+)\s*$/);
    if (cancelledSummary) {
      cancelledCount = Number(cancelledSummary[1]);
    }
    const skippedSummary = line.match(/^\s*# skipped (\d+)\s*$/);
    if (skippedSummary) {
      skippedCount = Number(skippedSummary[1]);
    }
    const todoSummary = line.match(/^\s*# todo (\d+)\s*$/);
    if (todoSummary) {
      todoCount = Number(todoSummary[1]);
    }
    const testSummary = line.match(/^\s*# tests (\d+)\s*$/);
    if (testSummary) {
      testCount = Number(testSummary[1]);
    }
  }
  return {
    exitCode: Number(exitCode),
    failures,
    failureRecords,
    successes,
    failCount,
    cancelledCount,
    skippedCount,
    todoCount,
    testCount,
    nonTapOutput: collectNonTapOutput(lines),
    complete:
      failCount !== null &&
      cancelledCount !== null &&
      skippedCount !== null &&
      todoCount !== null &&
      testCount !== null,
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
  const baselineSuccessCounts = countFailures(baseline.successes);
  const candidateSuccessCounts = countFailures(candidate.successes);
  const candidateSuccessPool = new Map(candidateSuccessCounts);
  const missingBaselineSuccesses = [];
  for (const [success, count] of baselineSuccessCounts) {
    const available = candidateSuccessPool.get(success) || 0;
    const missing = count - available;
    candidateSuccessPool.set(success, Math.max(0, available - count));
    for (let i = 0; i < missing; i += 1) missingBaselineSuccesses.push(success);
  }
  const candidateFailurePool = countFailures(
    candidate.failureRecords.map((failure) => failure.identity),
  );
  const missingBaselineFailures = [];
  for (const failure of baseline.failureRecords) {
    const matchingFailures = candidateFailurePool.get(failure.identity) || 0;
    if (matchingFailures > 0) {
      candidateFailurePool.set(failure.identity, matchingFailures - 1);
      continue;
    }
    const matchingSuccesses = candidateSuccessPool.get(failure.name) || 0;
    if (matchingSuccesses > 0) {
      candidateSuccessPool.set(failure.name, matchingSuccesses - 1);
      continue;
    }
    missingBaselineFailures.push(failure.name);
  }

  if (candidate.exitCode === 0) {
    const completeCleanRun =
      baseline.complete &&
      candidate.complete &&
      candidate.failCount === 0 &&
      candidate.cancelledCount === 0 &&
      candidate.skippedCount <= baseline.skippedCount &&
      candidate.todoCount <= baseline.todoCount &&
      candidate.testCount >= baseline.testCount &&
      missingBaselineSuccesses.length === 0 &&
      missingBaselineFailures.length === 0;
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

  if (missingBaselineSuccesses.length) {
    return {
      passed: false,
      kind: 'unclassified_failure',
      baselineFailures: baseline.failures,
      candidateFailures: candidate.failures,
      newFailures: missingBaselineSuccesses,
    };
  }

  if (candidate.exitCode !== baseline.exitCode) {
    return {
      passed: false,
      kind: 'unclassified_failure',
      baselineFailures: baseline.failures,
      candidateFailures: candidate.failures,
      newFailures: candidate.failures,
    };
  }

  const baselineNonTapCounts = countFailures(baseline.nonTapOutput);
  const candidateNonTapCounts = countFailures(candidate.nonTapOutput);
  const addedNonTapOutput = [...candidateNonTapCounts].some(
    ([line, count]) => count > (baselineNonTapCounts.get(line) || 0),
  );
  if (addedNonTapOutput) {
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

  if (
    candidate.cancelledCount > 0 ||
    candidate.skippedCount > baseline.skippedCount ||
    candidate.todoCount > baseline.todoCount
  ) {
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
  const passed =
    newFailures.length === 0 &&
    missingBaselineFailures.length === 0 &&
    parsedCountsMatch;
  const reportedFailures = newFailures.length
    ? newFailures
    : missingBaselineFailures;
  return {
    passed,
    kind: passed
      ? 'baseline_only'
      : newFailures.length
        ? 'new_failures'
        : 'unclassified_failure',
    baselineFailures: baseline.failures,
    candidateFailures: candidate.failures,
    newFailures: reportedFailures,
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
