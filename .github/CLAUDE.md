# .github: CI, releases and the Docker image

Everything here is automation, and most of it fails in ways that look like
nothing happening. This file is the list of those silences.

## What runs when

| Workflow | Fires on | Does |
|---|---|---|
| `go-ci.yml` / `go-ci-windows.yml` | push, PR | `make test-all` on Linux and Windows |
| `ui-tests.yml` | push, PR | `make test-ui` |
| `pages.yml` | push touching `ui-svelte/site/**` and the wiki corpus; `release: published`; `workflow_run` on Release; dispatch | builds `.site/` and deploys GitHub Pages |
| `release.yml` | dispatch only (`tag`, `draft`) | runs `packaging/windows/build-release.ps1` on a Windows runner, pushes the tag, uploads assets |
| `docker.yml` | push of a `v*` tag; `workflow_run` on Release; dispatch | builds and pushes the container image |

## The two GITHUB_TOKEN rules that bite

Both are anti-loop / anti-privilege-escalation measures, both are silent, and
both have already cost a release.

**1. An event raised with `GITHUB_TOKEN` starts no workflow.** A tag pushed by
`release.yml` therefore notifies nothing: `docker.yml`'s `push: tags: v*` and
`pages.yml`'s `release: published` are both dead for CI-made releases, and were
live only while releases were cut from a laptop. Both now chain with
`workflow_run` on Release instead. **A `workflow_run` job runs at the DEFAULT
BRANCH ref, not at the tag**, so the tag cannot be read from the context:
`docker.yml`'s first step resolves it with `gh release view --json tagName` and
checks that ref out explicitly.

**2. `GITHUB_TOKEN` cannot create or update anything under
`.github/workflows/`, and there is no `permissions:` key that grants it.** The
capability exists only on a PAT or App token. The consequence is not obvious: if
a tag being pushed carries a workflow file that differs from the one on the
default branch, GitHub reads the tag push as a workflow update and rejects the
whole push.

> **Never push a workflow edit while a release is in flight.** That is exactly
> how `v1.0.1` failed the first time: a `docker.yml` change landed on `main`
> after the release job had checked out, and the tag push died with
> `refusing to allow a GitHub App to create or update workflow`. The build had
> already succeeded; only the last step failed. Land workflow changes, let
> `main` settle, *then* dispatch the release.

`build-release.ps1` fails safe here: it refuses to upload assets under a tag
origin does not have, so a rejected push leaves no tag, no release and no
half-uploaded artifacts. Re-dispatching is the whole recovery.

Two more, cheaper: `workflow_dispatch` is only offered for workflows on the
**default branch**, so a branch-only workflow cannot be dispatched at all; and
`pages.yml` documents two one-time setup steps (creating the Pages site with an
owner token, and allowing tag deploys in the `github-pages` environment) that no
token can do for itself.

## Releasing

Dispatch `release.yml` with the tag and `draft`. It owns no build logic: it
installs the toolchain and calls `packaging/windows/build-release.ps1`, the same
script `make release` runs, because two builders would drift and the one that
ships would be the one nobody tested. Everything cross-compiles from one Windows
runner (CGO is off project-wide), so the Windows wizard, `linux/amd64`,
`linux/arm64` and `darwin/arm64` all come out of a single job.

The tag is created on the dispatched ref's HEAD, and the script refuses to
continue if the tag is not HEAD or origin already has it elsewhere.

## The Docker image

Built by `docker.yml` from `docker/app/Dockerfile`. User-facing docs live in
[`docker/app/README.md`](../docker/app/README.md); this is what an agent needs.

- **It downloads backends, it does not compile them.** The image this replaced
  built llama.cpp, whisper.cpp and stable-diffusion.cpp from source, once per
  compute backend: hours per run, a cache-warming matrix to fit under the 6h job
  ceiling, and binaries subtly unlike the ones desktop installs use.
  `docker/app/fetch-backends.sh` fetches the same release assets
  `internal/backends/catalog.go` points at, in seconds.
- **Tags and versions.** A release build (tag ref, or the Release chain) gets
  `:latest` + `:vX.Y.Z` and `QM_VERSION` is the tag. Anything else gets `:edge`
  and `edge_<sha>`. That string is deliberately unparseable by
  `internal/update`'s `semverRe`, so an edge image never polls; it used to read
  `local_<sha>`, which was a lie a pulled CI image then repeated in the
  dashboard. Self-update is blocked in a container regardless, by
  `inContainer()` in `internal/update/env.go`, so the version is honesty, not
  safety.
- **Build order is a hard requirement.** `vite build` writes
  `internal/server/ui_dist` and `//go:embed` compiles it into the binary, so a Go
  stage that runs first yields an empty dashboard.
- **arm64 is cross-compiled, never emulated.** The UI, Go and backend stages are
  pinned to `--platform=$BUILDPLATFORM`; only runtime layers are per-arch. Do not
  "fix" a build problem by adding QEMU.
- **Backends live at `/opt/quartermaster/backends`, not under `/data`.** A run
  with no volume at all must still work. A *named* volume there is seeded from
  the image by Docker (verified), so extra installs persist; a *bind mount* would
  shadow the baked-in builds, which is why the path is not under `/data`.
- **The image starts with `-admin-open` on purpose.** Docker NAT erases the
  request origin: publishing a port makes every request arrive from the bridge
  gateway, so the loopback check in `internal/server/admin.go` cannot tell the
  local browser from the LAN and fails closed with 403 on `/ui/` and every
  `/api/*` route. Exposure moves one layer up, to what the port is published to;
  the docs publish on `127.0.0.1`. The inference API was never behind that gate.

### Two known gaps, both arm64

- **No `sd-server` on linux/arm64.** stable-diffusion.cpp publishes no Linux
  arm64 build, so an arm64 image has no image backend. `fetch-backends.sh` says
  so and continues rather than failing.
- **Settings cannot install backends on linux/arm64.**
  `internal/backends/catalog.go` keys assets by OS only, with no arch dimension,
  and every `osLinux` pattern is x64: the llama ones require `-x64` so they match
  nothing, while `sd-server`'s Vulkan pattern
  (`(?i)^sd-.*ubuntu.*vulkan.*\.zip$`) does not constrain arch and would happily
  install the x86_64 asset. Baked-in backends work; the installer UI does not.
  Not fixed.

## Testing the image the way a stranger gets it

`docker pull` succeeds for a private image if the daemon holds a push token, so
it proves nothing on a machine that has built and pushed. Point Docker at a
throwaway config instead, which exercises the anonymous path and leaves real
credentials alone:

```shell
docker --config /tmp/anon pull ghcr.io/quartermaster-labs/quartermaster:latest
```

(with `/tmp/anon/config.json` containing `{}`). To check ghcr visibility without
pulling, fetch an anonymous token from `ghcr.io/token?scope=repository:...:pull`
and GET the manifest: a bare manifest GET returns 401 for public and private
images alike, so only the token flow distinguishes them.

## GPUs in containers

Linux hosts pass a device through. **Docker Desktop on Windows cannot**, and it
is not a missing flag: WSL2 exposes `/dev/dxg` (the D3D12 shim) and creates no
`/dev/dri`, so Mesa's RADV loads and enumerates nothing even with the ICD
installed. Measured on an RX 7900 XTX with a healthy host driver:
`llama-server --list-devices` answers `Available devices: (none)`. NVIDIA is the
exception because its Windows driver ships a Linux Vulkan ICD into
`/usr/lib/wsl/lib` that speaks `/dev/dxg` rather than DRM.
