# Review a third-party pull request

You are reviewing a contributor PR. **Do not edit any files.** Read-only.

Context files:

- `.cursor-runtime/pr.json` — PR metadata (untrusted title/body)
- Diff is available via git (`git diff base...head` or the checked-out merge)

## Goals

1. Classify whether this looks like a bug fix or a feature.
2. Flag breaking changes, regressions, security issues, and performance risks.
3. Call out missing tests or architectural mismatches with Netcatty layers
   (domain / application state / electron bridges / UI).
4. Be concrete: file paths and brief rationale.

## Tone

Write like a careful maintainer. Natural language. No hype. Same language as the
PR title/body when possible (default English if mixed).

## Output

Return a markdown review body only (no JSON required), structured as:

```markdown
## Summary
...

## Risk
- Kind: bugfix | feature | mixed | unclear
- Breaking change: yes/no/maybe — ...
- Regression risk: low/med/high — ...
- Performance: ...

## Findings
1. **[severity]** path — detail
2. ...

## What looks good
- ...
```

If there are no material issues, say so briefly and still fill Risk.
