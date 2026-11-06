#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Provision the FreeBSD 14 mosh parity cells inside the VM runner.
#
#   sh adapters/mosh/lab/freebsd-setup.sh
#
# The script installs a Go toolchain (the go.mod toolchain directive may fetch
# the pinned release) and git, installs mosh 1.4.0 from ports, builds mosh 1.3.2
# from the pinned upstream release, creates the disposable tester account, and
# enables the base-system sshd on port 22. Run the gate with
# `-lab-addr 127.0.0.1:22` for each FreeBSD cell. VM workspace syncs may omit
# .git, so the workflow also exports NATCATTY_MOSH_PARITY_COMMIT="$GITHUB_SHA";
# the gate never accepts an empty commit.
set -eu

PATH=/usr/local/bin:${PATH}
export PATH

LAB=/tmp/natcatty-mosh-lab
MOSH_132_SHA256=da600573dfa827d88ce114e0fed30210689381bbdcff543c931e4d6a2e851216
mkdir -p "${LAB}"

export ASSUME_ALWAYS_YES=yes
pkg update
# git is installed so the workspace can resolve a commit when the runner sync
# includes .git; the workflow also passes GitHub's SHA explicitly because VM
# workspace syncs may omit it (see NATCATTY_MOSH_PARITY_COMMIT in the gate).
pkg install -y git go gmake pkgconf curl ca_root_nss openssl protobuf ncurses mosh
# The gate's suites run through `go` inside this VM, and git keeps report
# provenance resolvable when the synced workspace contains a checkout.
command -v go >/dev/null
command -v git >/dev/null
go version
git --version

# 1.4.0: the ports package. The gate's server-version behavior proves the
# exact version before any behavior is counted.
mosh-server --version | grep -q 'mosh 1.4.0'

# 1.3.2: pinned upstream release.
curl -fsSL -o "${LAB}/mosh-1.3.2.tar.gz" \
  https://github.com/mobile-shell/mosh/releases/download/mosh-1.3.2/mosh-1.3.2.tar.gz
actual_sha=$(sha256 -q "${LAB}/mosh-1.3.2.tar.gz")
if [ "${actual_sha}" != "${MOSH_132_SHA256}" ]; then
  echo "mosh-1.3.2.tar.gz sha256 = ${actual_sha}, want ${MOSH_132_SHA256}" >&2
  exit 1
fi
rm -rf "${LAB}/src" "${LAB}/1.3.2"
mkdir -p "${LAB}/src" "${LAB}/1.3.2"
tar -xzf "${LAB}/mosh-1.3.2.tar.gz" -C "${LAB}/src" --strip-components=1
(
  cd "${LAB}/src"
  CXXFLAGS="-std=c++17 -O2" ./configure --prefix="${LAB}/1.3.2" --without-utempter
  gmake -j"$(sysctl -n hw.ncpu)"
  gmake install
)
"${LAB}/1.3.2/bin/mosh-server" --version | grep -q 'mosh 1.3.2'

# Per-version wrappers force the matrix cell's UDP port. The native locale is
# pinned to C because stock mosh-server adopts the client's -l locale
# variables only when the server host's own locale is not UTF-8.
cat > /usr/local/bin/mosh-1.4.0 <<'EOF'
#!/bin/sh
cmd="$1"; shift
exec env LANG=C LC_ALL=C /usr/local/bin/mosh-server "$cmd" -p 24610 -i 0.0.0.0 "$@"
EOF
cat > /usr/local/bin/mosh-1.3.2 <<'EOF'
#!/bin/sh
cmd="$1"; shift
exec env LANG=C LC_ALL=C /tmp/natcatty-mosh-lab/1.3.2/bin/mosh-server "$cmd" -p 24609 -i 0.0.0.0 "$@"
EOF
chmod 0755 /usr/local/bin/mosh-1.4.0 /usr/local/bin/mosh-1.3.2

# Disposable lab account. pw(8)'s -w option selects a password policy
# (no/yes/none/random); it does not accept a literal password. -h 0 reads the
# plaintext password from stdin instead.
if ! printf 'testpass\n' | pw useradd -n tester -m -s /bin/sh -h 0; then
  printf 'testpass\n' | pw usermod -n tester -h 0
fi
pw usershow -n tester >/dev/null

# sshd uses the first obtained PasswordAuthentication value, so appending "yes"
# is ineffective when an earlier active "no" exists. Rewrite every active
# directive (including ones inside Match blocks); when none exists, insert the
# global directive before the first Match block so the default is explicit.
sshd_config=/etc/ssh/sshd_config
tmp_config=$(mktemp /tmp/natcatty-sshd_config.XXXXXX)
trap 'rm -f "${tmp_config}"' EXIT
awk '
  BEGIN { found = 0; inserted = 0 }
  tolower($1) == "match" && !found && !inserted {
    print "PasswordAuthentication yes"
    inserted = 1
  }
  tolower($1) == "passwordauthentication" {
    print "PasswordAuthentication yes"
    found = 1
    next
  }
  { print }
  END {
    if (!found && !inserted) {
      print "PasswordAuthentication yes"
    }
  }
' "${sshd_config}" > "${tmp_config}"
cat "${tmp_config}" > "${sshd_config}"
rm -f "${tmp_config}"
trap - EXIT

sysrc sshd_enable=YES
ssh-keygen -A
/usr/sbin/sshd -t
# The vmactions runner drives this script over SSH to the same sshd. A restart
# would terminate that session mid-script, so reload the running daemon (SIGHUP
# applies config to new connections without dropping established ones) and
# start it only when it is not running.
if service sshd status >/dev/null 2>&1; then
  service sshd reload
else
  service sshd start
fi

# Readiness as root: sshd -T dumps the effective configuration after
# first-obtained-value resolution. A remaining "no" fails the cell before any
# behavior can count.
if ! /usr/sbin/sshd -T 2>/dev/null | grep -Eiq '^passwordauthentication[[:space:]]+yes$'; then
  echo "sshd effective configuration does not enable password authentication" >&2
  /usr/sbin/sshd -T 2>&1 | grep -i '^passwordauthentication' >&2 || true
  exit 1
fi

# Observable readiness condition with a hard deadline.
deadline=$(( $(date +%s) + 30 ))
while ! nc -z 127.0.0.1 22 >/dev/null 2>&1; do
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "FreeBSD sshd did not listen on 127.0.0.1:22 within 30s" >&2
    exit 1
  fi
  sleep 0.2
done

echo "FreeBSD mosh parity cells ready: 1.4.0 (ports) and 1.3.2 (pinned source)"
