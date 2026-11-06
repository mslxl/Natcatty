#!/bin/bash
# Prepare a stock etserver lab endpoint: two unprivileged test accounts, sshd
# configured for password, public-key, and user-certificate authentication,
# and an empty CA file the tests can populate through sudo.
set -euo pipefail

version="${1:?usage: setup-lab.sh VERSION}"
echo "${version}" > /etc/natcatty-et-lab-version

useradd -m -s /bin/bash tester 2>/dev/null || true
echo 'tester:testpass' | chpasswd
useradd -m -s /bin/bash tester2 2>/dev/null || true
echo 'tester2:testpass' | chpasswd

# Tests install a generated CA public key here. sshd reads TrustedUserCAKeys
# when it authenticates, so no reload is needed.
install -d -m 0755 /etc/ssh
touch /etc/ssh/lab_user_ca.pub
chmod 0644 /etc/ssh/lab_user_ca.pub

# Test-only privilege: append the certificate CA and restart sshd if a
# distribution requires it. No application code uses sudo.
install -d -m 0750 /etc/sudoers.d
echo 'tester ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/natcatty-et-lab
chmod 0440 /etc/sudoers.d/natcatty-et-lab
echo 'tester2 ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/natcatty-et-lab-2
chmod 0440 /etc/sudoers.d/natcatty-et-lab-2

cat >> /etc/ssh/sshd_config <<'EOF'

# natcatty ET parity lab
PasswordAuthentication yes
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
AllowTcpForwarding yes
AllowAgentForwarding no
X11Forwarding no
PrintMotd no
UsePAM yes
AuthorizedKeysFile .ssh/authorized_keys
TrustedUserCAKeys /etc/ssh/lab_user_ca.pub
EOF

ssh-keygen -A
mkdir -p /run/sshd
chmod 0755 /run/sshd
/usr/local/bin/etserver --version
