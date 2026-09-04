#!/bin/sh
# Bake upstream's PREBUILT backend binaries into the image.
#
# Note what this does not do: compile anything. The image this replaced built
# llama.cpp, whisper.cpp and stable-diffusion.cpp from source, which cost hours
# of CI per backend and produced binaries subtly different from the ones every
# desktop install downloads. These are the exact release assets
# internal/backends/catalog.go points at, so a container and a laptop run the
# same bytes -- they just arrive at build time instead of at first click.
#
# The layout is the one internal/backends/install.go expects:
#
#   <root>/bin/<component>/<version>-<variant>/  + .qm-install.json
#
# There is no central index: the manifest's presence is what makes a build
# "installed" (install.go:23), so writing one here is the whole handshake. The
# server's startup adopt pass then points the config at it.
#
# Vulkan only, on purpose. Upstream publishes no Linux CUDA build of llama.cpp
# at all, so Vulkan is what an NVIDIA box on Linux would install anyway, and it
# covers AMD and Intel with the same binary. ROCm would add ~460 MB compressed
# for one vendor.

set -eu

ROOT="${1:-/out/backends}"
API="https://api.github.com/repos"

# find_asset <repo> <tag> <extended-regex> -> asset download URL
find_asset() {
  curl -fsSL "${API}/$1/releases/tags/$2" \
    | jq -r --arg re "$3" '.assets[] | select(.name | test($re; "i")) | .browser_download_url' \
    | head -1
}

# install <component> <repo> <tag> <variant> <asset-regex> <exe-name>
install() {
  comp="$1"; repo="$2"; tag="$3"; variant="$4"; re="$5"; exe="$6"
  dir="${ROOT}/bin/${comp}/${tag}-${variant}"
  url="$(find_asset "$repo" "$tag" "$re")"
  if [ -z "$url" ]; then
    echo "ERROR: no asset matching /$re/ in ${repo}@${tag}" >&2
    exit 1
  fi
  asset="$(basename "$url")"
  echo "==> ${comp} ${tag} ${variant}: ${asset}"

  mkdir -p "$dir"
  tmp="$(mktemp -d)"
  curl -fsSL -o "${tmp}/${asset}" "$url"
  case "$asset" in
    *.zip)    unzip -q "${tmp}/${asset}" -d "$dir" ;;
    *.tar.gz) tar -xzf "${tmp}/${asset}" -C "$dir" ;;
    *) echo "ERROR: unhandled archive type ${asset}" >&2; exit 1 ;;
  esac
  rm -rf "$tmp"

  # Mirrors findExe(): the executable is somewhere under the extracted tree and
  # the manifest records it RELATIVE to the install dir, so the whole bundle
  # stays movable. Archives nest it differently (build/bin/, or the root), which
  # is exactly why neither side hardcodes a path.
  found="$(find "$dir" -type f -name "$exe" | head -1)"
  if [ -z "$found" ]; then
    echo "ERROR: ${exe} not found in ${asset}" >&2
    exit 1
  fi
  chmod -R +x "$(dirname "$found")"

  cat > "${dir}/.qm-install.json" <<JSON
{
  "component": "${comp}",
  "version": "${tag}",
  "variant": "${variant}",
  "exe": "${found#"$dir/"}",
  "asset": "${asset}",
  "installedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "sizeBytes": 0
}
JSON
}

# Which assets exist depends on the target architecture, and the two projects
# do not agree on what they publish.
#
# TARGETARCH is Docker's ("amd64" / "arm64"), passed in by the Dockerfile.
case "${TARGETARCH:-amd64}" in
  arm64)
    # llama.cpp ships an ubuntu-vulkan-arm64 build. stable-diffusion.cpp ships
    # nothing for Linux on arm64 -- only Darwin arm64 and Linux x86_64 -- so an
    # arm64 image has no image backend, and image generation is unavailable
    # until upstream publishes one. Failing the build over that would cost the
    # whole arm64 image for a component most arm64 hosts cannot accelerate
    # anyway (no GPU passthrough under Docker Desktop on Apple silicon).
    install llama-server ggml-org/llama.cpp "${LLAMA_TAG}" vulkan \
      '^llama-.*-bin-ubuntu-vulkan-arm64\.tar\.gz$' llama-server
    echo "note: stable-diffusion.cpp publishes no linux/arm64 build; skipping sd-server"
    ;;
  amd64)
    install llama-server ggml-org/llama.cpp "${LLAMA_TAG}" vulkan \
      '^llama-.*-bin-ubuntu-vulkan-x64\.tar\.gz$' llama-server
    install sd-server leejet/stable-diffusion.cpp "${SD_TAG}" vulkan \
      '^sd-.*ubuntu.*vulkan.*\.zip$' sd-server
    ;;
  *)
    echo "ERROR: unsupported TARGETARCH '${TARGETARCH}'" >&2
    exit 1
    ;;
esac

du -sh "$ROOT"
