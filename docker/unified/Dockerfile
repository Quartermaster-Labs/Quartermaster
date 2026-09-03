# Unified multi-stage Dockerfile for AI inference tools
# Supports CUDA and Vulkan backends via BACKEND build arg
#
# Usage:
#   docker buildx build --build-arg BACKEND=cuda -t quartermaster:unified-cuda .
#   docker buildx build --build-arg BACKEND=vulkan -t quartermaster:unified-vulkan .
#   docker buildx build --build-arg BACKEND=cuda --build-arg CMAKE_CUDA_ARCHITECTURES="86;89" -t quartermaster:unified-cuda .
#
# Each project has its own install script that handles cloning, building,
# and installing binaries. Build stages are independent for cache efficiency.

ARG BACKEND=cuda

# BUILD_JOBS caps `cmake --build -j` in every compile stage. Empty means
# $(nproc), which is right locally and wrong in CI: BuildKit runs whisper-build,
# sd-build and llama-build concurrently because they are independent stages, so
# on a 4-core runner an unbounded -j gives 12 parallel C++ compiles for no gain
# (there are still only 4 cores) and enough resident memory to get the runner
# OOM-killed. A CI build passes 2.
ARG BUILD_JOBS=

# ── Builder bases ──────────────────────────────────────────────────────

FROM nvidia/cuda:12.9.1-devel-ubuntu24.04 AS builder-base-cuda

# Turing, Ampere, Ada. Pascal (60;61) is dropped: ggml's CUDA kernels want at
# least sm_70 to be worth anything, and a Pascal card is better served by the
# vulkan image. `-real` suppresses PTX, which halves device codegen per arch at
# the cost of forward compatibility with an architecture not listed here, so a
# newer card wants an explicit --build-arg rather than JIT from PTX.
#
# This list is the single biggest lever on build time: every entry compiles
# every .cu file again, and the five-arch default with PTX is what pushed the
# CUDA job past GitHub's hard 6h ceiling.
ARG CMAKE_CUDA_ARCHITECTURES="75-real;86-real;89-real"
ENV DEBIAN_FRONTEND=noninteractive
ENV CMAKE_CUDA_ARCHITECTURES=${CMAKE_CUDA_ARCHITECTURES}
ENV CCACHE_DIR=/ccache
ENV CCACHE_MAXSIZE=2G
ENV PATH="/usr/lib/ccache:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git python3 python3-pip libssl-dev \
    curl ca-certificates ccache make wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# ──

FROM ubuntu:24.04 AS builder-base-vulkan

ENV DEBIAN_FRONTEND=noninteractive
ENV CCACHE_DIR=/ccache
ENV CCACHE_MAXSIZE=2G
ENV PATH="/usr/lib/ccache:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git python3 python3-pip libssl-dev \
    curl ca-certificates ccache make wget software-properties-common \
    libvulkan-dev glslang-tools spirv-tools vulkan-validationlayers glslc \
    spirv-headers \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# ── Select builder base by BACKEND ────────────────────────────────────

FROM builder-base-${BACKEND} AS builder-base

# ── Build whisper.cpp (fastest build, run first) ──────────────────────

FROM builder-base AS whisper-build
ARG BACKEND=cuda
ARG BUILD_JOBS=
ARG WHISPER_COMMIT_HASH=master
COPY docker/unified/install-whisper.sh /build/
RUN --mount=type=cache,id=ccache-${BACKEND},target=/ccache \
    --mount=type=cache,id=whisper-${BACKEND},target=/src/whisper.cpp/build \
    BACKEND=${BACKEND} BUILD_JOBS=${BUILD_JOBS} bash /build/install-whisper.sh "${WHISPER_COMMIT_HASH}"

# ── Build stable-diffusion.cpp ────────────────────────────────────────

FROM builder-base AS sd-build
ARG BACKEND=cuda
ARG BUILD_JOBS=
ARG SD_COMMIT_HASH=master
COPY docker/unified/install-sd.sh /build/
RUN --mount=type=cache,id=ccache-${BACKEND},target=/ccache \
    --mount=type=cache,id=sd-${BACKEND},target=/src/stable-diffusion.cpp/build \
    BACKEND=${BACKEND} BUILD_JOBS=${BUILD_JOBS} bash /build/install-sd.sh "${SD_COMMIT_HASH}"

# ── Build llama.cpp (slowest build, run last) ─────────────────────────

FROM builder-base AS llama-build
ARG BACKEND=cuda
ARG BUILD_JOBS=
ARG LLAMA_COMMIT_HASH=master
COPY docker/unified/install-llama.sh /build/
RUN --mount=type=cache,id=ccache-${BACKEND},target=/ccache \
    --mount=type=cache,id=llama-${BACKEND},target=/src/llama.cpp/build \
    BACKEND=${BACKEND} BUILD_JOBS=${BUILD_JOBS} bash /build/install-llama.sh "${LLAMA_COMMIT_HASH}"

# ── Build ik_llama.cpp (CUDA only) ────────────────────────────────────
#
# Two named stages allow ARG BACKEND to select at build time:
#   - ik-llama-cuda  : real build (from builder-base-cuda)
#   - ik-llama-vulkan: no-op (empty /install/bin, skips CUDA pull entirely)
# BuildKit only evaluates the selected branch, so vulkan builds never
# pull nvidia/cuda:*-devel or compile ik_llama.cpp.

FROM builder-base-vulkan AS ik-llama-vulkan
RUN mkdir -p /install/bin

FROM builder-base-cuda AS ik-llama-cuda
ARG BUILD_JOBS=
ARG IK_LLAMA_COMMIT_HASH=main
COPY docker/unified/install-ik-llama.sh /build/
RUN --mount=type=cache,id=ccache-cuda,target=/ccache \
    --mount=type=cache,id=ik-llama-cuda,target=/src/ik_llama.cpp/build \
    BUILD_JOBS=${BUILD_JOBS} bash /build/install-ik-llama.sh "${IK_LLAMA_COMMIT_HASH}"

ARG BACKEND=cuda
FROM ik-llama-${BACKEND} AS ik-llama-build

# ── Build quartermaster from source ──────────────────────
#
# Two stages, because the toolchains do not overlap and neither of them is in
# builder-base: the dashboard is Vite 8 / Svelte 5 and wants Node 24 (Ubuntu
# 24.04 ships Node 18, too old to run the build), while the server is Go 1.26.
#
# The order is a hard requirement, not a preference. `vite build` writes into
# internal/server/ui_dist, and `//go:embed ui_dist` in internal/server/ui.go
# compiles that directory INTO the binary -- a Go build that runs first yields
# a server whose dashboard is an empty embed.

FROM node:24-bookworm-slim AS quartermaster-ui
# The build context is the REPO ROOT, and the source is COPYed rather than
# cloned: the repo is private, so `git fetch` inside a build stage has no
# credentials and dies with "could not read Username for https://github.com".
# Copying also means a local build tests the working tree you are sitting in.
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

# ──

FROM golang:1.26 AS quartermaster-build
# `local_<hash>` is deliberately not parseable as a version: internal/update
# reads an unparseable version as a dev build and never polls GitHub or swaps
# the binary. That is the only correct behaviour in a container, where a
# self-applied update is erased by the next `docker run`.
ARG QM_COMMIT=unknown
ARG QM_VERSION=local_unknown
COPY --from=quartermaster-ui /src/quartermaster /src/quartermaster
WORKDIR /src/quartermaster
RUN mkdir -p /install/bin && echo "${QM_VERSION}" > /install/quartermaster-version
# CGO_ENABLED=0 keeps the README's "one static binary" true, and spares the
# runtime stage from having to match golang:1.26's libc.
RUN --mount=type=cache,id=go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X main.version=${QM_VERSION} \
        -X main.commit=${QM_COMMIT} \
        -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /install/bin/quartermaster ./cmd/quartermaster && \
    test -x /install/bin/quartermaster

# ── Runtime bases ─────────────────────────────────────────────────────

FROM nvidia/cuda:12.9.1-runtime-ubuntu24.04 AS runtime-cuda

ENV DEBIAN_FRONTEND=noninteractive
ENV LD_LIBRARY_PATH="/usr/local/cuda/lib64:${LD_LIBRARY_PATH}"
ENV PATH="/usr/local/bin:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
    libgomp1 python3 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# CUDA stub drivers for container compatibility
COPY --from=builder-base-cuda /usr/local/cuda/lib64/stubs/libcuda.so /usr/local/cuda/lib64/stubs/libcuda.so
COPY --from=builder-base-cuda /usr/local/cuda/lib64/stubs/libcuda.so /usr/local/cuda/lib64/stubs/libcuda.so.1

# ──

FROM ubuntu:24.04 AS runtime-vulkan

ENV DEBIAN_FRONTEND=noninteractive
ENV PATH="/usr/local/bin:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
    libgomp1 libvulkan1 mesa-vulkan-drivers \
    python3 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# ── Select runtime base by BACKEND ────────────────────────────────────

FROM runtime-${BACKEND} AS runtime

ARG BACKEND=cuda
ARG LLAMA_COMMIT_HASH=unknown
ARG WHISPER_COMMIT_HASH=unknown
ARG SD_COMMIT_HASH=unknown
ARG IK_LLAMA_COMMIT_HASH=unknown
ARG RUN_UID=0

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-numpy python3-sentencepiece python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user when RUN_UID != 0
RUN if [ "$RUN_UID" != "0" ]; then \
      groupadd --system --gid $RUN_UID quartermaster && \
      useradd --system --uid $RUN_UID --gid $RUN_UID \
        --home /app --shell /sbin/nologin quartermaster; \
    fi && \
    mkdir -p /etc/quartermaster/config && \
    chown -R ${RUN_UID}:${RUN_UID} /etc/quartermaster

WORKDIR /app

# Copy whisper.cpp binaries and libraries
COPY --from=whisper-build /install/bin/whisper-server /usr/local/bin/
COPY --from=whisper-build /install/bin/whisper-cli /usr/local/bin/
COPY --from=whisper-build /install/lib/ /usr/local/lib/

# Copy stable-diffusion.cpp binaries and libraries
COPY --from=sd-build /install/bin/sd-server /usr/local/bin/
COPY --from=sd-build /install/bin/sd-cli /usr/local/bin/
COPY --from=sd-build /install/lib/ /usr/local/lib/

# Copy llama.cpp binaries (statically linked)
COPY --from=llama-build /install/bin/llama-server /usr/local/bin/
COPY --from=llama-build /install/bin/llama-cli /usr/local/bin/

# Copy ik-llama-server (CUDA only; empty copy for vulkan)
COPY --from=ik-llama-build /install/bin/ /usr/local/bin/

# Install uv
RUN pip install uv --break-system-packages

# Copy quartermaster binary
COPY --from=quartermaster-build /install/bin/quartermaster /usr/local/bin/
COPY --from=quartermaster-build /install/quartermaster-version /tmp/

RUN ldconfig

COPY docker/unified/config.example.yaml /etc/quartermaster/config/config.yaml

# Version tracking
RUN echo "llama.cpp: ${LLAMA_COMMIT_HASH}" > /versions.txt && \
    echo "whisper.cpp: ${WHISPER_COMMIT_HASH}" >> /versions.txt && \
    echo "stable-diffusion.cpp: ${SD_COMMIT_HASH}" >> /versions.txt && \
    echo "ik_llama.cpp: ${IK_LLAMA_COMMIT_HASH}" >> /versions.txt && \
    echo "quartermaster: $(cat /tmp/quartermaster-version)" >> /versions.txt && \
    echo "backend: ${BACKEND}" >> /versions.txt && \
    echo "build_timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> /versions.txt

RUN mkdir -p /models && chown ${RUN_UID}:${RUN_UID} /models
WORKDIR /models
USER ${RUN_UID}
ENTRYPOINT ["quartermaster"]
CMD ["-config", "/etc/quartermaster/config/config.yaml", "-listen", "0.0.0.0:8080"]
