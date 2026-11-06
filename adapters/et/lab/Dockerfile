# A stock etserver lab endpoint for one matrix cell.
# Only stable Dockerfile syntax is used: BuildKit's built-in frontend avoids a
# Docker Hub round trip before the pinned base image is even resolved.
#
# The image builds EternalTerminal from a pinned upstream commit inside the
# cell's own distribution, so the binary is compiled and linked against that
# distribution's libsodium, protobuf, OpenSSL, and libc. Nothing in this image
# is Natcatty code: it is real stock etserver/etterminal plus sshd and a test
# account. The build stage is discarded; the final image carries only the
# built binaries, their runtime libraries, sshd, and the lab setup.
#
# Build arguments:
#   BASE        distribution image; the same tag must be pinned in compose.yaml
#   ET_REF      upstream tag to clone
#   ET_COMMIT   full commit that ET_REF must resolve to (prevents a moved tag)
#   ET_VERSION  version string for the image label and setup script

ARG BASE=debian:12-slim
FROM ${BASE} AS build
ARG DEBIAN_FRONTEND=noninteractive
ARG ET_REF=et-v7.1.1
ARG ET_COMMIT=a8318ca4598756ca3c5f4711cce12a65faeff57d

COPY install-deps.sh /usr/local/bin/install-deps.sh
RUN chmod 0755 /usr/local/bin/install-deps.sh \
 && /usr/local/bin/install-deps.sh build

# The release tree vendors its dependencies under external_imported/; the
# external/ directories are old submodule links that DISABLE_VCPKG builds do
# not use. Cloning them recursively only adds minutes of network and sentry
# submodules to every cell.
RUN git clone --depth 1 --branch "${ET_REF}" \
      https://github.com/MisterTea/EternalTerminal.git /src \
 && test "$(git -C /src rev-parse HEAD)" = "${ET_COMMIT}" \
 || { echo "ET_REF ${ET_REF} is not ${ET_COMMIT}" >&2; exit 1; }

WORKDIR /src
# DISABLE_VCPKG builds against the distribution's libraries, which is the
# point of the cell. SENTRY/TELEMETRY are off so the lab is deterministic and
# needs no network authorization.
RUN cmake -S . -B build \
      -DCMAKE_BUILD_TYPE=Release \
      -DDISABLE_VCPKG=ON \
      -DBUILD_TESTING=OFF \
      -DDISABLE_SENTRY=ON \
      -DDISABLE_TELEMETRY=ON \
 && cmake --build build -j"$(nproc)" --target etserver etterminal et \
 && ./build/etserver --version

FROM ${BASE}
ARG BASE
ARG DEBIAN_FRONTEND=noninteractive
ARG ET_VERSION=7.1.1
LABEL org.opencontainers.image.title="natcatty et parity lab" \
      org.opencontainers.image.description="stock etserver ${ET_VERSION} on ${BASE}" \
      org.opencontainers.image.source="https://github.com/MisterTea/EternalTerminal"

COPY install-deps.sh /usr/local/bin/install-deps.sh
RUN chmod 0755 /usr/local/bin/install-deps.sh \
 && /usr/local/bin/install-deps.sh runtime

COPY --from=build /src/build/etserver /src/build/etterminal /src/build/et /usr/local/bin/

COPY setup-lab.sh /usr/local/bin/setup-lab.sh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/setup-lab.sh /usr/local/bin/entrypoint.sh \
 && /usr/local/bin/setup-lab.sh "${ET_VERSION}"

# sshd and etserver must both accept connections before the cell is used.
HEALTHCHECK --interval=2s --timeout=3s --start-period=5s --retries=60 \
  CMD bash -c 'exec 3<>/dev/tcp/127.0.0.1/22 && exec 4<>/dev/tcp/127.0.0.1/2022'

# The lab never runs as root from outside: sshd and etserver are the only
# services, and the test accounts are unprivileged except for the test-only
# passwordless sudo used to install the user-certificate CA.
EXPOSE 22 2022
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
