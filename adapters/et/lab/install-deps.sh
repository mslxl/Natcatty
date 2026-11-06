#!/bin/bash
# Install build or runtime dependencies for the stock ET lab on Debian/Ubuntu,
# Fedora, or Arch. The package names differ but the dependency set is the same:
# sshd, a shell, tooling for the stock-client golden comparison, and the ET
# runtime libraries. Build additionally needs a C++ toolchain, CMake, git, and
# the development headers.
set -euo pipefail

mode="${1:?usage: install-deps.sh build|runtime}"

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  if [ "$mode" = build ]; then
    apt-get install -y --no-install-recommends \
      build-essential ca-certificates cmake git pkg-config \
      libsodium-dev libprotobuf-dev protobuf-compiler libssl-dev zlib1g-dev \
      libutempter-dev libcurl4-openssl-dev libgflags-dev
  else
    # Ubuntu 24.04 renamed several runtime packages with the t64 transition;
    # Debian 12 and Ubuntu 22.04 use the unsuffixed names.
    curl_pkg=libcurl4
    protobuf_pkg=libprotobuf32
    ssl_pkg=libssl3
    # shellcheck disable=SC1091
    . /etc/os-release
    version_major="${VERSION_ID%%.*}"
    if [ "${ID:-}" = ubuntu ] && [ "${version_major:-0}" -ge 24 ]; then
      curl_pkg=libcurl4t64
      protobuf_pkg=libprotobuf32t64
      ssl_pkg=libssl3t64
    fi
    apt-get install -y --no-install-recommends \
      bash ca-certificates curl findutils libgflags2.2 libncurses6 \
      libsodium23 libstdc++6 libutempter0 openssh-server perl \
      procps sudo tmux tzdata zlib1g \
      "${curl_pkg}" "${protobuf_pkg}" "${ssl_pkg}"
  fi
  rm -rf /var/lib/apt/lists/*
elif command -v dnf >/dev/null 2>&1; then
  if [ "$mode" = build ]; then
    dnf install -y --setopt=install_weak_deps=False \
      gcc-c++ make cmake git pkgconf-pkg-config findutils \
      libsodium-devel protobuf-devel protobuf-compiler openssl-devel \
      zlib-devel libutempter-devel libcurl-devel gflags-devel
  else
    dnf install -y --setopt=install_weak_deps=False \
      bash curl findutils gflags libatomic libcurl libsodium libutempter \
      ncurses-libs openssh-clients openssh-server openssl-libs perl-interpreter \
      procps-ng protobuf shadow-utils sudo tmux zlib-ng-compat
  fi
  dnf clean all
elif command -v pacman >/dev/null 2>&1; then
  # pacman 7's download sandbox uses seccomp, which fails under qemu user
  # emulation (the Arch cell runs linux/amd64 on arm64 runners). Disabling the
  # syscall sandbox in this disposable build container is safe and keeps the
  # cell buildable on both architectures.
  sed -i 's/^#DisableSandboxSyscalls/DisableSandboxSyscalls/' /etc/pacman.conf
  # One download at a time is slower but survives qemu user emulation and
  # flaky mirrors; pacman's default parallel retrieval occasionally loses a
  # package mid-transaction under emulation.
  sed -i 's/^#ParallelDownloads.*/ParallelDownloads = 1/' /etc/pacman.conf
  pacman -Syu --noconfirm --needed --disable-download-timeout
  if [ "$mode" = build ]; then
    pacman -S --noconfirm --needed \
      base-devel cmake git pkgconf libsodium protobuf openssl zlib \
      libutempter curl gflags
  else
    pacman -S --noconfirm --needed \
      bash curl findutils ncurses openssh perl procps-ng protobuf libsodium \
      openssl zlib libutempter curl gflags sudo tmux
  fi
  pacman -Scc --noconfirm
else
  echo "install-deps: unsupported distribution (no apt-get, dnf, or pacman)" >&2
  exit 1
fi
