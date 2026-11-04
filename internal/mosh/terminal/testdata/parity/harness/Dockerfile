# Reproducible stock mosh 1.4.0 terminal/display oracle.
#
# Build from the repository root:
#   docker build -t natcatty/mosh-parity:1.4.0 \
#     internal/mosh/terminal/testdata/parity/harness
#
# The image contains the unmodified stock mosh 1.4.0 source tree, the stock
# mosh-client/mosh-server binaries, and /usr/local/bin/mosh-terminal-ref, a
# small driver linked against the stock terminal sources. The driver emits the
# exact Display::new_frame bytes and deterministic screen dumps used by
# internal/mosh/terminal/stock_parity_test.go.
#
# Source provenance:
#   https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz
#   sha256 872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd
FROM ubuntu:24.04

ARG MOSH_VERSION=1.4.0
ARG MOSH_SHA256=872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd
ARG MOSH_URL=https://github.com/mobile-shell/mosh/releases/download/mosh-${MOSH_VERSION}/mosh-${MOSH_VERSION}.tar.gz

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl pkg-config \
      libssl-dev zlib1g-dev libprotobuf-dev protobuf-compiler \
      libncurses-dev libutempter-dev \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "${MOSH_URL}" -o /tmp/mosh.tar.gz \
 && echo "${MOSH_SHA256}  /tmp/mosh.tar.gz" | sha256sum -c - \
 && mkdir -p /src \
 && tar -xzf /tmp/mosh.tar.gz -C /src \
 && cd "/src/mosh-${MOSH_VERSION}" \
 && ./configure --prefix=/usr/local \
 && make -j"$(nproc)" \
 && make install \
 && rm -f /tmp/mosh.tar.gz

COPY config.h /harness/config.h
COPY driver.cc /harness/driver.cc
RUN g++ -std=c++11 -O2 -I/harness \
      -I"/src/mosh-${MOSH_VERSION}/src/util" \
      -I"/src/mosh-${MOSH_VERSION}/src/terminal" \
      -o /usr/local/bin/mosh-terminal-ref /harness/driver.cc \
      "/src/mosh-${MOSH_VERSION}/src/terminal/parser.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/parseraction.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/parserstate.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminal.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminaldispatcher.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminaldisplay.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminalframebuffer.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminalfunctions.cc" \
      "/src/mosh-${MOSH_VERSION}/src/terminal/terminaluserinput.cc"

ENTRYPOINT ["/usr/local/bin/mosh-terminal-ref"]
