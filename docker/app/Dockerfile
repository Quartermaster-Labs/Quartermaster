# syntax=docker/dockerfile:1

# The Quartermaster server, with working backends, in one image.
#
# The image that used to live at docker/unified COMPILED llama.cpp,
# whisper.cpp and stable-diffusion.cpp from source on every build: hours of CI
# per compute backend, one image per API, and binaries subtly different from
# the ones every desktop install downloads.
#
# Compiling and bundling are separate decisions, and only the first one was
# wrong. Upstream already publishes the Linux binaries, so this image fetches
# the same release assets internal/backends/catalog.go points at (~75 MB,
# seconds) and bakes them in. `docker run` gives you a serving instance with no
# egress and no first-boot setup, and the image tag still fully determines what
# runs inside it -- which is the thing a runtime download would have cost.
#
#   docker buildx build -f docker/app/Dockerfile -t quartermaster:local .
#
# The build context is the REPO ROOT, and the source is COPYed rather than
# cloned: the repo is private, so `git fetch` inside a build stage has no
# credentials. Copying also means a local build tests the tree you are sitting
# in.

# ── Dashboard ─────────────────────────────────────────────────────────
#
# Two build stages, because the toolchains do not overlap: the dashboard is
# Vite 8 / Svelte 5 on Node 24, the server is Go 1.26.
#
# The order is a hard requirement, not a preference. `vite build` writes into
# internal/server/ui_dist, and `//go:embed ui_dist` in internal/server/ui.go
# compiles that directory INTO the binary -- a Go build that runs first yields
# a server whose dashboard is an empty embed.
#
# --platform=$BUILDPLATFORM on both stages: the output of each is either
# arch-independent (JS) or explicitly cross-compiled (Go), so pinning them to
# the builder's own architecture keeps the arm64 image off QEMU, where `npm ci`
# and a Go build cost roughly an order of magnitude more.

FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS ui
WORKDIR /src/quartermaster/ui-svelte
# Manifests first, so editing Go or Svelte source does not re-run `npm ci`.
# .npmrc is not optional: it carries legacy-peer-deps=true, and without it
# `npm ci` fails outright on @tailwindcss/vite, which still declares a peer
# range of vite ^5||^6||^7 while this project is on vite 8.
COPY ui-svelte/package.json ui-svelte/package-lock.json ui-svelte/.npmrc ./
RUN --mount=type=cache,id=npm,target=/root/.npm npm ci
# node_modules is not in the context (.dockerignore), so this merge leaves the
# tree installed above intact.
COPY . /src/quartermaster
# Dropped in the layer that used it: the Go stage copies this whole /src, and
# build-only dependencies would otherwise ride along into it.
RUN npm run build && rm -rf node_modules

# ── Server ────────────────────────────────────────────────────────────

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETARCH
# `local_<hash>` is deliberately not parseable as a version: internal/update
# reads an unparseable version as a dev build and never polls GitHub or swaps
# the binary. Release builds pass a real version and are still blocked from
# self-updating by restartMode(), but a dev image should not even poll.
ARG QM_COMMIT=unknown
ARG QM_VERSION=local_unknown
COPY --from=ui /src/quartermaster /src/quartermaster
WORKDIR /src/quartermaster
# CGO_ENABLED=0 keeps the README's "one static binary" true, and is also what
# makes GOARCH=arm64 a plain cross-compile with no toolchain to install.
RUN --mount=type=cache,id=go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=go-build-${TARGETARCH},target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w \
        -X main.version=${QM_VERSION} \
        -X main.commit=${QM_COMMIT} \
        -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/quartermaster ./cmd/quartermaster

# ── Prebuilt backends ─────────────────────────────────────────────────
#
# Downloaded, not compiled. See docker/app/fetch-backends.sh for why, and for
# the on-disk layout it produces.
#
# The tags are pinned rather than resolved to "latest" so an image tag fully
# determines what runs inside it: rebuilding this Dockerfile a month from now
# produces the same backends, and bumping them is a visible commit.

FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS backends
ARG TARGETARCH
ARG LLAMA_TAG=b10796
ARG SD_TAG=master-841-6b3edaa
# Pinned to the BUILDER and told the target explicitly: downloading and
# unpacking someone else's binaries is architecture-independent work, so
# running this stage under QEMU would be pure cost.
ENV LLAMA_TAG=${LLAMA_TAG} SD_TAG=${SD_TAG} TARGETARCH=${TARGETARCH}
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl jq unzip \
    && rm -rf /var/lib/apt/lists/*
COPY docker/app/fetch-backends.sh /fetch-backends.sh
RUN /fetch-backends.sh /out/backends

# ── Runtime ───────────────────────────────────────────────────────────
#
# ubuntu:24.04 and not a smaller base, because of what runs BESIDE the binary.
# Quartermaster itself is static and would be happy on scratch, but every
# Linux backend asset in internal/backends/catalog.go is an `ubuntu-*-x64`
# build from upstream CI. Matching the distribution they were built against is
# what makes a downloaded llama-server actually start.
#
# The three packages are the minimum those binaries need and no more:
#   ca-certificates - TLS roots, without which every backend and model
#                     download fails at the handshake
#   libgomp1        - OpenMP, dynamically linked by llama-server and sd-server
#   libvulkan1      - the Vulkan LOADER only. The ICD comes from the host
#                     driver you bind-mount; mesa-vulkan-drivers would add a
#                     software rasteriser nobody wants to run inference on.
#
# There is no CUDA layer. An NVIDIA host gets CUDA from the container toolkit
# (`--gpus all`), and upstream publishes no Linux CUDA build for llama.cpp
# anyway, so the Vulkan variant is what a Linux NVIDIA box installs.

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates libgomp1 libvulkan1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/quartermaster /usr/local/bin/quartermaster

# The backends live on an IMAGE path, not on the /data volume, and that is the
# whole point: a `docker run` with no volume at all still has a working server.
# Mounting a NAMED volume here is still supported and is what you want for
# persistent extra installs -- Docker seeds an empty named volume from the image,
# so the baked-in builds are copied in rather than hidden. A bind mount would
# shadow them, which is why this is not under /data.
ENV QM_BACKENDS_DIR=/opt/quartermaster/backends
COPY --from=backends /out/backends /opt/quartermaster/backends

# Everything the user owns lives under /data, so one volume covers the lot.
RUN mkdir -p /data/config /data/models

COPY docker/app/entrypoint.sh /usr/local/bin/entrypoint.sh

VOLUME ["/data"]
WORKDIR /data
EXPOSE 8080

# -generate, not a hand-written config: the container has no setup wizard, so
# the config is derived from whatever GGUFs are under /data/models on each
# start. The entrypoint seeds the control file the first time; after that the
# dashboard owns it.
#
# -admin-open, because the loopback gate cannot work here and silently fails
# closed without it. Publishing a port NATs every request through the bridge
# gateway (172.17.0.1), so internal/server/admin.go sees one non-loopback
# source for the browser on this machine and for a stranger on the LAN alike --
# the origin is erased before the server can judge it, and `docker run -p
# 1250:8080` then serves 403 on /ui/ and every /api/ route. Exposure is decided
# one layer up instead, by what you publish the port to: `-p
# 127.0.0.1:1250:8080` keeps the dashboard on this machine. The inference API
# was never behind this gate; gate that with apiKeys.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["-generate", "/data/config/quartermaster-generate.yaml", "-config", "/data/config/config.yaml", "-listen", "0.0.0.0:8080", "-admin-open"]
