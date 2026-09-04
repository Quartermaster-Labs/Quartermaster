# Docker

One image: the Quartermaster server plus working inference backends, for
`linux/amd64` and `linux/arm64`.

The backends are **downloaded, not compiled**. They are the same upstream
release assets `internal/backends/catalog.go` points at, so a container and a
laptop run the same bytes, and the build costs seconds rather than the hours
the old compile-from-source image did. They are baked in at build time, so
`docker run` gives you a serving instance with no egress and no setup step, and
the image tag fully determines what runs inside it.

What ships:

| Component | Variant | amd64 | arm64 |
|---|---|---|---|
| `llama-server` (llama.cpp) | Vulkan | yes | yes |
| `sd-server` (stable-diffusion.cpp) | Vulkan | yes | no build published upstream |

Vulkan is the only Linux GPU variant worth baking in: upstream publishes no
Linux CUDA build of llama.cpp at all, so even an NVIDIA host installs Vulkan,
and the same binary covers AMD and Intel. It also runs on CPU when no GPU is
visible.

## Run it

```shell
docker run -d --name quartermaster \
  -p 127.0.0.1:1250:8080 \
  -v /path/to/models:/data/models \
  ghcr.io/quartermaster-labs/quartermaster:latest
```

Then open <http://localhost:1250>.

| Path | What |
|---|---|
| `/data/models` | your GGUFs. Mount your real models folder here |
| `/data/config` | the autogen control file and the generated `config.yaml` |
| `/opt/quartermaster/backends` | the baked-in backends (`QM_BACKENDS_DIR`) |

The config is generated from whatever is under `/data/models` on each start.
The control file is seeded on first start if it is missing and never touched
again: after that it belongs to you and to the dashboard.

To keep backends you install yourself across container recreates, mount a
**named** volume at `/opt/quartermaster/backends`. Docker seeds an empty named
volume from the image, so the baked-in builds are copied in rather than hidden.
A bind mount would shadow them.

## Who can reach the dashboard

`127.0.0.1:1250:8080` in the command above is deliberate. On the desktop the
dashboard and the `/api/*` routes are restricted to the machine they run on, by
source address. That check cannot work in a container: publishing a port NATs
every request through the bridge gateway, so the server sees one non-loopback
address for your own browser and for anyone else on the network alike. The
image therefore starts with `-admin-open` (otherwise it would fail closed, and
`/ui/` would answer 403), and the decision moves to what you publish the port
to.

Bind it to `0.0.0.0` only when you mean to share the dashboard, and remember
the inference API was never behind that gate: set `apiKeys` in your config to
put a key in front of it.

## GPUs

**On a Linux host:**

- **NVIDIA** - install the [container
  toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/)
  and add `--gpus all`.
- **AMD / Intel** - pass the render node and the host's Vulkan ICD:
  `--device /dev/dri -v /usr/share/vulkan/icd.d:/usr/share/vulkan/icd.d:ro`.
  The image carries the Vulkan *loader* only; the driver comes from the host.
- **No GPU** - it still serves, on CPU. Expect diffusion to be very slow.

**On Docker Desktop (Windows/macOS) there is no Vulkan device**, and this is
not a flag you are missing, nor a driver you can install. Containers run inside
a VM, and on Windows all that reaches Linux is `/dev/dxg`, a shim onto the
Windows GPU stack. Mesa's AMD driver (RADV) reaches its GPU through a DRM
render node at `/dev/dri`, which WSL2 never creates, so it loads and enumerates
nothing: measured on an RX 7900 XTX with a healthy host driver,
`llama-server --list-devices` answers `Available devices: (none)`. NVIDIA is
the exception, because its Windows driver ships a Linux Vulkan ICD into
`/usr/lib/wsl/lib` that speaks `/dev/dxg` directly rather than DRM: `--gpus
all`, with the container toolkit installed in your WSL distribution. AMD's
route is ROCm-on-WSL, which needs the ROCm build rather than the bundled
Vulkan one.

ROCm is not bundled: it would add ~460 MB compressed for one vendor. Install it
from Settings -> Backends if you want it, over a named volume so it persists.

## Build it yourself

```shell
docker buildx build -f docker/app/Dockerfile -t quartermaster:local .
```

The context is the repo root and the source is copied in, so the image runs the
tree you are sitting in. `linux/arm64` cross-compiles rather than emulating, so
`--platform linux/arm64` is no slower than the native build.

The backend versions are pinned as build args, so bumping them is a visible
commit rather than a silent drift:

```shell
docker buildx build -f docker/app/Dockerfile \
  --build-arg LLAMA_TAG=b10796 \
  --build-arg SD_TAG=master-841-6b3edaa \
  -t quartermaster:local .
```

## Self-update

Blocked, by design: the image is the unit of update, and a binary swapped
inside a container is erased by the next `docker run`. Pull a new tag instead.
