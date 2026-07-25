![quartermaster header image](docs/assets/hero3.webp)
![GitHub Downloads (all assets, all releases)](https://img.shields.io/github/downloads/Quartermaster-Labs/quartermaster/total)
![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/Quartermaster-Labs/quartermaster/go-ci.yml)
![GitHub Repo stars](https://img.shields.io/github/stars/Quartermaster-Labs/quartermaster)

# quartermaster

> **Fork of [llama-swap](https://github.com/mostlygeek/llama-swap) (MIT).** quartermaster
> started as a fork and has since diverged into its own project — it does **not** track upstream
> and has no plan to merge back. It keeps llama-swap's core (on-demand model swapping, OpenAI-compatible
> proxy, text/image/audio support, Anthropic API, single Go binary) and layers on automatic config
> generation, VRAM-aware load planning, multi-port catalogs with cross-port eviction, and a
> redesigned web UI with a standalone playground.

Run **any** generative AI model on your machine — text, image, audio — and hot-swap between them on
demand. quartermaster works with any OpenAI- or Anthropic-API-compatible server.

A **hassle-free yet fully customizable inference engine**: point it at your models folder and it
auto-generates a near-optimal setup — VRAM-aware context, GPU offload, and KV sizing computed per
model — so a less technical user gets a great config out of the box, while power users can tune every
knob.

Built in Go for performance and simplicity — a single binary and one config file. It orchestrates
your inference backends (llama-server, stable-diffusion.cpp, etc.); the Windows installer and unified
Docker image bundle them for you.

## Features

> 🆕 marks additions made in this fork; the rest is inherited from llama-swap.

- 🆕 **Automatic config generation** — discovers your GGUFs and emits a working config at startup
  (`-generate`). Kills hand-baked per-model config variants: ctx, GPU offload, CPU-MoE split, and
  KV-cache sizing are computed at runtime per model and per architecture (Gemma SWA, Qwen3.5/3.6 SSM,
  LFM2, etc.).
- 🆕 **VRAM-aware load planning** — samples free VRAM at startup and sizes models to fit. Per-arch
  KV math, derived MoE expert byte fractions, and a compute-buffer estimate keep large-vocab models
  from spilling.
- 🆕 **KV-cache persistence to disk** — snapshots a llama-server slot's KV-cache before
  eviction and restores it (instead of re-prefilling) when the conversation returns, so an expensive
  long chat survives being swapped out by a throwaway request. Also seeds brand-new conversations
  from a per-agent system+tools preamble cache to skip re-prefilling the static prefix.
- 🆕 **Multi-port catalogs + cross-port eviction** — bind N listeners on one shared
  router/scheduler, each with its own `/v1/models` view; loading a model on one port evicts a
  VRAM-exclusive model on another. One process, one GPU accounting.
- 🆕 **Live model reload** — watches the models folder and hot-reloads on add/remove without a
  restart (`-watch-models`).
- 🆕 **Redesigned web UI** — LM Studio-style per-model parameter editor (edit ctx/KV/spec, create
  named variants, reset to autogen default), collapsible variant groups, segmented VRAM/RAM gauges
  (system vs model), and a unified Observe page (activity + logs + performance).
- 🆕 **Standalone playground** — split onto its own port (`-playground-port`) with per-user login,
  server-side chat history, and a side-rail for Chat / Images / Speech / Transcription / Rerank /
  Load Test.
- 🆕 **Per-key model scoping** — API keys can be restricted to specific models, not just all-or-nothing.
- 🆕 **Safe LAN/tailnet exposure** — bind the API to `0.0.0.0` (or a tailnet address) and the
  dashboard, ops and config-editor endpoints — which are deliberately key-free so a bad key can
  never lock you out of your own UI — automatically answer to this host only. Widen with
  `-admin-allow 100.64.0.0/10`, or drop the gate entirely with `-admin-open`.
- ✅ Easy to deploy and configure: one binary, one configuration file; orchestrates your inference backends (llama-server, stable-diffusion.cpp, …)
- ✅ On-demand model switching
- ✅ Use any local OpenAI compatible server (llama.cpp, vllm, tabbyAPI, stable-diffusion.cpp, etc.)
  - future proof, upgrade your inference servers at any time.
- ✅ Broad API coverage:
  - **OpenAI** — chat/completions, responses, embeddings, models, audio (speech/transcription/voices), images (generation/edits)
  - **Anthropic** — messages, count_tokens
  - **llama-server** — rerank, infill, completion
  - **Stable Diffusion** — SDAPI txt2img / img2img / loras
  - **Ops** — `/upstream/:model`, `/running`, unload, `/logs[/stream]`, `/health`, `/metrics` (Prometheus)
  - See the [configuration docs](docs/configuration.md) for the full endpoint list.
- ✅ API Key support - define keys to restrict access to API endpoints
- ✅ Customizable
  - Run concurrent models with a custom DSL swap matrix
  - Automatic unloading of models after timeout by setting a `ttl`
  - Docker and Podman support using `cmd` and `cmdStop` together
  - Preload models on startup with `hooks`
  - Apply filters to requests to control inference with `stripParams`, `setParams` and `setParamsByID`

### Web UI

quartermaster includes a real time web interface with a playground for testing out all sorts of local models:

<img width="1125" height="876" alt="image" src="https://github.com/user-attachments/assets/8ee41947-97af-463d-b0f0-8e9c478fac07" />

View detailed token metrics:

<img width="1111" height="515" alt="image" src="https://github.com/user-attachments/assets/64bfb280-d7a3-4126-971a-a128fd40410c" />

Inspect request and responses:

<img width="1111" height="720" alt="image" src="https://github.com/user-attachments/assets/24fe4aca-1448-4d7c-b9e8-a967589bda6c" />

Manually load and unload models:

<img width="1109" height="719" alt="image" src="https://github.com/user-attachments/assets/02b1e1f2-abd0-4050-84ae-facd66ff01c4" />

Real time log streaming:

<img width="1107" height="559" alt="image" src="https://github.com/user-attachments/assets/39669a10-cff2-409e-836a-5bad8bd0140c" />

## Playground

The built-in playground is a full chat client over your local models (plus Images, Speech,
Transcription, Rerank, and Load Test tabs). It can run on its own port with per-user login
(`-playground-port`). Fork additions to the chat experience:

- **Web search** — toggle a `web_search` tool the model can call mid-conversation. Results come from
  your own [SearXNG](https://github.com/searxng/searxng) instance via a same-origin proxy
  (`/api/websearch`), so no third-party search key and no browser CORS headaches.
- **Clean reasoning toggle** — reasoning ("thinking") models stream their thought process into a
  collapsible block, kept out of the final answer. Flip it off to hide thinking entirely.
- **Rewrite tool** — a text-transformation mode: paste prose + an instruction ("make it formal",
  "translate to pirate"), and the result renders as a side-by-side **word-level diff** against the
  original so you see exactly what changed.
- **Chat sessions** — conversations are saved server-side per user (not just localStorage), with a
  history flyout to switch between and delete past chats.

## Installation

Pick whichever fits you:

1. **Windows installer** — easiest; bundles/fetches the inference backends
2. Docker (unified container)
3. Release binary (any OS)
4. From source

### Windows installer (recommended)

Download the latest `quartermaster-setup-*.exe` from the
[Releases page](https://github.com/Quartermaster-Labs/quartermaster/releases) and run it.

It's a per-user install (no admin/UAC needed). The wizard:

- downloads the inference backends (`llama-server` / `sd-server`) for your chosen acceleration
  (vulkan / cuda / cpu) — so you don't hunt them down yourself,
- seeds a starter `quartermaster-generate.yaml` you can edit, and
- optionally adds a logon-autostart shortcut.

On first run it discovers your GGUFs and auto-generates a config — point it at your models folder and
go.

### Docker Install ([download images](https://github.com/Quartermaster-Labs/quartermaster/pkgs/container/quartermaster))

The unified container bundles llama-server, ik-llama-server, stable-diffusion.cpp,
whisper.cpp and quartermaster. It is built for `cuda` and `vulkan` backends.

```shell
$ docker pull ghcr.io/quartermaster-labs/quartermaster:unified-cuda   # or :unified-vulkan

# run with a custom configuration and models directory
$ docker run -it --rm --runtime nvidia -p 9292:8080 \
 -v /path/to/models:/models \
 -v /path/to/custom/config.yaml:/etc/quartermaster/config/config.yaml \
 ghcr.io/quartermaster-labs/quartermaster:unified-cuda
```

> Images are built on demand from `.github/workflows/unified-docker.yml`
> (Actions → Build Unified Docker Image → Run workflow), or locally with
> `docker/unified/build-image.sh --cuda`.

### Release binary

Prefer a bare binary (or not on Windows)? Grab the archive for your OS from the
[Releases page](https://github.com/Quartermaster-Labs/quartermaster/releases). You supply your
own inference backends (`llama-server`, `sd-server`) and point your config at them.

### Building from source

1. Building requires Go and Node.js (for the UI).
2. `git clone https://github.com/Quartermaster-Labs/quartermaster.git`
3. Build for your platform: `make windows`, `make mac`, or `make linux` (each builds the UI first).
   - Or build a runnable bundle (binary + example configs + launcher/service files) plus an archive
     under `build/`: `make package-windows` (zip), `make package-linux` / `make package-mac` (tar.gz).
4. Look in `build/` for the binary.

## Configuration

```yaml
# minimum viable config.yaml

models:
  model1:
    cmd: llama-server --port ${PORT} --model /path/to/model.gguf
```

That's all you need to get started:

1. `models` - holds all model configurations
2. `model1` - the ID used in API calls
3. `cmd` - the command to run to start the server.
4. `${PORT}` - an automatically assigned port number

Almost all configuration settings are optional and can be added one step at a time:

- Advanced features
  - `matrix` to run concurrent models with a custom swap logic DSL
  - `hooks` to run things on startup
  - `macros` reusable snippets
- Model customization
  - `ttl` to automatically unload models
  - `aliases` to use familiar model names (e.g., "gpt-4o-mini")
  - `env` to pass custom environment variables to inference servers
  - `cmdStop` gracefully stop Docker/Podman containers
  - `useModelName` to override model names sent to upstream servers
  - `${PORT}` automatic port variables for dynamic port assignment
  - `filters` rewrite parts of requests before sending to the upstream server

See the [configuration documentation](docs/configuration.md) for all options.

## How does quartermaster work?

When a request hits an OpenAI- or Anthropic-compatible endpoint, quartermaster reads the
`model` value and loads the right upstream server to serve it. If the wrong server is running, it's
replaced with the correct one — that's the "swap." The upstream is spawned on demand and torn down on
a `ttl`, so only what you're using holds VRAM.

Most setups never write that upstream command by hand. With `-generate`, quartermaster discovers
your GGUFs at startup and emits a config for them: it reads each model's metadata, estimates its VRAM
footprint, and computes a near-optimal context length, GPU/CPU layer split, and KV-cache sizing to fit
your hardware. You can then tune any of it — per model, or as named variants — from the web UI or by
editing `quartermaster-generate.yaml`; changes hot-reload.

In the most basic configuration quartermaster handles one model at a time. For more advanced use
cases, a `matrix` runs multiple models concurrently, and multi-port listeners give each a scoped
catalog while a single shared scheduler keeps VRAM accounting honest across ports. You have complete
control over how your system resources are used.

## Reverse Proxy Configuration (nginx)

If you deploy quartermaster behind nginx, disable response buffering for streaming endpoints. By default, nginx buffers responses which breaks Server‑Sent Events (SSE) and streaming chat completion.

Recommended nginx configuration snippets:

```nginx
# SSE for UI events/logs
location /api/events {
    proxy_pass http://your-quartermaster-backend;
    proxy_buffering off;
    proxy_cache off;
}

# Streaming chat completions (stream=true)
location /v1/chat/completions {
    proxy_pass http://your-quartermaster-backend;
    proxy_buffering off;
    proxy_cache off;
}
```

As a safeguard, quartermaster also sets `X-Accel-Buffering: no` on SSE responses. However, explicitly disabling `proxy_buffering` at your reverse proxy is still recommended for reliable streaming behavior.

## Monitoring Logs on the CLI

```sh
# sends up to the last 10KB of logs
$ curl http://host/logs

# streams combined logs
curl -Ns http://host/logs/stream

# stream quartermaster's proxy status logs
curl -Ns http://host/logs/stream/proxy

# stream logs from upstream processes that quartermaster loads
curl -Ns http://host/logs/stream/upstream

# stream logs only from a specific model
curl -Ns http://host/logs/stream/{model_id}

# stream and filter logs with linux pipes
curl -Ns http://host/logs/stream | grep 'eval time'

# appending ?no-history will disable sending buffered history first
curl -Ns 'http://host/logs/stream?no-history'
```

## Do I need to use llama.cpp's server (llama-server)?

Any OpenAI compatible server would work. quartermaster was originally designed for llama-server and it is the best supported.

For Python based inference servers like vllm or tabbyAPI it is recommended to run them via podman or docker. This provides clean environment isolation as well as responding correctly to `SIGTERM` signals for proper shutdown.

## Star History

> [!NOTE]
> Thank you to everyone who has given this project a ⭐️!

[![Star History Chart](https://api.star-history.com/svg?repos=Quartermaster-Labs/quartermaster&type=Date)](https://www.star-history.com/#Quartermaster-Labs/quartermaster&Date)
