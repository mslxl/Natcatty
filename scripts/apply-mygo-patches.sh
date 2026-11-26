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

# Go ignores the version beside a directory replace, so nothing else would
# notice go.mod naming one MyGo release while the gitlink pins another.
# Refuse to prepare the build in that state: the module graph would describe
# a version the binary does not contain.
required=$(sed -n 's/^[[:space:]]*github\.com\/egoist\/mygo[[:space:]]\{1,\}\(v[^[:space:]]*\).*/\1/p' go.mod | head -n 1)
if [ -z "$required" ]; then
	echo "mygo: go.mod does not require github.com/egoist/mygo" >&2
	exit 1
fi
tags=$(git -C third_party/mygo tag --points-at HEAD)
if [ -z "$tags" ]; then
	# A CI checkout clones the pinned submodule commit without the remote's
	# tags, so the pin check cannot see the release the gitlink names. Fetch
	# them once before calling the pin wrong; an offline checkout fails the
	# fetch quietly and reports the missing tag below as before.
	git -C third_party/mygo fetch --tags --quiet origin >/dev/null 2>&1 || true
	tags=$(git -C third_party/mygo tag --points-at HEAD)
fi
if ! printf '%s\n' "$tags" | grep -qx "$required"; then
	if [ -z "$tags" ]; then
		echo "mygo: third_party/mygo HEAD has no tag; fetch tags with 'git -C third_party/mygo fetch --tags'" >&2
	else
		echo "mygo: go.mod requires $required but third_party/mygo HEAD is tagged $(printf '%s' "$tags" | paste -sd, -)" >&2
	fi
	echo "mygo: update go.mod and the gitlink together (see patches/mygo/README.md)" >&2
	exit 1
fi

for patch in patches/mygo/*.patch; do
	[ -e "$patch" ] || continue
	git -C third_party/mygo apply --check "$root/$patch"
	git -C third_party/mygo apply "$root/$patch"
done
