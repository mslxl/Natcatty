# CLAUDE.md

## Coding conventions

- Reuse existing repository logic first; do not introduce unnecessary or premature abstractions.
- Keep code readable; do not sacrifice clarity for overly compressed or "clever" shortcuts.
- Avoid unnecessary layers of indirection.

## Credentials and encryption

- Encryption work is cross-platform by default: a credential protection added to the desktop shell must also land on the phone shell, and vice versa. When you touch one side's credential handling, check the other side and align it in the same change.
- In-memory credentials are never held as plaintext. Use the shared encrypted store (`appsession.MemCredentialStore`, AES-256-GCM sealed under a random per-run key) rather than a plaintext field, and drop entries when their owner (Pane, Session) closes.

## Testing

- Run UI and application tests headlessly with MyGo's headless tester (`ui.NewTester` via `go test`); do not launch a foreground GUI window to run tests.
- Skip new tests unless requested, required by the repo, or needed to catch a specific regression existing tests would miss.

### Development runs

When running the app manually during development, use `just dev` (`go run .`). A development build keeps its Vault and secrets in `<cwd>/.natcatty-test/` and never reads the real profile or OS credential store (ADR 0022). Do not test against an installed Natcatty or a `go tool mygo build` binary; those use the real profile and keychain.

### Mobile test environments

Phone work runs in one of two dedicated shells entered from the repository root: `nix develop .#ios` and `nix develop .#android` (the Android shell keys its AVDs to the checkout). They pin the toolchains: the iOS shell uses the host Xcode, and the Android shell ships a pinned SDK, NDK, emulator, and system image.

- **Device reuse:** use at most one iOS simulator and at most one Android emulator. Before creating one, check existing devices (`xcrun simctl list devices` for iOS, `emulator -list-avds` for Android); if any exists for that platform, reuse it and do not create another, even if it differs in device type, runtime, profile, or system image.
- **Boot:** start a device with `scripts/dev/ios-boot.sh` or `scripts/dev/android-boot.sh`; both print the device identifier and both are idempotent.
- **Teardown:** **shut the simulator or emulator down as soon as the test run finishes** (`scripts/dev/ios-stop.sh` / `scripts/dev/android-stop.sh`). Do not leave one booted between tasks or after a failed run — a running emulator holds CPU and memory, and the next run inherits its stale state.

## Git merges

Merge feature branches into `main` only after verifying the baseline.

- **Baseline:** run `git fetch origin --prune`; confirm local `main` matches `origin/main` (or that divergence is intentional). `git merge-base main <feature>` must be the intended old `main`.
- **Branch name:** before merging, check that the branch name accurately describes the change. If it does not, stop and ask the user whether to rename it before merging; do not rename silently.
- **Merge strategy:** from a clean `main` checkout (use a temporary worktree if `main` is not checked out):
  - **Single-commit branch** (exactly one commit after branching off `main`): fast-forward merge (`git merge --ff-only <feature>`) is allowed when `main` can fast-forward; otherwise use `--no-ff`.
  - **Multi-commit branch** (more than one commit after branching off `main`): use `git merge --no-ff <feature>` so the feature commits and their SHAs are preserved. `git update-ref` is fast-forward only and is not a substitute for a merge commit.
- **Fast-forward or direct push override:** for multi-commit branches, if the user asks for a fast-forward or a direct push to `main` instead of a `--no-ff` merge, ask them to confirm and suggest using `git merge --no-ff <feature>` instead. Proceed only if they confirm after being asked.
- **Message:** for `--no-ff` merges, keep Git's generated message and do not pass `-m`, e.g. `Merge branch '<feature>' into main`. Do not rewrite it as `feat:` or `chore:`; Conventional Commits defines no merge type and commitlint ignores merge messages by default. Use a Conventional Commit only for squash merges, where the squashed commit is the feature commit.
- **Verify before push:**
  - For `--no-ff` merges, `main^1` is the old `main` and `main^2` is the feature tip; when `main` has no independent changes, `main^{tree}` equals `<feature>^{tree}`. For single-commit fast-forward merges, `main` matches `<feature>` and `main^1` is the old `main`.
  - Run the full test gate on the merged tree, not only on the feature branch: `go test ./... -count=1 -p 1`, `go vet ./...`, plus required cross-builds.
- **Rewrites:** do not rewrite pushed history without explicit approval. If approved, preserve tree, parents, author, committer, and timestamps; verify all headers are byte-identical except the message.

## Agent skills

- **Issue tracker:** issues live as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.
- **Triage labels:** default five-role vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.
- **Domain docs:** single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.
