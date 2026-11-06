#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Container entrypoint for one parity matrix cell. The harness passes the
# cell's forced UDP port; the wrapper binary is the ServerPath every lab test
# uses, so the same test code works for every platform and version.
set -eu

port="${MOSH_FORCED_PORT:?MOSH_FORCED_PORT is required}"

cat > /usr/local/bin/mosh-lab-server <<EOF
#!/bin/sh
# Stock mosh-server adopts the client-supplied -l locale variables only when
# the server host's own locale is not UTF-8; otherwise it keeps the native
# locale. Pin the native locale to C so the stock locale-adoption path is
# exercised deterministically. Fedora's default C.UTF-8 would otherwise
# short-circuit it and hide the client's locale handoff.
cmd="\$1"; shift
exec env LANG=C LC_ALL=C /usr/local/bin/mosh-server "\$cmd" -p ${port} -i 0.0.0.0 "\$@"
EOF
chmod 0755 /usr/local/bin/mosh-lab-server

exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config.lab
