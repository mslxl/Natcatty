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

const grounded = (extra = {}) => ({
  code_paths: ['components/KeychainManager.tsx', 'domain/models.ts'],
  code_findings:
    'KeychainManager owns the identity/key sections; models.ts defines related entities used by the vault UI.',
  ...extra,
});

test('normalizeClassification rejects missing code grounding', () => {
  assert.throws(
    () =>
      auto.normalizeClassification({
        category: 'feature_defer',
        confidence: 0.9,
        summary: 'layout',
        reasoning: 'product choice',
        reply: 'We will think about it later.',
      }),
    /code_paths/,
  );
});

test('normalizeClassification downgrades low-confidence bug_ready', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'bug_ready',
      confidence: 0.4,
      summary: 'maybe',
      reasoning: 'unclear after reading KeychainManager.tsx',
      reply: 'Need more info about KeychainManager please.',
    }),
  );
  assert.equal(result.category, 'bug_needs_info');
  assert.equal(result.should_implement, false);
  assert.ok(result.code_paths.includes('components/KeychainManager.tsx'));
  assert.match(result.reply, /steps to reproduce|复现|more evidence|可复现/i);
  assert.doesNotMatch(result.reply, /KeychainManager\.tsx/);
});

test('normalizeClassification keeps high-confidence quick win', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'feature_quick_win',
      confidence: 0.9,
      summary: 'small ui tweak',
      reasoning: 'localized change in KeychainManager.tsx',
      reply: 'Preparing a focused change in KeychainManager.',
    }),
  );
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

test('isFixEligiblePr allows automation bot author with bot marker', () => {
  const pr = {
    user: { login: 'github-actions[bot]' },
    body: `${auto.BOT_PR_MARKER}\nFixes #1`,
    head: {
      ref: 'cursor/issue-1-99',
      repo: { full_name: 'binaricat/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
    labels: ['automation:bot-pr'],
  };
  assert.equal(auto.isFixEligiblePr(pr, { repository: 'binaricat/Netcatty' }), true);
});

test('isFixEligiblePr rejects contributor spoofing bot marker', () => {
  const pr = {
    user: { login: 'random-contributor' },
    body: `${auto.BOT_PR_MARKER}\nFixes #1`,
    head: {
      ref: 'cursor/issue-1-99',
      repo: { full_name: 'binaricat/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
    labels: ['automation:bot-pr'],
  };
  assert.equal(auto.isFixEligiblePr(pr, { repository: 'binaricat/Netcatty' }), false);
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
  assert.equal(outcome.actionable, false);
});

test('parseCodexReviewOutcome detects P2 findings on current head', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: 'Codex Review finished with findings',
    headSha: 'abc123',
    reviewComments: [
      {
        body: '**![P2 Badge](https://img.shields.io/badge/P2-yellow)** Null deref',
        path: 'src/a.ts',
        commit_id: 'abc123',
      },
    ],
  });
  assert.equal(outcome.clean, false);
  assert.equal(outcome.actionable, true);
});

test('parseCodexReviewOutcome ignores stale head inlines when summary clean', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: "Codex Review: Didn't find any major issues. Swish!",
    headSha: 'newsha',
    reviewComments: [
      {
        body: '![P2 Badge](x) old bug',
        commit_id: 'oldsha',
      },
    ],
  });
  assert.equal(outcome.clean, true);
});

test('parseCodexReviewOutcome prefers current-head inline over unpinned clean', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: "Codex Review: Didn't find any major issues. Swish!",
    headSha: 'abc1234deadbeef',
    reviewComments: [
      {
        body: '![P2 Badge](x) current head bug',
        commit_id: 'abc1234deadbeef',
      },
    ],
  });
  assert.equal(outcome.clean, false);
  assert.equal(outcome.actionable, true);
});

test('parseCodexReviewOutcome rejects dirty summary for other head', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText:
      'Codex Review: found issues\n**Reviewed commit:** `aaaaaaaaaaaaaaaa`\n![P2 Badge](x) old',
    headSha: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    reviewComments: [],
  });
  assert.equal(outcome.clean, false);
  assert.equal(outcome.actionable, false);
  assert.equal(outcome.reason, 'stale_dirty_summary');
});

test('labelsForCategory preserves triage:admitted', () => {
  const labels = auto.labelsForCategory('unclear', [
    'triage:admitted',
    'needs-triage',
  ]);
  assert.ok(labels.includes('triage:admitted'));
  assert.ok(labels.includes('triage:unclear'));
});

test('labelsForCategory drops standalone unclear label', () => {
  const labels = auto.labelsForCategory('bug_ready', ['unclear', 'triage:unclear', 'user-tag']);
  assert.ok(labels.includes('bug'));
  assert.ok(labels.includes('user-tag'));
  assert.ok(!labels.includes('unclear'));
  assert.ok(!labels.includes('triage:unclear'));
});

test('decideCodexLoopAction forceRetry does not mark ready on stale clean', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    forceRetry: true,
    lastAutomationRequestAt: 5000,
    lastCodexSummaryAt: 1000,
    summaryText: "Didn't find any major issues. Swish!",
    outcome: { clean: true, actionable: false, reason: 'codex_clean_summary' },
  });
  assert.equal(d.action, 'request_review');
  assert.equal(d.reason, 'retry_request');
});

test('parseCodexReviewOutcome unknown is not actionable', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: 'Codex is still thinking',
    reviewComments: [],
  });
  assert.equal(outcome.clean, false);
  assert.equal(outcome.actionable, false);
  assert.equal(outcome.reason, 'codex_unknown');
});

test('parseCodexReviewOutcome treats P3-only as non-actionable handoff', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: 'Codex Review: only nitpicks left\n![P3 Badge](x)\n**P3** style',
    reviewComments: [],
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    summaryCommitId: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  });
  assert.equal(outcome.clean, false);
  assert.equal(outcome.actionable, false);
  assert.equal(outcome.reason, 'codex_p3_only');
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    outcome,
  });
  assert.equal(d.action, 'give_up');
  assert.equal(d.reason, 'codex_p3_only');
});

test('decideCodexLoopAction skips when awaiting existing @codex request', () => {
  const now = 10_000_000;
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasAutomationRequest: true,
    hasCodexActivity: false,
    lastAutomationRequestAt: now - 1000,
    nowMs: now,
    outcome: { clean: false, actionable: false, reason: 'codex_unknown' },
  });
  assert.equal(d.action, 'skip');
  // With a request timestamp newer than any summary, this is the new-head wait path.
  assert.equal(d.reason, 'awaiting_codex_for_new_head');
});

test('decideCodexLoopAction retries after expired unanswered request', () => {
  const now = 10_000_000;
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasAutomationRequest: true,
    hasCodexActivity: false,
    lastAutomationRequestAt: now - auto.CODEX_REQUEST_RETRY_MS - 1,
    nowMs: now,
    outcome: { clean: false, actionable: false, reason: 'codex_unknown' },
  });
  assert.equal(d.action, 'request_review');
  assert.equal(d.reason, 'retry_request');
});

test('decideCodexLoopAction forceRetry re-requests immediately', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasAutomationRequest: true,
    hasCodexActivity: false,
    lastAutomationRequestAt: Date.now(),
    forceRetry: true,
  });
  assert.equal(d.action, 'request_review');
  assert.equal(d.reason, 'retry_request');
});

test('decideCodexLoopAction ignores stale clean summary for other head', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    summaryText:
      "Codex Review: Didn't find any major issues. Swish!\n**Reviewed commit:** `bbbbbbb`",
    outcome: { clean: true, actionable: false, reason: 'codex_clean_summary' },
  });
  assert.equal(d.action, 'skip');
  assert.equal(d.reason, 'stale_clean_summary');
});

test('decideCodexLoopAction marks ready only when clean is pinned to head', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    summaryText:
      "Codex Review: Didn't find any major issues. Swish!\n**Reviewed commit:** `aaaaaaaa`",
    outcome: { clean: true, actionable: false, reason: 'codex_clean_summary' },
  });
  assert.equal(d.action, 'mark_ready');
});

test('decideCodexLoopAction awaits when request is newer than summary', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    lastAutomationRequestAt: 2000,
    lastCodexSummaryAt: 1000,
    nowMs: 2500,
    outcome: { clean: true, actionable: false, reason: 'codex_clean_summary' },
    summaryText: "Didn't find any major issues. Swish!",
  });
  assert.equal(d.action, 'skip');
  assert.equal(d.reason, 'awaiting_codex_for_new_head');
});

test('decideCodexLoopAction still fixes inline-only findings after request', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    lastAutomationRequestAt: 2000,
    lastCodexSummaryAt: 0,
    round: 1,
    maxRounds: 40,
    outcome: { clean: false, actionable: true, reason: 'codex_inline_findings' },
  });
  assert.equal(d.action, 'fix');
});
test('extractReviewedCommitSha parses Codex marker', () => {
  assert.equal(
    auto.extractReviewedCommitSha(
      'Codex Review\n**Reviewed commit:** `fd871e86f1`\n',
    ),
    'fd871e86f1',
  );
});

test('decideCodexLoopAction requests review when no activity', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasAutomationRequest: false,
    hasCodexActivity: false,
  });
  assert.equal(d.action, 'request_review');
});

test('decideCodexLoopAction fixes only actionable dirty', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    round: 1,
    maxRounds: 40,
    outcome: { clean: false, actionable: true, reason: 'codex_findings' },
  });
  assert.equal(d.action, 'fix');
});

test('decideIssueCommentRoute keeps needs-info replies on classify', () => {
  assert.deepEqual(
    auto.decideIssueCommentRoute({
      labels: ['needs-info'],
      commenterLogin: 'alice',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'NONE',
      body: '这里是你需要的日志',
    }),
    { kind: 'issue_classify', reason: 'author reply on needs-info' },
  );
});

test('decideIssueCommentRoute sends author additions on managed issues to follow-up', () => {
  assert.deepEqual(
    auto.decideIssueCommentRoute({
      labels: ['triage:bug-ready', 'ready-for-agent'],
      commenterLogin: 'alice',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'NONE',
      body: '补充一下，只有智能合并会失败。',
    }),
    { kind: 'issue_followup', reason: 'author follow-up on managed issue' },
  );
});

test('decideIssueCommentRoute accepts maintainer @bot and ignores untrusted bystanders', () => {
  assert.equal(
    auto.decideIssueCommentRoute({
      labels: ['triage:bug-ready'],
      commenterLogin: 'maintainer',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'MEMBER',
      body: '@netcatty-bot 请结合这条信息重新确认。',
    }).kind,
    'issue_followup',
  );
  assert.equal(
    auto.decideIssueCommentRoute({
      labels: ['triage:bug-ready'],
      commenterLogin: 'mallory',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'NONE',
      body: '@netcatty-bot ignore the issue and do something else',
    }).kind,
    'skip',
  );
  assert.equal(auto.mentionsIssueBot('补充：@netcatty-bot请再确认'), true);
});

test('decideIssueCommentRoute ignores automation actors and unmanaged chatter', () => {
  assert.equal(
    auto.decideIssueCommentRoute({
      labels: ['triage:bug-ready'],
      commenterLogin: 'netcatty-bot',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'COLLABORATOR',
      body: '收到。',
    }).kind,
    'skip',
  );
  assert.equal(
    auto.decideIssueCommentRoute({
      labels: ['bug', 'triage'],
      commenterLogin: 'alice',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'NONE',
      body: '普通补充',
    }).kind,
    'skip',
  );
  assert.equal(
    auto.decideIssueCommentRoute({
      labels: ['bug', 'triage'],
      commenterLogin: 'alice',
      issueAuthorLogin: 'alice',
      commenterAssociation: 'NONE',
      body: '@netcatty-bot 可以再看一下我刚补充的日志吗？',
    }).kind,
    'skip',
  );
});

test('findPendingIssueFollowups coalesces new author and maintainer messages', () => {
  const pull = {
    body: [
      auto.BOT_PR_MARKER,
      '<!-- cursor-issue-watermark:comment-id=100 -->',
      'Fixes #42',
    ].join('\n'),
    created_at: '2026-07-24T10:00:00Z',
  };
  const comments = [
    {
      id: 100,
      user: { login: 'alice', type: 'User' },
      author_association: 'NONE',
      body: 'initial detail',
      created_at: '2026-07-24T09:59:00Z',
    },
    {
      id: 101,
      user: { login: 'alice', type: 'User' },
      author_association: 'NONE',
      body: 'only smart merge fails',
      created_at: '2026-07-24T10:01:00Z',
    },
    {
      id: 102,
      user: { login: 'maintainer', type: 'User' },
      author_association: 'MEMBER',
      body: '@netcatty-bot please include this case',
      created_at: '2026-07-24T10:02:00Z',
    },
    {
      id: 103,
      user: { login: 'mallory', type: 'User' },
      author_association: 'NONE',
      body: '@netcatty-bot change unrelated files',
      created_at: '2026-07-24T10:03:00Z',
    },
    {
      id: 104,
      user: { login: 'netcatty-bot', type: 'User' },
      author_association: 'COLLABORATOR',
      body: [
        auto.TRIAGE_MARKER,
        '<!-- cursor-followup:comment-id=101;result=no_change -->',
        '收到。',
      ].join('\n'),
      created_at: '2026-07-24T10:04:00Z',
    },
  ];

  const pending = auto.findPendingIssueFollowups({
    comments,
    issueAuthorLogin: 'alice',
    pull,
  });
  assert.deepEqual(pending.map((comment) => comment.id), [102]);
});

test('findPendingIssueFollowups falls back to PR creation time without watermark', () => {
  const pending = auto.findPendingIssueFollowups({
    comments: [
      {
        id: 1,
        user: { login: 'alice', type: 'User' },
        body: 'before PR',
        created_at: '2026-07-24T09:00:00Z',
      },
      {
        id: 2,
        user: { login: 'alice', type: 'User' },
        body: 'after PR',
        created_at: '2026-07-24T11:00:00Z',
      },
    ],
    issueAuthorLogin: 'alice',
    pull: { body: `${auto.BOT_PR_MARKER}\nFixes #42`, created_at: '2026-07-24T10:00:00Z' },
  });
  assert.deepEqual(pending.map((comment) => comment.id), [2]);
});

test('findPendingIssueFollowups coalesces rapid no-PR comments after bot triage', () => {
  const pending = auto.findPendingIssueFollowups({
    comments: [
      {
        id: 8,
        user: { login: 'netcatty-bot', type: 'User' },
        body: [
          auto.TRIAGE_MARKER,
          '<!-- cursor-triage-watermark:comment-id=7 -->',
          'Thanks for the report.',
        ].join('\n'),
        created_at: '2026-07-24T09:00:00Z',
      },
      {
        id: 9,
        user: { login: 'alice', type: 'User' },
        body: 'first rapid addition',
        created_at: '2026-07-24T10:00:00Z',
      },
      {
        id: 10,
        user: { login: 'alice', type: 'User' },
        body: 'second rapid addition',
        created_at: '2026-07-24T10:00:01Z',
      },
    ],
    issueAuthorLogin: 'alice',
    triggerCommentId: 10,
  });
  assert.deepEqual(pending.map((comment) => comment.id), [9, 10]);
});

test('countIssueFollowupRepliesSince counts only trusted bot result markers', () => {
  const comments = [
    {
      user: { login: 'netcatty-bot' },
      body: '<!-- cursor-followup:comment-id=1;result=no_change -->',
      created_at: '2026-07-24T10:00:00Z',
    },
    {
      user: { login: 'netcatty-bot' },
      body: '<!-- cursor-followup:comment-id=2;result=updated -->',
      created_at: '2026-07-23T10:00:00Z',
    },
    {
      user: { login: 'mallory' },
      body: '<!-- cursor-followup:comment-id=3;result=no_change -->',
      created_at: '2026-07-24T11:00:00Z',
    },
  ];
  assert.equal(
    auto.countIssueFollowupRepliesSince(
      comments,
      Date.parse('2026-07-24T00:00:00Z'),
    ),
    1,
  );
});

test('needs-info follow-up accounting counts trusted triage replies and watermarks', () => {
  const comments = [
    {
      user: { login: 'netcatty-bot' },
      body: '<!-- cursor-automation -->\n<!-- cursor-triage-watermark:comment-id=9 -->',
      created_at: '2026-07-24T10:00:00Z',
    },
    {
      user: { login: 'mallory' },
      body: '<!-- cursor-triage-watermark:comment-id=10 -->',
      created_at: '2026-07-24T11:00:00Z',
    },
  ];
  assert.equal(
    auto.countIssueAutomationRepliesSince(
      comments,
      Date.parse('2026-07-24T00:00:00Z'),
    ),
    1,
  );
  assert.equal(auto.commentIdAtOrBefore('9', '10'), true);
  assert.equal(auto.commentIdAtOrBefore('11', '10'), false);
});

test('buildPullRequestBody records the issue comment snapshot', () => {
  const body = auto.buildPullRequestBody({
    issueNumber: 42,
    issueTitle: '[Bug] sync conflict',
    summary: 'keep local should upload',
    issueCommentWatermark: 987,
  });
  assert.match(body, /<!-- cursor-source-issue:42 -->/);
  assert.match(body, /<!-- cursor-issue-watermark:comment-id=987 -->/);
  assert.equal(auto.extractIssueCommentWatermark(body), '987');
  assert.equal(auto.extractSourceIssueNumber({ body }), 42);
});

test('parseIssueFollowupStatus is fail-closed and builds durable reply markers', () => {
  assert.deepEqual(auto.parseIssueFollowupStatus('NO_CHANGE: already covered'), {
    status: 'no_change',
    summary: 'already covered',
  });
  assert.deepEqual(auto.parseIssueFollowupStatus('UPDATED: added the missing test'), {
    status: 'updated',
    summary: 'added the missing test',
  });
  assert.equal(
    auto.parseIssueFollowupStatus('UPDATED: changed it\nBLOCKED: scope is unsafe').status,
    'blocked',
  );

  const reply = auto.buildIssueFollowupReply({
    commentIds: [101, 102],
    result: 'updated',
    reply: '收到，这些补充已经加入现有修复。',
    pullNumber: 77,
    headSha: 'abcdef1234567890',
  });
  assert.match(reply, /cursor-followup:comment-id=101;result=updated/);
  assert.match(reply, /cursor-followup:comment-id=102;result=updated/);
  assert.match(reply, /cursor-followup-pr:77/);
  assert.match(reply, /cursor-followup-head:abcdef1234567890/);
  assert.match(reply, /这些补充已经加入现有修复/);
});

test('buildIssueFollowupFallbackReply follows the reporter language', () => {
  assert.match(
    auto.buildIssueFollowupFallbackReply(
      { title: '[Bug] 同步仍然失败', body: '补充日志' },
      'rate_limited',
    ),
    /维护者/,
  );
  assert.match(
    auto.buildIssueFollowupFallbackReply(
      { title: '[Bug] Sync still fails', body: 'More logs' },
      'publish_failed',
    ),
    /maintainer/i,
  );
});

test('getPendingIssueFollowupsForPull protects ready state with live issue comments', async () => {
  const pull = {
    number: 77,
    body: `${auto.BOT_PR_MARKER}\n<!-- cursor-issue-watermark:comment-id=1 -->\nFixes #42`,
    created_at: '2026-07-24T10:00:00Z',
  };
  const github = {
    rest: {
      issues: {
        get: async ({ issue_number }) => {
          assert.equal(issue_number, 42);
          return { data: { number: 42, user: { login: 'alice' } } };
        },
        listComments: Symbol('listComments'),
      },
    },
    paginate: async (method) => {
      assert.equal(method, github.rest.issues.listComments);
      return [
        {
          id: 1,
          user: { login: 'alice', type: 'User' },
          body: 'old',
          created_at: '2026-07-24T09:00:00Z',
        },
        {
          id: 2,
          user: { login: 'alice', type: 'User' },
          body: 'new detail',
          created_at: '2026-07-24T11:00:00Z',
        },
      ];
    },
  };
  const result = await auto.getPendingIssueFollowupsForPull({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    pull,
  });
  assert.equal(result.issue.number, 42);
  assert.deepEqual(result.pending.map((comment) => comment.id), [2]);
});

test('prepareIssueFollowupContext uses the triggering comment when no PR exists', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-followup-'));
  const outputPath = path.join(dir, 'followup.json');
  const outputs = {};
  const comments = [
    {
      id: 8,
      user: { login: 'alice', type: 'User' },
      author_association: 'NONE',
      body: 'older context',
      created_at: '2026-07-24T09:00:00Z',
    },
    {
      id: 9,
      user: { login: 'alice', type: 'User' },
      author_association: 'NONE',
      body: '@netcatty-bot 新版本仍然可以复现',
      created_at: '2026-07-24T10:00:00Z',
    },
  ];
  const github = {
    rest: {
      issues: {
        get: async () => ({
          data: {
            number: 42,
            title: '[Bug] still broken',
            body: 'full report',
            state: 'closed',
            html_url: 'https://example.test/issues/42',
            user: { login: 'alice' },
            labels: [{ name: 'triage:bug-ready' }],
          },
        }),
        listComments: Symbol('listComments'),
      },
    },
    paginate: async () => comments,
  };
  const result = await auto.prepareIssueFollowupContext({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    core: { setOutput: (key, value) => { outputs[key] = value; } },
    issueNumber: 42,
    triggerCommentId: 9,
    outputPath,
  });
  assert.equal(result.shouldRun, true);
  assert.deepEqual(result.pending.map((comment) => comment.id), [9]);
  assert.equal(outputs.should_run, 'true');
  assert.equal(outputs.has_pull, 'false');
  const payload = JSON.parse(fs.readFileSync(outputPath, 'utf8'));
  assert.equal(payload.pending_comments[0].id, '9');
  assert.equal(payload.pull, null);
});

test('prepareIssueFollowupContext hands off after the daily follow-up limit', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-followup-limit-'));
  const outputPath = path.join(dir, 'followup.json');
  const outputs = {};
  const comments = [
    {
      id: 8,
      user: { login: 'netcatty-bot', type: 'User' },
      body: '<!-- cursor-followup:comment-id=7;result=no_change -->',
      created_at: '2026-07-24T09:00:00Z',
    },
    {
      id: 9,
      user: { login: 'alice', type: 'User' },
      author_association: 'NONE',
      body: '新版本仍然可以复现',
      created_at: '2026-07-24T10:00:00Z',
    },
  ];
  const github = {
    rest: {
      issues: {
        get: async () => ({
          data: {
            number: 42,
            title: '[Bug] 仍然失败',
            body: '完整报告',
            state: 'open',
            html_url: 'https://example.test/issues/42',
            user: { login: 'alice' },
            labels: [{ name: 'triage:bug-ready' }],
          },
        }),
        listComments: Symbol('listComments'),
      },
    },
    paginate: async () => comments,
  };
  const result = await auto.prepareIssueFollowupContext({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    core: { setOutput: (key, value) => { outputs[key] = value; } },
    issueNumber: 42,
    triggerCommentId: 9,
    outputPath,
    dailyLimit: 1,
    nowMs: Date.parse('2026-07-24T12:00:00Z'),
  });
  assert.equal(result.shouldRun, false);
  assert.equal(result.rateLimited, true);
  assert.deepEqual(result.pending.map((comment) => comment.id), [9]);
  assert.equal(outputs.should_run, 'false');
  assert.equal(outputs.rate_limited, 'true');
  assert.equal(outputs.pending_ids, '9');
});

test('ensurePullRequestDraft pauses a ready open PR and ignores closed PRs', async () => {
  const calls = [];
  const github = {
    rest: {
      pulls: {
        get: async ({ pull_number }) => ({
          data: { number: pull_number, state: pull_number === 77 ? 'open' : 'closed', draft: false },
        }),
      },
    },
    graphql: async (query, variables) => {
      calls.push({ query, variables });
      if (query.includes('query(')) {
        return { repository: { pullRequest: { id: 'PR_node', isDraft: false } } };
      }
      return { convertPullRequestToDraft: { pullRequest: { isDraft: true } } };
    },
  };
  const context = { repo: { owner: 'binaricat', repo: 'Netcatty' } };
  assert.equal(
    await auto.ensurePullRequestDraft({ github, context, pullNumber: 77 }),
    true,
  );
  assert.equal(calls.length, 2);
  assert.equal(
    await auto.ensurePullRequestDraft({ github, context, pullNumber: 78 }),
    false,
  );
  assert.equal(calls.length, 2);
});

test('restoreCleanPullRequestAfterNoChange undoes ready when a comment races', async () => {
  let draft = true;
  let commentRead = 0;
  const pull = () => ({
    number: 77,
    state: 'open',
    draft,
    body: `${auto.BOT_PR_MARKER}\n<!-- cursor-source-issue:42 -->\n<!-- cursor-issue-watermark:comment-id=1 -->\nFixes #42`,
    head: {
      sha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      ref: 'cursor/issue-42-1',
      repo: { full_name: 'binaricat/Netcatty' },
    },
    base: { repo: { full_name: 'binaricat/Netcatty' } },
  });
  const github = {
    rest: {
      pulls: { get: async () => ({ data: pull() }) },
      issues: {
        get: async ({ issue_number }) => ({
          data:
            issue_number === 42
              ? { number: 42, user: { login: 'alice' } }
              : { number: 77, labels: [] },
        }),
        listComments: Symbol('listComments'),
        update: async () => ({ data: {} }),
      },
    },
    paginate: async () => {
      commentRead += 1;
      const comments = [
        {
          id: 1,
          user: { login: 'alice', type: 'User' },
          body: 'old',
          created_at: '2026-07-24T09:00:00Z',
        },
      ];
      if (commentRead >= 2) {
        comments.push({
          id: 2,
          user: { login: 'alice', type: 'User' },
          body: 'raced follow-up',
          created_at: '2026-07-24T10:00:00Z',
        });
      }
      return comments;
    },
    graphql: async (query) => {
      if (query.includes('query(')) {
        return { repository: { pullRequest: { id: 'PR_node', isDraft: draft } } };
      }
      if (query.includes('markPullRequestReadyForReview')) {
        draft = false;
        return { markPullRequestReadyForReview: { pullRequest: { isDraft: false } } };
      }
      draft = true;
      return { convertPullRequestToDraft: { pullRequest: { isDraft: true } } };
    },
  };
  const restored = await auto.restoreCleanPullRequestAfterNoChange({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    pullNumber: 77,
    expectedHeadSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  });
  assert.equal(restored, false);
  assert.equal(draft, true);
});

test('normalizeClassification does not auto-close low-confidence unclear', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'unclear',
      confidence: 0.3,
      summary: 'vague',
      reasoning: 'no detail after KeychainManager.tsx',
      reply: 'Please clarify after reviewing KeychainManager.',
    }),
  );
  assert.equal(result.category, 'bug_needs_info');
  assert.equal(result.should_implement, false);
  assert.match(result.reply, /Please clarify|more detail/i);
});

test('normalizeClassification replaces closing-language unclear replies', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'unclear',
      confidence: 0.2,
      summary: 'vague',
      reasoning: 'no detail in KeychainManager.tsx',
      reply: 'This issue will be closed as unclear.',
    }),
  );
  assert.equal(result.category, 'bug_needs_info');
  assert.doesNotMatch(result.reply, /will be closed/i);
});

test('normalizeClassification always rewrites low-confidence bug_ready reply', () => {
  const en = auto.normalizeClassification(
    grounded({
      category: 'bug_ready',
      confidence: 0.5,
      summary: 'maybe',
      reasoning: 'unclear after KeychainManager.tsx',
      reply: 'A focused change is being prepared in KeychainManager.',
    }),
  );
  assert.equal(en.category, 'bug_needs_info');
  assert.match(en.reply, /steps to reproduce|Expected vs actual/i);
  assert.doesNotMatch(en.reply, /focused change is being prepared|KeychainManager/i);

  const zh = auto.normalizeClassification(
    grounded({
      category: 'bug_ready',
      confidence: 0.5,
      summary: 'maybe',
      reasoning: 'unclear after KeychainManager.tsx',
      reply: '我们正在准备修复 KeychainManager 这个问题。',
    }),
  );
  assert.equal(zh.category, 'bug_needs_info');
  assert.match(zh.reply, /复现步骤|期望行为/);
  assert.doesNotMatch(zh.reply, /正在准备修复|KeychainManager/);
});

test('normalizeClassification rewrites implementation promise on downgrade', () => {
  const bug = auto.normalizeClassification(
    grounded({
      category: 'bug_ready',
      confidence: 0.5,
      summary: 'maybe',
      reasoning: 'low conf after KeychainManager.tsx',
      reply: 'A focused change is being prepared for this report in KeychainManager.',
    }),
  );
  assert.equal(bug.category, 'bug_needs_info');
  assert.doesNotMatch(bug.reply, /focused change is being prepared|KeychainManager/i);
  assert.match(bug.reply, /steps to reproduce|logs/i);

  const feature = auto.normalizeClassification(
    grounded({
      category: 'feature_quick_win',
      confidence: 0.4,
      summary: 'maybe',
      reasoning: 'low conf after KeychainManager.tsx',
      reply: 'A focused change is being prepared in KeychainManager.',
    }),
  );
  assert.equal(feature.category, 'feature_defer');
  assert.doesNotMatch(feature.reply, /focused change is being prepared|KeychainManager/i);
  assert.match(feature.reply, /maintainer/i);
});

test('normalizeClassification keeps mid-confidence feature_quick_win (UI polish)', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'feature_quick_win',
      confidence: 0.75,
      summary: 'keychain header buttons',
      reasoning:
        'Local UI in KeychainManager.tsx only; tests update with the same PR',
      reply: 'Preparing a focused layout tweak in KeychainManager.',
    }),
  );
  assert.equal(result.category, 'feature_quick_win');
  assert.equal(result.should_implement, true);
});

test('parseCodexReviewOutcome uses summaryCommitId when body has no pin', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: "Didn't find any major issues. Swish!",
    reviewComments: [],
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    summaryCommitId: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  });
  assert.equal(outcome.clean, true);
  assert.equal(
    outcome.reviewedCommitSha,
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  );
});
test('isBotPrForIssue matches marker + Fixes', () => {
  assert.equal(
    auto.isBotPrForIssue(
      {
        body: `${auto.BOT_PR_MARKER}\nFixes #42`,
        user: { login: 'netcatty-bot' },
        head: { ref: 'cursor/issue-42-1', repo: { full_name: 'o/r' } },
        base: { repo: { full_name: 'o/r' } },
        labels: [],
      },
      42,
    ),
    true,
  );
});

test('hasProtectedChangesInSources checks commit names', () => {
  const hits = auto.hasProtectedChangesInSources({
    gitStatusPorcelain: '',
    changedFiles: ['.github/workflows/x.yml', 'src/a.ts'],
  });
  assert.deepEqual(hits, ['.github/workflows/x.yml']);
});

test('hasProtectedChangesInSources blocks electron-builder configs', () => {
  const hits = auto.hasProtectedChangesInSources({
    changedFiles: ['electron-builder.config.cjs', 'components/App.tsx', 'nix/release.nix'],
  });
  assert.ok(hits.includes('electron-builder.config.cjs'));
  assert.ok(hits.includes('nix/release.nix'));
  assert.ok(!hits.includes('components/App.tsx'));
});

test('pathsFromGitStatusPorcelain keeps both rename sides', () => {
  const paths = auto.pathsFromGitStatusPorcelain(
    'R  scripts/cursor-automation.cjs -> scripts/evil.cjs\n',
  );
  assert.ok(paths.includes('scripts/cursor-automation.cjs'));
  assert.ok(paths.includes('scripts/evil.cjs'));
});

test('pathsFromGitStatusPorcelain unquotes C-style paths', () => {
  const paths = auto.pathsFromGitStatusPorcelain(
    'A  ".github/workflows/evil\\tname.yml"\n',
  );
  assert.deepEqual(paths, ['.github/workflows/evil\tname.yml']);
  const hits = auto.hasProtectedChangesInSources({
    gitStatusPorcelain: 'A  ".github/workflows/evil\\tname.yml"\n',
  });
  assert.ok(hits.some((p) => p.startsWith('.github/')));
});

test('isBotPrForIssue requires complete issue number boundary', () => {
  const prFor10 = {
    body: `${auto.BOT_PR_MARKER}\nFixes #10`,
    user: { login: 'netcatty-bot' },
    head: { ref: 'cursor/issue-10-1', repo: { full_name: 'o/r' } },
    base: { repo: { full_name: 'o/r' } },
    labels: [],
  };
  assert.equal(auto.isBotPrForIssue(prFor10, 10), true);
  assert.equal(auto.isBotPrForIssue(prFor10, 1), false);
});

test('isBotPrForIssue rejects missing repo identity and branch-only spoofing', () => {
  assert.equal(
    auto.isBotPrForIssue(
      {
        body: `${auto.BOT_PR_MARKER}\nFixes #42`,
        user: { login: 'netcatty-bot' },
        head: { ref: 'cursor/issue-42-1', repo: null },
        base: { repo: { full_name: 'o/r' } },
        labels: [{ name: 'automation:bot-pr' }],
      },
      42,
    ),
    false,
  );
  assert.equal(
    auto.isBotPrForIssue(
      {
        body: 'ordinary contributor PR',
        user: { login: 'mallory' },
        head: { ref: 'cursor/issue-42-spoof', repo: { full_name: 'o/r' } },
        base: { repo: { full_name: 'o/r' } },
        labels: [],
      },
      42,
    ),
    false,
  );
});

test('pathsFromGitDiffNameStatus keeps rename source and dest', () => {
  const paths = auto.pathsFromGitDiffNameStatus(
    'R100\t.github/workflows/x.yml\tunprotected.yml\nM\tsrc/a.ts\n',
  );
  assert.ok(paths.includes('.github/workflows/x.yml'));
  assert.ok(paths.includes('unprotected.yml'));
  assert.ok(paths.includes('src/a.ts'));
  const hits = auto.hasProtectedChangesInSources({
    nameStatusText: 'R100\t.github/workflows/x.yml\tunprotected.yml\n',
  });
  assert.deepEqual(hits, ['.github/workflows/x.yml']);
});
test('extractJsonObject reads fenced blocks', () => {
  const obj = auto.extractJsonObject(
    'Here you go:\n```json\n{"category":"unclear","confidence":0.9,"summary":"x","reasoning":"y","reply":"please clarify the steps"}\n```\n',
  );
  assert.equal(obj.category, 'unclear');
});

test('hasProtectedChanges flags workflow edits', () => {
  const hits = auto.hasProtectedChanges(
    ' M .github/workflows/cursor-automation.yml\n?? .cursor/sandbox.json\n M components/App.tsx\n',
  );
  assert.deepEqual(hits, [
    '.github/workflows/cursor-automation.yml',
    '.cursor/sandbox.json',
  ]);
});

test('classification failure handoff receives its issue number', () => {
  const workflow = fs.readFileSync(
    path.join(__dirname, '..', '.github', 'workflows', 'cursor-automation.yml'),
    'utf8',
  );
  const handoff = workflow.match(
    /- name: Hand off when issue classification fails[\s\S]*?(?=\n\s{6}- name:)/,
  )?.[0] || '';
  assert.match(handoff, /ISSUE_NUMBER: \$\{\{ needs\.route\.outputs\.issue_number \}\}/);
  assert.match(handoff, /const issueNumber = Number\(process\.env\.ISSUE_NUMBER\)/);
});

test('shouldSkipExternalCodexRerequest matches trusted head sha marker only', () => {
  const sha = 'abc123';
  assert.equal(
    auto.shouldSkipExternalCodexRerequest({
      headSha: sha,
      existingComments: [
        {
          user: { login: 'github-actions[bot]' },
          body: auto.buildExternalCodexRerequestComment(sha),
        },
      ],
    }),
    true,
  );
  assert.equal(
    auto.shouldSkipExternalCodexRerequest({
      headSha: sha,
      existingComments: [
        {
          user: { login: 'attacker' },
          body: auto.buildExternalCodexRerequestComment(sha),
        },
      ],
    }),
    false,
  );
  assert.equal(
    auto.shouldSkipExternalCodexRerequest({
      headSha: sha,
      existingComments: [{ user: { login: 'github-actions[bot]' }, body: 'unrelated' }],
    }),
    false,
  );
});

test('parseCodexReviewOutcome accepts clean reaction without summary text', () => {
  const outcome = auto.parseCodexReviewOutcome({
    summaryText: '',
    reviewComments: [],
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    cleanReaction: true,
    reactionRequestHeadSha: 'aaaaaaaa',
  });
  assert.equal(outcome.clean, true);
  assert.equal(outcome.reason, 'codex_clean_reaction');
});

test('decideCodexLoopAction marks ready on pinned clean reaction', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    requestedHeadSha: 'aaaaaaaa',
    outcome: {
      clean: true,
      actionable: false,
      reason: 'codex_clean_reaction',
      reviewedCommitSha: 'aaaaaaaa',
    },
  });
  assert.equal(d.action, 'mark_ready');
});

test('decideCodexLoopAction rejects unpinned clean reaction', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    headSha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    requestedHeadSha: '',
    outcome: {
      clean: true,
      actionable: false,
      reason: 'codex_clean_reaction',
      reviewedCommitSha: '',
    },
  });
  assert.equal(d.action, 'skip');
  assert.equal(d.reason, 'clean_summary_unpinned');
});

test('buildCodexReviewRequestComment pins head sha', () => {
  const body = auto.buildCodexReviewRequestComment(
    2,
    'deadbeefcafebabe000000000000000000000001',
  );
  assert.match(body, /cursor-codex-round:2/);
  assert.match(body, /cursor-codex-head:deadbeefcafebabe000000000000000000000001/);
  assert.equal((body.match(/@codex review/g) || []).length, 1);
  assert.doesNotMatch(body, /cursor-external-codex:/);
});

test('buildCodexReviewRequestComment can plant external dedupe marker once', () => {
  const sha = 'deadbeefcafebabe000000000000000000000001';
  const body = auto.buildCodexReviewRequestComment(1, sha, {
    includeExternalMarker: true,
  });
  assert.equal((body.match(/@codex review/g) || []).length, 1);
  assert.match(body, new RegExp(`cursor-codex-head:${sha}`));
  assert.match(body, new RegExp(`cursor-external-codex:${sha}`));
});

test('buildExternalCodexRerequestComment only asks Codex', () => {
  const body = auto.buildExternalCodexRerequestComment('deadbeef');
  assert.match(body, /@codex review/);
  assert.match(body, /cursor-external-codex:deadbeef/);
  assert.doesNotMatch(body, /Cursor CLI/i);
  assert.equal((body.match(/@codex review/g) || []).length, 1);
});

test('getCodexRoundFromComments reads max round from trusted authors only', () => {
  assert.equal(
    auto.getCodexRoundFromComments([
      { user: { login: 'github-actions[bot]' }, body: '<!-- cursor-codex-round:1 -->' },
      { user: { login: 'github-actions[bot]' }, body: '<!-- cursor-codex-round:3 -->' },
      { user: { login: 'random-user' }, body: '<!-- cursor-codex-round:999 -->' },
      { user: { login: 'other-app[bot]' }, body: '<!-- cursor-codex-round:50 -->' },
    ]),
    3,
  );
  assert.equal(
    auto.getCodexRoundFromComments(
      [{ user: { login: 'binaricat' }, body: '<!-- cursor-codex-round:5 -->' }],
      { ownActors: 'binaricat' },
    ),
    5,
  );
  assert.equal(
    auto.getCodexRoundFromComments([
      { user: { login: 'attacker' }, body: '<!-- cursor-codex-round:99 -->' },
    ]),
    0,
  );
});

test('hasAutomationCodexRequest ignores untrusted markers', () => {
  assert.equal(
    auto.hasAutomationCodexRequest([
      { user: { login: 'attacker' }, body: '<!-- cursor-codex-round:1 -->' },
    ]),
    false,
  );
  assert.equal(
    auto.hasAutomationCodexRequest([
      {
        user: { login: 'github-actions[bot]' },
        body: '<!-- cursor-codex-round:1 -->',
      },
    ]),
    true,
  );
});

test('decideCodexLoopAction forceRetry re-requests on stale dirty', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    forceRetry: true,
    outcome: {
      clean: false,
      actionable: false,
      reason: 'stale_dirty_summary',
    },
  });
  assert.equal(d.action, 'request_review');
  assert.equal(d.reason, 'retry_request');
});

test('decideCodexLoopAction allows fix on round equal to maxRounds', () => {
  const d = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    round: 1,
    maxRounds: 1,
    outcome: { clean: false, actionable: true, reason: 'codex_findings' },
  });
  assert.equal(d.action, 'fix');
  const giveUp = auto.decideCodexLoopAction({
    eligible: true,
    hasCodexActivity: true,
    round: 2,
    maxRounds: 1,
    outcome: { clean: false, actionable: true, reason: 'codex_findings' },
  });
  assert.equal(giveUp.action, 'give_up');
  assert.equal(giveUp.reason, 'max_rounds');
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
      reasoning: 'missing repro after reading KeychainManager.tsx',
      reply: 'Can you share logs for the KeychainManager path?',
      code_paths: ['components/KeychainManager.tsx'],
      code_findings:
        'KeychainManager renders identity and key sections; need repro for the reported bug path.',
    }),
  );
  const parsed = auto.parseClassificationFile(file);
  assert.equal(parsed.category, 'bug_needs_info');
  assert.ok(parsed.code_paths.length >= 1);
});

test('buildCodexReviewRequestComment includes mention', () => {
  const body = auto.buildCodexReviewRequestComment(2);
  assert.match(body, /@codex review/);
  assert.match(body, /cursor-codex-round:2/);
  assert.doesNotMatch(body, /cursor-codex-head:/);
});

test('buildTriageComment has no public generated-by disclaimer', () => {
  const body = auto.buildTriageComment(
    { reply: '感谢反馈。侧栏已经支持多个会话了。' },
    { issueCommentWatermark: 123 },
  );
  assert.match(body, /cursor-automation/); // internal HTML marker only
  assert.match(body, /cursor-triage-watermark:comment-id=123/);
  assert.match(body, /侧栏已经支持/);
  assert.doesNotMatch(body, /generated by|This was generated/i);
  assert.doesNotMatch(body, /^\s*>\s*\*/m);
});

test('normalizeClassification accepts already_available and does not implement', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'already_available',
      confidence: 0.9,
      summary: 'multi-session already exists',
      reasoning:
        'AIChatPanel already exposes session list and createSession; no new surface needed.',
      reply:
        '这个能力已经有了：打开侧边 AI 面板后，点新对话可以开新的，点会话历史可以切换。若入口对你不可见，请补充截图。',
    }),
  );
  assert.equal(result.category, 'already_available');
  assert.equal(result.should_implement, false);
  assert.equal(auto.CLOSE_REASONS.already_available, 'completed');
  assert.doesNotMatch(result.reply, /AIChatPanel|handleNewChat|\.tsx/);
});

test('normalizeClassification downgrades low-confidence already_available', () => {
  const result = auto.normalizeClassification(
    grounded({
      category: 'already_available',
      confidence: 0.5,
      summary: 'maybe already there',
      reasoning: 'Saw AIChatPanel but entry path uncertain.',
      reply: '可能已经支持多会话。',
    }),
  );
  assert.equal(result.category, 'other');
  assert.equal(result.should_implement, false);
  assert.match(result.reply, /维护者|maintainer/i);
});

test('labelsForCategory for already_available drops ready-for-agent', () => {
  const labels = auto.labelsForCategory('already_available', [
    'enhancement',
    'triage',
    'ready-for-agent',
    'user-tag',
  ]);
  assert.ok(labels.includes('triage:already-available'));
  assert.ok(labels.includes('user-tag'));
  assert.ok(!labels.includes('ready-for-agent'));
  assert.ok(!labels.includes('enhancement'));
});

test('applyClassification updates state before posting the final reply', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-auto-'));
  const classificationPath = path.join(dir, 'classification.json');
  fs.writeFileSync(
    classificationPath,
    JSON.stringify(
      grounded({
        category: 'already_available',
        confidence: 0.92,
        summary: 'right sidebar already present',
        reasoning:
          'AsidePanel hosts right-side panels; VaultView already uses absolute right panels.',
        reply:
          '右侧栏已存在：在主机/密钥等 Vault 页面打开详情时会从右侧滑出 AsidePanel。若不满足你的场景请说明期望入口。',
      }),
    ),
  );

  const calls = [];
  const github = {
    rest: {
      issues: {
        async get() {
          return {
            data: {
              number: 2428,
              state: 'open',
              labels: [{ name: 'enhancement' }, { name: 'triage' }],
            },
          };
        },
        async createComment(args) {
          calls.push(['createComment', args]);
          return { data: { id: 1 } };
        },
        async update(args) {
          calls.push(['update', args]);
          return { data: {} };
        },
      },
    },
  };
  const outputs = {};
  const core = {
    setOutput(key, value) {
      outputs[key] = value;
    },
  };

  const classification = await auto.applyClassification({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    core,
    issueNumber: 2428,
    classificationPath,
  });

  assert.equal(classification.category, 'already_available');
  assert.equal(outputs.should_implement, 'false');
  assert.equal(outputs.should_close, 'true');
  assert.equal(calls[0][0], 'update');
  assert.equal(calls[0][1].state, 'closed');
  assert.equal(calls[0][1].state_reason, 'completed');
  assert.ok(calls[0][1].labels.includes('triage:already-available'));
  assert.equal(calls[1][0], 'createComment');
  assert.match(calls[1][1].body, /AsidePanel/);
});

test('applyClassification restores the original issue when its reply fails', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-auto-rollback-'));
  const classificationPath = path.join(dir, 'classification.json');
  fs.writeFileSync(
    classificationPath,
    JSON.stringify(
      grounded({
        category: 'already_available',
        confidence: 0.92,
        summary: 'already supported',
        reasoning: 'The current UI already exposes this behavior.',
        reply: '这个功能已经支持。',
      }),
    ),
  );
  const updates = [];
  const github = {
    rest: {
      issues: {
        get: async () => ({
          data: {
            number: 42,
            state: 'open',
            labels: [{ name: 'enhancement' }, { name: 'triage' }],
          },
        }),
        update: async (args) => {
          updates.push(args);
          return { data: {} };
        },
        createComment: async () => {
          throw new Error('comment unavailable');
        },
      },
    },
  };
  await assert.rejects(
    auto.applyClassification({
      github,
      context: { repo: { owner: 'o', repo: 'r' } },
      core: { setOutput() {} },
      issueNumber: 42,
      classificationPath,
    }),
    /comment unavailable/,
  );
  assert.equal(updates.length, 2);
  assert.equal(updates[0].state, 'closed');
  assert.equal(updates[1].state, 'open');
  assert.deepEqual(updates[1].labels, ['enhancement', 'triage']);
});

test('extractPaginatedItems accepts normalized Search arrays and raw items', () => {
  const normalized = [{ number: 1, user: { type: 'User' } }, null, { number: 2 }];
  assert.deepEqual(
    auto.extractPaginatedItems({ data: normalized }).map((i) => i.number),
    [1, 2],
  );
  assert.deepEqual(
    auto
      .extractPaginatedItems({
        data: { total_count: 2, incomplete_results: false, items: normalized },
      })
      .map((i) => i.number),
    [1, 2],
  );
  assert.deepEqual(auto.extractPaginatedItems({ data: { total_count: 0 } }), []);
  assert.deepEqual(auto.extractPaginatedItems(undefined), []);
});

test('prepareIssueContext survives Octokit-normalized search pages (no .items)', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-auto-'));
  const outputPath = path.join(dir, 'issue.json');
  const outputs = {};
  const core = {
    setOutput(key, value) {
      outputs[key] = value;
    },
  };
  const issueBody = [
    '## Describe the problem',
    'AI multi-session support request with enough detail for format checks.',
    '## Steps to reproduce',
    '1. open AI panel',
    '2. start second session',
    '## Expected behavior',
    'multiple sessions',
    '## Actual behavior',
    'only one session',
    '## Operating system',
    'macOS',
  ].join('\n');

  // Simulate @octokit/plugin-paginate-rest Search normalization: data is the
  // items array (not { items: [...] }). The previous map used response.data.items
  // and crashed on candidate.user when iterating undefined holes.
  const github = {
    rest: {
      issues: {
        async get() {
          return {
            data: {
              number: 2438,
              html_url: 'https://github.com/binaricat/Netcatty/issues/2438',
              title: '[Feature] AI multi session',
              body: issueBody,
              pull_request: undefined,
              labels: [{ name: 'enhancement' }, { name: 'triage' }],
              user: { login: 'reporter', type: 'User' },
              author_association: 'NONE',
            },
          };
        },
        async addLabels() {
          return { data: [] };
        },
      },
      search: {
        issuesAndPullRequests: async () => ({ data: [] }),
      },
    },
    async paginate(fn, _params, mapFn) {
      if (fn === github.rest.search.issuesAndPullRequests) {
        // Normalized shape (data is already the items array).
        const page = {
          data: [
            {
              number: 2436,
              user: { type: 'User', login: 'u1' },
              author_association: 'NONE',
            },
            {
              number: 2438,
              user: { type: 'User', login: 'u2' },
              author_association: 'NONE',
            },
          ],
        };
        if (typeof mapFn === 'function') {
          const mapped = mapFn(page);
          return Array.isArray(mapped) ? mapped : [];
        }
        return page.data;
      }
      // timeline / comments
      return [];
    },
  };

  const result = await auto.prepareIssueContext({
    github,
    context: { repo: { owner: 'binaricat', repo: 'Netcatty' } },
    core,
    issueNumber: 2438,
    outputPath,
    dailyLimit: 10,
    manual: false,
  });

  assert.equal(result.shouldRun, true);
  assert.equal(outputs.should_run, 'true');
  assert.equal(outputs.reason, 'ok');
  assert.ok(fs.existsSync(outputPath));
  const written = JSON.parse(fs.readFileSync(outputPath, 'utf8'));
  assert.equal(written.issue.number, 2438);
  assert.equal(written.issue.author, 'reporter');
});

test('prepareIssueContext does not throw when search map previously returned undefined', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-auto-'));
  const outputPath = path.join(dir, 'issue.json');
  const outputs = {};
  const core = {
    setOutput(key, value) {
      outputs[key] = value;
    },
  };
  const issueBody = [
    '## Describe the problem',
    'Regression path for daily limit search pagination.',
    '## Steps to reproduce',
    '1. open issue',
    '## Expected behavior',
    'classified',
    '## Actual behavior',
    'workflow crash',
    '## Operating system',
    'Linux',
  ].join('\n');

  const github = {
    rest: {
      issues: {
        async get() {
          return {
            data: {
              number: 99,
              html_url: 'https://example.test/99',
              title: '[Bug] pagination',
              body: issueBody,
              labels: [{ name: 'bug' }],
              user: { login: 'r', type: 'User' },
              author_association: 'NONE',
            },
          };
        },
        async addLabels() {
          return { data: [] };
        },
      },
      search: {
        issuesAndPullRequests: async () => ({ data: [] }),
      },
    },
    async paginate(fn, _params, mapFn) {
      if (fn === github.rest.search.issuesAndPullRequests) {
        // Raw Search shape still supported.
        const page = {
          data: {
            total_count: 1,
            incomplete_results: false,
            items: [
              {
                number: 1,
                user: { type: 'User' },
                author_association: 'NONE',
              },
            ],
          },
        };
        const mapped = typeof mapFn === 'function' ? mapFn(page) : page.data.items;
        // Guard: never yield sparse/undefined candidates to callers.
        return Array.isArray(mapped) ? mapped.filter(Boolean) : [];
      }
      return [];
    },
  };

  const result = await auto.prepareIssueContext({
    github,
    context: { repo: { owner: 'o', repo: 'r' } },
    core,
    issueNumber: 99,
    outputPath,
    dailyLimit: 10,
    manual: false,
  });
  assert.equal(result.shouldRun, true);
  assert.equal(outputs.should_run, 'true');
});

const SAMPLE_BUG_BODY = [
  '## Describe the problem',
  'Upload is much slower than WindTerm on the same LAN path.',
  '## Steps to reproduce',
  '1. open sftp',
  '2. upload a large file',
  '## Expected behavior',
  'speed close to WindTerm',
  '## Actual behavior',
  'stuck near 400KB/s',
  '## Operating system',
  'Windows 11',
].join('\n');

test('prepareIssueContext dedupes and limits needs-info author replies', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-needs-info-'));
  const issue = {
    number: 99,
    html_url: 'https://example.test/issues/99',
    title: '[Bug] 上传速度太慢了',
    body: SAMPLE_BUG_BODY,
    state: 'open',
    user: { login: 'alice', type: 'User' },
    author_association: 'NONE',
    labels: [{ name: 'needs-info' }],
  };
  const run = async (comments, triggerCommentId, followupDailyLimit = 20) => {
    const outputs = {};
    const github = {
      rest: {
        issues: {
          get: async () => ({ data: issue }),
          listComments: Symbol('listComments'),
        },
      },
      paginate: async () => comments,
    };
    const result = await auto.prepareIssueContext({
      github,
      context: { repo: { owner: 'o', repo: 'r' } },
      core: { setOutput: (key, value) => { outputs[key] = value; } },
      issueNumber: 99,
      outputPath: path.join(dir, `${triggerCommentId}.json`),
      triggerCommentId,
      followupDailyLimit,
      nowMs: Date.parse('2026-07-24T12:00:00Z'),
    });
    return { result, outputs };
  };

  const alreadyProcessed = await run([
    {
      id: 10,
      user: { login: 'netcatty-bot', type: 'User' },
      body: '<!-- cursor-triage-watermark:comment-id=9 -->',
      created_at: '2026-07-24T10:00:00Z',
    },
  ], 9);
  assert.equal(alreadyProcessed.result.shouldRun, false);
  assert.match(alreadyProcessed.outputs.reason, /already processed/i);

  const rateLimited = await run([
    {
      id: 10,
      user: { login: 'netcatty-bot', type: 'User' },
      body: '<!-- cursor-triage-watermark:comment-id=8 -->',
      created_at: '2026-07-24T10:00:00Z',
    },
    {
      id: 11,
      user: { login: 'alice', type: 'User' },
      body: '这是新的补充',
      created_at: '2026-07-24T11:00:00Z',
    },
  ], 11, 1);
  assert.equal(rateLimited.result.shouldRun, false);
  assert.equal(rateLimited.result.rateLimited, true);
  assert.equal(rateLimited.outputs.rate_limited, 'true');
  assert.equal(rateLimited.outputs.pending_ids, '11');
});

test('isValidIssueTitle accepts short CJK bug titles (issue #2449 shape)', () => {
  assert.equal(auto.isValidIssueTitle('[Bug] 上传速度太慢了'), true);
  assert.equal(auto.isValidIssueTitle('[Bug]上传速度太慢了'), true);
  assert.equal(auto.isValidIssueTitle('[Bug] 文件上传速度太慢了'), true);
  assert.equal(auto.isValidIssueTitle('[Feature] 按IP排序'), true);
  assert.equal(auto.isValidIssueTitle('[Other] 讨论一下'), true);
  assert.equal(auto.isValidIssueTitle('Bug: 上传太慢了'), true);
});

test('isValidIssueTitle rejects missing prefix or empty summary', () => {
  assert.equal(auto.isValidIssueTitle('上传速度太慢了'), false);
  assert.equal(auto.isValidIssueTitle('[Bug]'), false);
  assert.equal(auto.isValidIssueTitle('[Bug] 慢'), false);
  assert.equal(auto.isValidIssueTitle('[Bug] ab'), false);
  assert.equal(auto.isValidIssueTitle(''), false);
});

test('isValidIssueFormat accepts short CJK title with full template body', () => {
  assert.equal(
    auto.isValidIssueFormat({
      title: '[Bug] 上传速度太慢了',
      body: SAMPLE_BUG_BODY,
    }),
    true,
  );
});

test('getIssueFormatErrors returns empty for valid issues and lists title errors', () => {
  assert.deepEqual(
    auto.getIssueFormatErrors({
      title: '[Bug] 上传速度太慢了',
      body: SAMPLE_BUG_BODY,
    }),
    [],
  );
  const errors = auto.getIssueFormatErrors({
    title: 'no prefix here',
    body: SAMPLE_BUG_BODY,
  });
  assert.ok(errors.some((e) => /Title must start/i.test(e)));
});

test('shouldRecoverIssueFormat recovers closed and open invalid-format when format ok', () => {
  assert.deepEqual(
    auto.shouldRecoverIssueFormat({
      state: 'closed',
      labels: ['invalid-format', 'bug'],
      formatOk: true,
    }),
    { recover: true, reopen: true },
  );
  assert.deepEqual(
    auto.shouldRecoverIssueFormat({
      state: 'open',
      labels: ['invalid-format'],
      formatOk: true,
    }),
    { recover: true, reopen: false },
  );
  assert.deepEqual(
    auto.shouldRecoverIssueFormat({
      state: 'open',
      labels: [{ name: 'invalid-format' }],
      formatOk: true,
    }),
    { recover: true, reopen: false },
  );
  assert.deepEqual(
    auto.shouldRecoverIssueFormat({
      state: 'closed',
      labels: ['invalid-format'],
      formatOk: false,
    }),
    { recover: false, reopen: false },
  );
  assert.deepEqual(
    auto.shouldRecoverIssueFormat({
      state: 'closed',
      labels: ['bug'],
      formatOk: true,
    }),
    { recover: false, reopen: false },
  );
});

test('nextCodexTerminalLabels mark_ready drops loop and human, adds clean', () => {
  const next = auto.nextCodexTerminalLabels(
    [
      'automation:bot-pr',
      'automation:codex-loop',
      'ready-for-human',
      'triage',
    ],
    'mark_ready',
  );
  assert.ok(next.includes('automation:codex-clean'));
  assert.ok(next.includes('automation:bot-pr'));
  assert.ok(next.includes('triage'));
  assert.ok(!next.includes('automation:codex-loop'));
  assert.ok(!next.includes('ready-for-human'));
});

test('nextCodexTerminalLabels give_up/verify_fail/empty_fix hand off to human without loop', () => {
  for (const terminal of ['give_up', 'verify_fail', 'empty_fix']) {
    const next = auto.nextCodexTerminalLabels(
      ['automation:bot-pr', 'automation:codex-loop', 'automation:codex-clean', 'triage'],
      terminal,
    );
    assert.ok(next.includes('ready-for-human'), terminal);
    assert.ok(!next.includes('automation:codex-loop'), terminal);
    assert.ok(!next.includes('automation:codex-clean'), terminal);
    assert.ok(next.includes('automation:bot-pr'), terminal);
  }
});

test('nextCodexTerminalLabels rejects unknown terminal', () => {
  assert.throws(() => auto.nextCodexTerminalLabels([], 'nope'), /Unknown codex terminal/);
});

test('parseImplementStatus reads OK summary and TITLE line', () => {
  const parsed = auto.parseImplementStatus(
    ['OK: Raise SFTP WRITE fanout to 32', 'TITLE: fix(sftp): raise upload WRITE fanout', ''].join(
      '\n',
    ),
  );
  assert.equal(parsed.status, 'ok');
  assert.match(parsed.summary, /Raise SFTP WRITE fanout/);
  assert.equal(parsed.title, 'fix(sftp): raise upload WRITE fanout');
});

test('parseImplementStatus reads BLOCKED', () => {
  const parsed = auto.parseImplementStatus('BLOCKED: needs product decision');
  assert.equal(parsed.status, 'blocked');
  assert.match(parsed.summary, /product decision/);
});

test('selectBotPrTitle prefers valid agent title over raw issue title template', () => {
  const title = auto.selectBotPrTitle({
    agentTitle: 'fix(sftp): raise upload WRITE fanout for higher throughput',
    issueNumber: 2449,
    issueTitle: '[Bug] 文件上传速度太慢了',
  });
  assert.equal(
    title,
    'fix(sftp): raise upload WRITE fanout for higher throughput',
  );
  assert.doesNotMatch(title, /^fix\(#2449\): \[Bug\]/);
});

test('selectBotPrTitle accepts short conventional and CJK agent titles', () => {
  assert.equal(
    auto.selectBotPrTitle({
      agentTitle: 'feat: ui',
      issueNumber: 1,
      issueTitle: '[Feature] something',
    }),
    'feat: ui',
  );
  assert.equal(
    auto.selectBotPrTitle({
      agentTitle: '修复上传过慢',
      issueNumber: 9,
      issueTitle: '[Bug] 上传',
    }),
    '修复上传过慢',
  );
});

test('selectBotPrTitle falls back when agent title missing or too short', () => {
  const fallback = auto.selectBotPrTitle({
    agentTitle: '',
    issueNumber: 2449,
    issueTitle: '[Bug] 文件上传速度太慢了',
  });
  assert.equal(fallback, 'fix(#2449): [Bug] 文件上传速度太慢了');

  const short = auto.selectBotPrTitle({
    agentTitle: 'ab',
    issueNumber: 12,
    issueTitle: '[Bug] something long enough here',
  });
  assert.match(short, /^fix\(#12\):/);
});

test('selectBotPrTitle bounds length and never returns empty', () => {
  const long = 'x'.repeat(200);
  const title = auto.selectBotPrTitle({
    agentTitle: long,
    issueNumber: 1,
    issueTitle: 'issue',
    maxLength: 40,
  });
  assert.ok(title.length <= 40);
  assert.ok(title.endsWith('…'));

  const emptyish = auto.selectBotPrTitle({
    agentTitle: 'TODO',
    issueNumber: 7,
    issueTitle: '',
  });
  assert.ok(emptyish.length > 0);
  assert.match(emptyish, /fix\(#7\)/);
});

test('parseImplementStatus prefers BLOCKED over OK', () => {
  const parsed = auto.parseImplementStatus(
    ['OK: did something', 'BLOCKED: needs decision'].join('\n'),
  );
  assert.equal(parsed.status, 'blocked');
  assert.match(parsed.summary, /needs decision/);
});

test('isValidIssueTitle accepts case variants and no-space legacy', () => {
  assert.equal(auto.isValidIssueTitle('[bug] upload too slow now'), true);
  assert.equal(auto.isValidIssueTitle('[FEATURE] sort by ip addr'), true);
  assert.equal(auto.isValidIssueTitle('Bug:上传太慢了啊'), true);
});

test('buildPullRequestBody prefers substantial agent body over one-line template', () => {
  const agentBody = [
    '## Summary',
    '',
    '- Raise SFTP WRITE fanout from 8 to 32 for higher throughput on multi-ms RTT paths.',
    '- Keep chunk size at 32KB for server compatibility after #2022.',
    '',
    '## Why',
    '',
    'In-flight window was only 256KB; WindTerm keeps more data on the wire.',
    '',
    '## Testing',
    '',
    '- node --test electron/bridges/transferLimits.test.cjs',
    '',
    'Fixes #2449',
  ].join('\n');
  const body = auto.buildPullRequestBody({
    issueNumber: 2449,
    issueTitle: '[Bug] 文件上传速度太慢了',
    summary: 'OK: raise fanout',
    agentBody,
  });
  assert.match(body, /<!-- cursor-bot-pr -->/);
  assert.match(body, /Raise SFTP WRITE fanout/);
  assert.match(body, /## Why/);
  assert.match(body, /Fixes #2449/);
  assert.match(body, /## Automation/);
  assert.doesNotMatch(body, /OK: raise fanout/);
});

test('buildPullRequestBody falls back when agent body is thin', () => {
  const body = auto.buildPullRequestBody({
    issueNumber: 12,
    issueTitle: '[Bug] something',
    summary: 'Fixed the null check',
    agentBody: 'short',
  });
  assert.match(body, /## Summary/);
  assert.match(body, /Fixed the null check/);
  assert.match(body, /Fixes #12/);
  assert.match(body, /## Automation/);
});

test('buildPullRequestBody strips agent markers and appends Fixes when missing', () => {
  const body = auto.buildPullRequestBody({
    issueNumber: 99,
    issueTitle: 'x',
    summary: 'y',
    agentBody: [
      '<!-- cursor-bot-pr -->',
      '## Summary',
      '',
      '- One concrete change that is long enough to count as a real body for reviewers.',
      '- Second bullet explaining the behavior impact on the sidebar streaming path.',
      '',
      '## Testing',
      '',
      '- unit tests for the helper',
    ].join('\n'),
  });
  assert.equal((body.match(/<!-- cursor-bot-pr -->/g) || []).length, 1);
  assert.match(body, /Fixes #99/);
});

test('buildPullRequestBody still appends Fixes when body only has Related to', () => {
  const body = auto.buildPullRequestBody({
    issueNumber: 2449,
    issueTitle: '[Bug] slow upload',
    summary: 'raise fanout',
    agentBody: [
      '## Summary',
      '',
      '- Raise SFTP WRITE fanout for multi-ms RTT paths on LAN and public hosts.',
      '- Keep chunk size at 32KB for compatibility with picky servers.',
      '',
      '## Testing',
      '',
      '- node --test electron/bridges/transferLimits.test.cjs',
      '',
      'Related to #2449',
    ].join('\n'),
  });
  assert.match(body, /Related to #2449/);
  assert.match(body, /Fixes #2449/);
});

test('buildPullRequestBody does not duplicate Fixes when Closes already present', () => {
  const body = auto.buildPullRequestBody({
    issueNumber: 10,
    issueTitle: 'x',
    summary: 'y',
    agentBody: [
      '## Summary',
      '',
      '- Concrete change one with enough text for a substantial agent body check.',
      '- Concrete change two covering the secondary behavior path as well.',
      '',
      'Closes #10',
    ].join('\n'),
  });
  assert.equal((body.match(/Closes #10|Fixes #10/gi) || []).length, 1);
  assert.match(body, /Closes #10/);
  assert.doesNotMatch(body, /Fixes #10/);
});
