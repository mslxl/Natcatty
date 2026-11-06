#!/bin/bash
# Provision a macOS host for the native ET parity cell: two throwaway accounts
# matching the container lab, password authentication, a trusted user CA slot,
# and the system sshd. The stock etserver is built and started by the caller.
#
# This is the documented native runner job in .github/workflows/et-parity.yml.
# It is required to run as root (sudo) because macOS only lets the system sshd
# authenticate other accounts from a root-owned configuration.
set -euo pipefail

password="${1:?usage: macos-provision.sh PASSWORD}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "macos-provision: this script only runs on macOS" >&2
  exit 1
fi

for user in tester tester2; do
  if id "$user" >/dev/null 2>&1; then
    sudo sysadminctl -resetPasswordFor "$user" -password "$password"
  else
    sudo sysadminctl -addUser "$user" -fullName "$user" -password "$password" -shell /bin/bash
    sudo createhomedir -c -u "$user" >/dev/null
  fi
  sudo mkdir -p "/Users/$user/.ssh"
  sudo chown -R "$user":staff "/Users/$user/.ssh"
  sudo chmod 700 "/Users/$user/.ssh"
done

# The tests install a user CA; sshd must start with the file present.
sudo touch /etc/ssh/lab_user_ca.pub
sudo chmod 644 /etc/ssh/lab_user_ca.pub

sudo tee /etc/sudoers.d/natcatty-et-parity >/dev/null <<'SUDOERS'
tester ALL=(ALL) NOPASSWD: ALL
SUDOERS
sudo chmod 440 /etc/sudoers.d/natcatty-et-parity

# sshd keeps the first occurrence of a keyword, so replace any existing
# directives instead of appending a second one that would be ignored.
sudo sed -i '' -E '/^[[:space:]]*(PasswordAuthentication|UsePAM|SetEnv|TrustedUserCAKeys)[[:space:]]/d' /etc/ssh/sshd_config
sudo tee -a /etc/ssh/sshd_config >/dev/null <<'SSHD'
PasswordAuthentication yes
UsePAM yes
SetEnv PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
TrustedUserCAKeys /etc/ssh/lab_user_ca.pub
SSHD

sudo ssh-keygen -A

# On hosted runners the launchd service is the reliable switch; systemsetup
# additionally flips the persisted preference when the runner permits it.
# Different macOS releases expose different launchctl verbs, so try all of the
# supported enable/start paths; the socket poll below is the real check.
sudo launchctl enable system/com.openssh.sshd 2>/dev/null || true
sudo launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist 2>/dev/null || true
sudo launchctl load -w /System/Library/LaunchDaemons/ssh.plist 2>/dev/null || true
sudo launchctl kickstart -k system/com.openssh.sshd 2>/dev/null || true
sudo systemsetup -setremotelogin on 2>/dev/null || true

# Poll the real listening socket with a hard deadline.
for _ in $(seq 1 60); do
  if nc -z 127.0.0.1 22; then
    exit 0
  fi
  sleep 1
done
echo "macos-provision: sshd is not listening on 127.0.0.1:22" >&2
exit 1
