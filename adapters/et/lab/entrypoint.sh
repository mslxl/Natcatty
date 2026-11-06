#!/bin/bash
# Start the cell's sshd and one stock etserver. The ET server is reachable
# only through the SSH connection, matching production deployment.
set -euo pipefail

mkdir -p /run/sshd
chmod 0755 /run/sshd
/usr/sbin/sshd

exec /usr/local/bin/etserver --port "${ET_PORT:-2022}" --logtostdout -v 1
