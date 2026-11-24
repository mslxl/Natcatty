# MyGo patches

MyGo is a git submodule at `third_party/mygo`, pinned by the parent
repository's gitlink to upstream v0.10.0
(`ebde9399baf2734b3361856a53f4036859973d1d`). The submodule is never edited in
place in this repository's history: the patches in this directory are applied
to a clean checkout before building.

Run `scripts/apply-mygo-patches.sh` after checking out the repository, or use
`just prepare-mygo`. The script initializes the submodule, resets it to the
pinned commit (removing untracked files), and applies every `*.patch` here in
name order. It also refuses to prepare the build when `go.mod`'s
`github.com/egoist/mygo` version and the submodule's exact tag differ: the
replace directive would otherwise compile one release while `go.mod` names
another. `just dev`, `just test`, and `just build` depend on it, and CI runs
it before Go.

The patches are relative to v0.10.0. If the submodule moves to another upstream
commit, rebase them and re-run the archive and terminal tests.

If GitHub over HTTPS is unavailable in your environment, point the submodule
at SSH locally (not committed):

```sh
git config submodule.third_party/mygo.url git@github.com:egoist/mygo.git
```

## Why the patches exist

Upstream `plugins/terminal/view.go` computes the link under the pointer before
it decides whether the terminal program is taking the mouse. A program with
DEC mouse tracking on (modes 9, 1000, 1001, 1002, or 1003) therefore received
no report for a Command+click over a link-looking string; MyGo opened the link
instead. TUIs commonly bind Command/Alt+click to their own action. The patches
keep the link policy in MyGo, where the ordering lives, without vendoring its
source into this repository.
