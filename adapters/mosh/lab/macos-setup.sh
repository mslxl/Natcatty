#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Provision the native macOS mosh parity cells on a dedicated runner.
#
#   sudo bash adapters/mosh/lab/macos-setup.sh
#
# Homebrew supplies the pinned 1.4.0 server. mosh 1.3.2 is built from the
# pinned upstream release on a case-sensitive APFS image: modern protobuf
# headers include C++ <version>, and macOS's default case-insensitive
# filesystem would otherwise resolve it to mosh's generated VERSION file.
# The script starts a dedicated sshd on 127.0.0.1:2457 for the disposable
# tester account. It must run as root; GitHub's macOS runner provides
# passwordless sudo.
set -euo pipefail

LAB=/tmp/natcatty-mosh-lab
MOSH_132="${LAB}/1.3.2/bin/mosh-server"
MOSH_132_SHA256=da600573dfa827d88ce114e0fed30210689381bbdcff543c931e4d6a2e851216

if [ "$(id -u)" -ne 0 ]; then
  echo "macos-setup.sh must run as root (sudo)" >&2
  exit 1
fi
# Homebrew refuses to run as root, so resolve and invoke it as the invoking
# runner user. The build itself stays root-owned, like the rest of the lab.
LAB_USER="${SUDO_USER:-}"
if [ -z "${LAB_USER}" ] || [ "${LAB_USER}" = "root" ]; then
  echo "run with sudo as the runner user, not directly as root" >&2
  exit 1
fi
BREW=""
for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
  if [ -x "${candidate}" ]; then
    BREW="${candidate}"
    break
  fi
done
if [ -z "${BREW}" ]; then
  echo "Homebrew is required on the macOS runner" >&2
  exit 1
fi
BREW_PREFIX="$(sudo -u "${LAB_USER}" -H "${BREW}" --prefix)"
MOSH_140="${BREW_PREFIX}/bin/mosh-server"

# Run Homebrew as the runner user; -H points HOME at that user's account.
brew_run() {
  sudo -u "${LAB_USER}" -H "${BREW}" "$@"
}

mkdir -p "${LAB}"

# 1.4.0: the Homebrew formula. Homebrew formulas are not version-pinned, so
# fail closed on drift here; the server-version behavior proves the exact
# version over SSH again before any behavior counts.
if ! "${MOSH_140}" --version | grep -q 'mosh 1.4.0'; then
  brew_run install mosh
fi
"${MOSH_140}" --version | grep -q 'mosh 1.4.0'

# 1.3.2: build the pinned release on a case-sensitive image.
brew_run install pkg-config protobuf ncurses
IMAGE="${LAB}/mosh132.sparseimage"
MNT=/Volumes/natcattymosh
if [ -d "${MNT}" ]; then
  hdiutil detach "${MNT}" >/dev/null 2>&1 || true
fi
rm -f "${IMAGE}" "${IMAGE}.dmg"
hdiutil create -size 4g -fs "Case-sensitive APFS" -volname natcattymosh -type SPARSE "${IMAGE}" >/dev/null
hdiutil attach "${IMAGE}" >/dev/null
trap 'hdiutil detach "${MNT}" >/dev/null 2>&1 || true' EXIT

curl -fsSL -o "${LAB}/mosh-1.3.2.tar.gz" \
  https://github.com/mobile-shell/mosh/releases/download/mosh-1.3.2/mosh-1.3.2.tar.gz
echo "${MOSH_132_SHA256}  ${LAB}/mosh-1.3.2.tar.gz" | shasum -a 256 -c -
mkdir -p "${MNT}/src"
tar -xzf "${LAB}/mosh-1.3.2.tar.gz" -C "${MNT}/src" --strip-components=1
# 1.3.2 calls an unqualified bind() here. With a modern libc++ <functional>
# in scope that resolves to std::bind, and the build fails; qualify the POSIX
# call so the pinned release compiles against the toolchain the runner ships.
sed -i '' 's/if ( bind( sock(),/if ( ::bind( sock(),/' "${MNT}/src/src/network/network.cc"
grep -q '::bind( sock(),' "${MNT}/src/src/network/network.cc"
(
  cd "${MNT}/src"
  PATH="${BREW_PREFIX}/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
    CXXFLAGS="-std=c++17 -O2" ./configure --prefix="${LAB}/1.3.2" --without-utempter
  PATH="${BREW_PREFIX}/bin:/usr/bin:/bin:/usr/sbin:/sbin" make -j"$(sysctl -n hw.ncpu)"
  PATH="${BREW_PREFIX}/bin:/usr/bin:/bin:/usr/sbin:/sbin" make install
)
"${MOSH_132}" --version | grep -q 'mosh 1.3.2'

# Per-version wrappers force the matrix cell's UDP port. Both servers share
# the one sshd, exactly as the gate's cells expect. The native locale is
# pinned to C because stock mosh-server adopts the client's -l locale
# variables only when the server host's own locale is not UTF-8.
mk_wrapper() {
  version="$1"
  port="$2"
  binary="$3"
  cat > "${LAB}/mosh-${version}" <<EOF
#!/bin/sh
cmd="\$1"; shift
exec env LANG=C LC_ALL=C "${binary}" "\$cmd" -p ${port} -i 0.0.0.0 "\$@"
EOF
  chmod 0755 "${LAB}/mosh-${version}"
}
mk_wrapper 1.4.0 24607 "${MOSH_140}"
mk_wrapper 1.3.2 24608 "${MOSH_132}"

# Disposable lab account. sysadminctl is not guaranteed to create the home
# directory on every runner image, and sshd/mosh-server need a usable home, so
# make it explicit.
if ! id tester >/dev/null 2>&1; then
  sysadminctl -addUser tester -fullName "Mosh Lab" -password testpass \
    -home /Users/tester -shell /bin/bash
else
  sysadminctl -resetPasswordFor tester -newPassword testpass >/dev/null
fi
mkdir -p /Users/tester
chown tester:staff /Users/tester
if dscl . -read /Groups/com.apple.access_ssh >/dev/null 2>&1; then
  dseditgroup -o edit -a tester -t user com.apple.access_ssh >/dev/null 2>&1 || true
fi

# Dedicated sshd for the matrix, on a non-default port.
ssh-keygen -A
cat > "${LAB}/sshd_config" <<'EOF'
Port 2457
ListenAddress 127.0.0.1
HostKey /etc/ssh/ssh_host_ed25519_key
HostKey /etc/ssh/ssh_host_rsa_key
PasswordAuthentication yes
KbdInteractiveAuthentication no
UsePAM yes
PermitRootLogin no
PidFile /tmp/natcatty-mosh-lab/sshd.pid
Subsystem sftp internal-sftp
EOF
pkill -f 'sshd.*natcatty-mosh-lab' >/dev/null 2>&1 || true
/usr/sbin/sshd -f "${LAB}/sshd_config"

# Wait on an observable condition with a hard deadline, never a bare sleep.
deadline=$((SECONDS + 30))
until nc -z 127.0.0.1 2457 >/dev/null 2>&1; do
  if [ "${SECONDS}" -ge "${deadline}" ]; then
    echo "the matrix sshd did not listen on 127.0.0.1:2457 within 30s" >&2
    exit 1
  fi
  sleep 0.2
done

echo "macOS mosh parity cells ready: 1.4.0 (Homebrew) and 1.3.2 (pinned source)"
