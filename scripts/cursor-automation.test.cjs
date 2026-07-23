'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const os = require('node:os');

const auto = require('./cursor-automation.cjs');

test('isValidIssueFormat accepts modern bug template', () => {
  assert.equal(
    auto.isValidIssueFormat({
      title: '[Bug] SFTP upload fails on Windows',
      body: [
        '## Describe the problem',
        'Upload fails on large files.',
        '## Steps to reproduce',
        '1. open sftp',
        '2. upload',
        '## Expected behavior',
        'success',
        '## Actual behavior',
        'error',
        '## Operating system',
        'Windows 11',
      ].join('\n'),
    }),
    true,
  );
});

test('isValidIssueFormat rejects short bodies', () => {
  assert.equal(
    auto.isValidIssueFormat({
      title: '[Bug] too short',
      body: 'Steps to reproduce: nope',
    }),
    false,
  );
});

test('normalizeClassification downgrades low-confidence bug_ready', () => {
  const result = auto.normalizeClassification({
    category: 'bug_ready',
    confidence: 0.4,
    summary: 'maybe',
    reasoning: 'unclear',
    reply: 'Need more info please.',
  });
  assert.equal(result.category, 'bug_needs_info');
  assert.equal(result.should_implement, false);
});

test('normalizeClassification keeps high-confidence quick win', () => {
  const result = auto.normalizeClassification({
    category: 'feature_quick_win',
    confidence: 0.9,
    summary: 'small ui tweak',
    reasoning: 'localized',
    reply: 'Working on it.',
  });
  assert.equal(result.category, 'feature_quick_win');
  assert.equal(result.should_implement, true);
});

test('labelsForCategory swaps bug/enhancement correctly', () => {
  const labels = auto.labelsForCategory('bug_ready', [
    'enhancement',
    'needs-triage',
    'user-tag',
  ]);
  assert.ok(labels.includes('bug'));
  assert.ok(labels.includes('ready-for-agent'));
  assert.ok(labels.includes('user-tag'));
  assert.ok(!labels.includes('enhancement'));
  assert.ok(!labels.includes('needs-triage'));
});

test('isFixEligiblePr allows bot marker on same-repo branch', () => {
  const pr = {
    user: { login: 'random-contributor' },
    body: `${auto.BOT_PR_MARKER}\nFixes #1`,
    head: {
      ref: 'cursor/issue-1-99',
      repo: { full_name: 'binaricat/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
    labels: [],
  };
  assert.equal(auto.isFixEligiblePr(pr, { repository: 'binaricat/Netcatty' }), true);
});

test('isFixEligiblePr rejects forks', () => {
  const pr = {
    user: { login: 'binaricat' },
    body: auto.BOT_PR_MARKER,
    head: {
      ref: 'cursor/issue-1-99',
      repo: { full_name: 'someone/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
    labels: ['automation:bot-pr'],
  };
  assert.equal(auto.isFixEligiblePr(pr), false);
});

test('isFixEligiblePr allows maintainer same-repo PRs', () => {
  const pr = {
    user: { login: 'binaricat' },
    body: 'manual pr',
    head: {
      ref: 'feature/foo',
      repo: { full_name: 'binaricat/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
    labels: [],
  };
  assert.equal(auto.isFixEligiblePr(pr), true);
});

test('parseCodexReviewOutcome detects clean summary', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: "Codex Review: Didn't find any major issues. Swish!",
    reviewComments: [],
  });
  assert.equal(outcome.clean, true);
});

test('parseCodexReviewOutcome detects P2 findings', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: 'Codex Review finished',
    reviewComments: [
      {
        body: '**![P2 Badge](https://img.shields.io/badge/P2-yellow)** Null deref',
        path: 'src/a.ts',
      },
    ],
  });
  assert.equal(outcome.clean, false);
});

test('extractJsonObject reads fenced blocks', () => {
  const obj = auto.extractJsonObject(
    'Here you go:\n```json\n{"category":"unclear","confidence":0.9,"summary":"x","reasoning":"y","reply":"please clarify the steps"}\n```\n',
  );
  assert.equal(obj.category, 'unclear');
});

test('hasProtectedChanges flags workflow edits', () => {
  const hits = auto.hasProtectedChanges(
    ' M .github/workflows/cursor-automation.yml\n M components/App.tsx\n',
  );
  assert.deepEqual(hits, ['.github/workflows/cursor-automation.yml']);
});

test('shouldSkipExternalReview matches head sha marker', () => {
  const sha = 'abc123';
  assert.equal(
    auto.shouldSkipExternalReview({
      headSha: sha,
      existingComments: [
        { body: auto.buildExternalReviewComment('ok', sha) },
      ],
    }),
    true,
  );
  assert.equal(
    auto.shouldSkipExternalReview({
      headSha: sha,
      existingComments: [{ body: 'unrelated' }],
    }),
    false,
  );
});

test('getCodexRoundFromComments reads max round', () => {
  assert.equal(
    auto.getCodexRoundFromComments([
      { body: '<!-- cursor-codex-round:1 -->' },
      { body: '<!-- cursor-codex-round:3 -->' },
    ]),
    3,
  );
});

test('parseClassificationFile accepts pure JSON file', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-auto-'));
  const file = path.join(dir, 'c.json');
  fs.writeFileSync(
    file,
    JSON.stringify({
      category: 'bug_needs_info',
      confidence: 0.7,
      summary: 'need logs',
      reasoning: 'missing repro',
      reply: 'Can you share logs?',
    }),
  );
  const parsed = auto.parseClassificationFile(file);
  assert.equal(parsed.category, 'bug_needs_info');
});

test('buildCodexReviewRequestComment includes mention', () => {
  const body = auto.buildCodexReviewRequestComment(2);
  assert.match(body, /@codex review/);
  assert.match(body, /cursor-codex-round:2/);
});
