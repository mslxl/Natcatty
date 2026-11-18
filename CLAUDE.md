# CLAUDE.md

## Agent skills

### Issue tracker

Issues live as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.

## Development runs

When testing the app, run `just dev` (`go run .`). A development build keeps its Vault and secrets in `<cwd>/.natcatty-test/` and never reads the real profile or OS credential store (ADR 0022). Do not test against an installed Natcatty or a `go tool mygo build` binary; those use the real profile and keychain.

## Git merges

Merge feature branches into `main` only after verifying the baseline.

- **Baseline:** run `git fetch origin --prune`; confirm local `main` matches `origin/main` (or that divergence is intentional). `git merge-base main <feature>` must be the intended old `main`.
- **Branch name:** before merging, check that the branch name accurately describes the change. If it does not, stop and ask the user whether to rename it before merging; do not rename silently.
- **History:** from a clean `main` checkout (use a temporary worktree if `main` is not checked out), run `git merge --no-ff <feature>` so the feature commits and their SHAs are preserved. `git update-ref` is fast-forward only and is not a substitute for a merge commit.
- **Fast-forward or direct push:** if the user asks for a fast-forward or a direct push to `main` instead of a `--no-ff` merge, ask them to confirm and suggest using `git merge --no-ff <feature>` instead. Proceed only if they confirm after being asked.
- **Message:** keep Git's generated message and do not pass `-m`, e.g. `Merge branch '<feature>' into main`. Do not rewrite it as `feat:` or `chore:`; Conventional Commits defines no merge type and commitlint ignores merge messages by default. Use a Conventional Commit only for squash merges, where the squashed commit is the feature commit.
- **Verify before push:** `main^1` is the old `main` and `main^2` is the feature tip; when `main` has no independent changes, `main^{tree}` equals `<feature>^{tree}`. Run the full test gate on the merged tree, not only on the feature branch: `go test ./... -count=1 -p 1`, `go vet ./...`, plus required cross-builds.
- **Rewrites:** do not rewrite pushed history without explicit approval. If approved, preserve tree, parents, author, committer, and timestamps; verify all headers are byte-identical except the message.
