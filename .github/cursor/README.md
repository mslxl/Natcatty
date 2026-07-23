# Cursor CLI automation

GitHub Actions orchestration that uses **Cursor CLI** for issue triage,
implementation, and third-party PR review. Own / bot PRs use the existing
**Codex GitHub connector** (`@codex review`) as the review gate — not Bugbot.

## Required secret

| Secret | Purpose |
|---|---|
| `CURSOR_API_KEY` | Cursor CLI authentication |

Optional: `TRIAGE_GITHUB_TOKEN` (PAT with contents + PR + issues) if
`github-actions[bot]` comments are ignored by Codex; `SLACK_WEBHOOK_URL` for
status pings.

## Variables

| Variable | Default | Purpose |
|---|---|---|
| `CURSOR_CODEX_FIX_MAX_ROUNDS` | `3` | Max Cursor fix ↔ `@codex review` loops |
| `CURSOR_TRIAGE_DAILY_LIMIT` | `10` | Daily auto triage for non-collaborators |
| `AUTOMATION_OWN_ACTORS` | `binaricat` | Logins treated as first-party PR authors |

## Manual retry

Actions → **Cursor automation** → Run workflow → provide an issue number.

## Safety

- External / fork PRs are review-only (no commits).
- Automation never publishes changes under `.github/` or automation scripts.
- Issue text is sanitized before prompts.
