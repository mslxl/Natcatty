# MyGo is vendored with a host patch

The phone shell is a MyGo app: MyGo supplies the native host, the UI runtime,
and the terminal surface on both platforms. The iOS host in MyGo v0.10.0
cannot re-show the soft keyboard after the user dismisses it with the
keyboard's own key: `SurfaceWindow.setTextInput` calls
`becomeFirstResponder()` again, but the view never stopped being first
responder, so UIKit does nothing and the keyboard stays down. `reloadInputViews()`
is the call that brings it back. The fix belongs in the host, and the host
ships as Swift source inside the MyGo module.

**Status**: accepted

**Decision**: MyGo is pinned as a git submodule at `third_party/mygo` on the
release tag (`v0.10.0`), `go.mod` selects it with
`replace github.com/egoist/mygo => ./third_party/mygo`, and natcatty's
changes live as patches in `patches/mygo/`. Every build path runs
`scripts/apply-mygo-patches.sh`, which resets the submodule to the pinned
commit and applies every patch in name order. The compile gates, the iOS
render driver, and the release and phone CI workflows call it before they
build. The directory also carries the desktop mouse, DEC 1001, ConPTY and
encoder-mode patches; the keyboard patch is `0005`. The Nix package cannot see a submodule's contents (a flake
source holds the gitlink, not the tree), so it takes the same release from a
`mygo` flake input and overlays that tree in place of the empty
`third_party/mygo`, applying the same patches.

The patch is the smallest upstream-shaped change: when the host is asked to
show the keyboard again while it is still first responder, call
`reloadInputViews()` instead of `becomeFirstResponder()`. It is written as an
ordinary `git diff` against the pinned tag so it can be proposed upstream
unchanged.

**Upgrading MyGo**: bump the submodule and the `mygo` flake input to the new
tag, run `scripts/apply-mygo-patches.sh` and decide each patch: if upstream
now carries the fix, delete the patch; if the source moved, rework the patch
against the new tree; never force one. Adding a *new* patch is a
product decision: ask the maintainer before writing it.

**Considered Options**:
- Patch the module cache: rejected. It is per-machine, invisible to CI, and
  lost on every cache clean.
- Copy MyGo's source into the repository: rejected. It hides the upstream
  relationship, makes the diff against a release unreadable, and turns every
  upgrade into a manual merge of the whole framework.
- Wait for an upstream release: rejected for this milestone. The keyboard
  behaviour is a criterion of the phone build, and the fix is one branch.
- Modify MyGo's generated Swift host after `go tool mygo build`: rejected.
  The generated app is a build product; patching it would make the shipped
  binary differ from what the gates compiled and tested.

**Consequences**: a checkout needs `git submodule update --init --recursive`
before it builds; CI's `actions/checkout` runs with `submodules: recursive`.
The Nix package's `vendorHash` changes with the vendored tree, so it must be
refreshed whenever the submodule or a patch changes Go sources. CI's phone
jobs clone and compile MyGo from source, which costs a checkout step rather
than a module-cache fetch. Patch drift is a real maintenance cost, bounded by
the rule that a patch must be dropped or reworked at each upgrade and that new
patches need approval. Applying the patches leaves the submodule's working
tree modified against its pinned commit; `git status` in the superproject
reports `third_party/mygo` as changed, which is expected and is not a
change to the recorded gitlink.
