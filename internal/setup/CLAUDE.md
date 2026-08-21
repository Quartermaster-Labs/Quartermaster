# internal/setup — first-run wizard (server half)

The engine behind `cmd/quartermaster-setup`, the program a new Windows user
downloads. This package is a plain loopback HTTP server plus the install
sequence; it has never heard of a window. Everything platform-shaped —
WebView2, running the Inno installer, launching the app — arrives as a hook in
`Options`.

## Why it exists at all

The Inno installer used to ask five pages of questions (models folder, which
servers, download vs existing, compute backend, existing exe paths) and drive
`packaging/windows/fetch-backend.ps1` from `[Run]`. That duplicated
`internal/backends` badly: no GPU detection, no versioned side-by-side installs,
no staging, no rollback, no `peimports` preflight — and a bug in the cudart
extract deleted the llama-server binaries it had just downloaded, so every CUDA
install silently ended up unconfigured.

Now the wizard asks, `internal/backends` fetches, and Inno is driven with
`/VERYSILENT` for the three things only it provides: the Add/Remove Programs
record, the Start Menu group, and an in-place upgrade keyed to a stable AppId.

## Why a separate binary, not a flag

`TODO.md`'s "Desktop app — second binary" decision. Linkage is per-`main`, so a
`quartermaster -setup` flag would put WebView2 into the same Windows artifact
that runs headless in Docker and under systemd. Build tags do not help: guarding
the import behind `//go:build windows` still links it into the Windows *file*.
The two binaries share all of `internal/*` at the source level and none of it at
link time.

## Files

| File | What |
|---|---|
| `setup.go` | `Wizard`, `Options`, `Status`, `Phase`, the per-run token |
| `probe.go` | `NewProbe` (opening state), `GpuNames`, `Scan` (models-folder count) |
| `run.go` | `Start`/`run`/`Finish` — place → configure → backends → launch |
| `yaml.go` | `setSettingsKey` (line-based generate-file edit), `minimalGenerate` |
| `api.go` | routes, the token guard, `serveUI`, `Listen` |
| `ui_dist/` | the wizard's Svelte bundle (`npm run build:setup`), `//go:embed all:` |

## Gotchas

- **Loopback is not a trust boundary.** Any local process — and any page in a
  background browser tab — can POST to `127.0.0.1`. Every API route requires
  `X-QM-Setup-Token` (minted per run, injected into `index.html` at serve time so
  it never lands in a URL) *and* a loopback `Host` (`isLoopbackHost`, which is
  what stops DNS rebinding). A custom header also forces a preflight that we
  answer with no CORS headers at all.
- **The install runs on `context.Background()`**, not the request's context
  (`api.go`). A page reload mid-download must not abort a job already writing to
  disk.
- **`setSettingsKey` edits lines, never round-trips yaml.v3.** The generate file
  is the user's and is heavily commented; a re-marshal strips every comment. The
  scan is scoped to the top-level `settings:` block so it cannot rewrite a
  same-named key under `overrides:`, which is what a whole-file regex (the old
  PowerShell) did.
- **`probeComponents` is a short list, not `backends.Catalog()`.** First run is
  not the place to explain vLLM's Python requirement. Settings → Backends does
  the full thing against the same manager.
- **`DefaultVariant`, not `SuggestVariant`.** An NVIDIA card suggests CUDA, but
  llama.cpp publishes no Linux CUDA asset — the honest Linux recommendation is
  Vulkan.
- **`perf.GetGpuStats` is not nil-logger-safe** (`monitor_windows.go` calls
  `logger.Info` directly), hence `logmon.NewWriter(io.Discard)` in `GpuNames`.
- **`//go:embed all:ui_dist`** — the `all:` prefix is what makes the committed
  `.gitkeep` count, so the package compiles on a fresh clone before the UI is
  ever built. `.gitignore` negates that one path.
- **No native window off Windows** (`cmd/quartermaster-setup/window_other.go`).
  webkit2gtk/WebKit need cgo, which would drop the setup binary out of the
  `CGO_ENABLED=0` cross-compile matrix; the browser fallback shows the identical
  UI, and Linux installs are usually headless boxes configured over ssh.
