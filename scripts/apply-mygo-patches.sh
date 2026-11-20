#!/bin/sh
# Apply the local MyGo patches to the third_party/mygo submodule.
#
# The parent repository pins MyGo with a gitlink; the patches in patches/mygo
# are relative to that commit. Resetting first makes the script idempotent and
# keeps a stale patch application from surviving a checkout.
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

git submodule update --init --recursive third_party/mygo

# The gitlink, not the submodule's working tree, is the source of truth.
git -C third_party/mygo reset --hard --quiet HEAD
git -C third_party/mygo clean -fdx --quiet

for patch in patches/mygo/*.patch; do
	[ -e "$patch" ] || continue
	git -C third_party/mygo apply --check "$root/$patch"
	git -C third_party/mygo apply "$root/$patch"
done
