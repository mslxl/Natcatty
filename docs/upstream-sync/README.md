# Upstream sync procedure

This repository is a Go rewrite of `binaricat/Netcatty`. Upstream keeps shipping
Electron/React content that this fork deleted. The goal of a sync is to record
upstream ancestry so GitHub's "N commits behind" badge reaches zero, while the
tree stays pure Go and only consciously chosen files are adopted.

## Branch policy

- Do the sync on `upstream-sync`. Never merge it into `main` as part of the
  sync work; `main` is updated only by a later, explicit decision.
- Push `upstream-sync` as the review object and backup.
- No `gh` operations unless the maintainer asks.

## Remotes

```
origin    https://github.com/mslxl/Natcatty.git     # this fork
upstream  https://github.com/binaricat/Netcatty.git # the Electron project
```

## Recipe

1. Fetch both sides and note the upstream tip.

   ```
   git fetch origin
   git fetch upstream
   git log -1 --format='%h %s' upstream/main
   ```

2. Find the content fork point: the local commit whose tree first matches an
   upstream commit (in the last sync this was local `948a4b210` == upstream
   `6abb6635b`). Then find the upstream commit to graft onto — the parent side
   of that fork point (`73d15da76` in the last sync).

   ```
   git diff --quiet <local-rewrite-root> <upstream-equal-commit> && echo same tree
   ```

3. Graft the local rewrite root onto the upstream commit. The graft makes the
   two histories share a merge base so the merge is an ordinary two-parent
   merge rather than an unrelated-histories merge.

   ```
   git replace --graft <local-rewrite-root> <upstream-commit>
   git merge-base HEAD upstream/main     # should print <upstream-commit>
   ```

   Replace refs are local-only: they are not pushed and not fetched. The merge
   commit produced below does not depend on the graft, so the ref is deleted
   afterwards.

4. Merge without committing and record the shape.

   ```
   git merge --no-commit --no-ff upstream/main
   git status --porcelain | cut -c1-2 | sort | uniq -c
   ```

   The expected shape is a large set of `DU` (deleted by us, modified
   upstream), a handful of `UU` content conflicts, and a large set of
   upstream-added `A` paths.

5. Resolve by keeping the Go tree. This resolves every conflict — including the
   content conflicts and every upstream addition — as "keep ours":

   ```
   git read-tree --reset -u HEAD
   ```

   `MERGE_HEAD` survives this, so the next commit is still a merge commit.

6. Adopt only the explicitly chosen files, for example the generic workflows:

   ```
   git checkout upstream/main -- .github/workflows/grokbot-issue-relay.yml \
                                .github/workflows/spam-comments.yml
   ```

   Do not take `build.yml` / `test.yml`; the Go repo has its own CI.

7. Commit, then verify.

   ```
   git commit -F - <<'EOF'
   merge: record upstream/main ancestry while keeping the Go tree
   EOF
   git diff <old-head> HEAD --stat                    # only the chosen files
   git rev-list --count HEAD..upstream/main           # 0
   git merge-base --is-ancestor upstream/main HEAD    # passes
   go build ./...
   ```

8. Delete the graft and re-verify. The merge commit has upstream as its second
   parent, so the count and ancestor check must still pass without the graft.

   ```
   git replace -d <local-rewrite-root>
   git replace -l                                     # empty
   git rev-list --count HEAD..upstream/main           # still 0
   git merge-base --is-ancestor upstream/main HEAD    # still passes
   ```

9. Fill the ledger for this range (see the ledger file next to this page and
   `.scratch/upstream-sync/inputs/generate_ledger.py`), then run the acceptance
   gates from the sync map.

10. Push only the sync branch.

    ```
    git push -u origin upstream-sync
    ```

## Notes

- The `behind` badge measures commits reachable from `upstream/main` but not
  from the default branch. It disappears only when the merge commit is on the
  default branch; until then the sync branch holds it ready.
- Never resolve a conflict by taking an upstream Electron file into the tree.
  The rewrite's deletions are deliberate.
- Tests in this repository wait on a real condition with a hard timeout, never
  a fixed sleep.
