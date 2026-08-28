# internal/setup — first-run wizard

## Purpose

The wizard a fresh machine sees before quartermaster exists on disk: **where to install, where the
models live, which compute backend to fetch** — then it does the work. It places the files (driving
the Inno installer silently on Windows), seeds `quartermaster-generate.yaml`, downloads the chosen
backends through `internal/backends`, registers them in the sidecar backend list, and launches the
result.

Three pieces, in different places on purpose:

| Piece | Where | What it is |
|---|---|---|
| The wizard itself | `internal/setup` | State machine + loopback HTTP API + embedded UI bundle. **Has never heard of a window.** |
| The program | `cmd/quartermaster-setup` | A second `main`: native WebView2 window, the embedded Inno installer, the platform `place`/`launch` hooks. |
| The UI | `ui-svelte/src/setup/` | A second Vite bundle (`vite.setup.config.ts`) → `internal/setup/ui_dist`, embedded by `api.go`. |

**Why a second binary and not `quartermaster -setup`:** linkage is per-`main`. A flag would put
WebView2 into the binary that runs headless in Docker and under systemd, and build tags do not help
because the import is still in the same program. See TODO.md "Desktop app - second binary".

**Why the platform work arrives as hooks:** `Options.Place` / `Options.Launch`. Placing files is the
one genuinely OS-shaped step, and keeping it in the command means this package builds identically
everywhere and the 20 MB installer blob stays in the binary that ships it.

## Key files

| File | Role |
|---|---|
| `setup.go` | Package doc, `Phase`, `Choices`, `Status`, `Options`, `Wizard` + the state mutators (`step`, `fail`, `warn`) and the run token. |
| `api.go` | `Handler()` (the five `/api/setup/*` endpoints), `guard` (token + loopback Host), `serveUI` (embedded bundle, token injected into `index.html`), `Listen()`. `//go:embed all:ui_dist`. |
| `run.go` | The install itself: `Start`, `run`, `ensureGenerate` (reports whether it created the file), `seedBudgets` (measures this box's VRAM/RAM into the freshly created generate file — first run only, never over a repair run), `installBackends`, `awaitJob`, `registerBackend`, `Finish`. |
| `probe.go` | Opening state: `GpuNames`, `NewProbe` (variant list from the llama-server catalog entry, `probeComponents`), and `Scan` — the real discovery walk over a candidate models folder. |
| `yaml.go` | `setSettingsKey` — line-level, comment-preserving edit of `settings.<key>` in the generate file. `minimalGenerate` for when there is no example to seed from. |
| `ui_dist/` | The built wizard bundle. Holds a committed `.gitkeep` so the `//go:embed` compiles on a tree where the UI was never built. |
| `cmd/quartermaster-setup/main.go` | `runtime.LockOSThread`, flags (`-dir`, `-browser`, `-v`), `Listen` → `runWindow` → browser fallback, `defaultInstallDir`, `fatal`. |
| `cmd/quartermaster-setup/window_windows.go` | `runWindow` — creates the webview, hands it to `nativewin.Attach`, navigates, and closes it when the wizard signals done. The window mechanics themselves live in `internal/nativewin`, shared with the app window. |
| `cmd/quartermaster-setup/place_windows.go` | `//go:embed inno/setup.exe`, `placeInno` (silent `/VERYSILENT /DIR= /TASKS= /LOG=`), `launch` — starts the installed exe with no arguments (it supplies its own; see `bundle.go`). |
| `cmd/quartermaster-setup/place_other.go`, `place_common.go` | Unix install: `placeCopy` when a binary sits beside the wizard, `update.FetchBinary` when none does. `placeCopy` is also the dev-build stand-in on Windows when no installer is embedded. |
| `cmd/quartermaster-setup/window_other.go` | `runWindow` that always fails, so main falls back to the browser. Deliberate, not a gap. |

## Important types & functions

- **`Choices`** — `Dir`, `ModelsRoot` (may be empty: "I'll pick later"), `Variant`, `Components`,
  `StartMenu`, `DesktopIcon`, `Autostart`. Crosses the HTTP boundary as JSON and is handed whole to
  `Place`, because the installer answers more than one question (the last three become an Inno
  `/TASKS=` list).
- **`Status`** — one struct, one endpoint. There is nothing to correlate by id: the wizard has a
  single linear job. `Warnings` accumulate and are shown on the final screen.
- **`Phase`** — `idle → placing → configuring → backends → done`, or `error`. The only thing the UI
  switches on; finer detail rides in `Step`/`Detail`.
- **`Start`** — refuses a second concurrent run (two of them would interleave writes to the same
  generate file), but a **finished or failed run can be restarted**, so a user who fixes a bad path
  and clicks again gets a retry rather than a dead window. Runs on `context.Background()`, not the
  request's: a page reload mid-download must not abort a job already writing to disk.
- **`run`** — the ordering. Only backend installs degrade to warnings; a missing binary or an
  unwritable generate file is fatal. `c.Dir` **and** a non-empty `c.ModelsRoot` are created, the
  latter as a warning rather than a fatal (the path may be on a drive that is not attached, which is
  a fine answer to "where will your models live").
- **`registerBackend`** — `(*server.Server).registerManagedBackend` minus the config regeneration
  (nothing is running yet, and the sidecar is folded into autogen's inputs hash, so first boot
  regenerates anyway). Same default rule as the server: **first backend of a class wins**, so a
  wizard run over an existing install cannot silently repoint a class the user had configured.
  A component with an empty `Kind` (yt-dlp) is installed but never registered as a backend.
- **`NewProbe`** — variant labels/notes come from the **llama-server catalog entry**, not retyped
  here, so the wizard and Settings → Backends cannot drift. It uses `DefaultVariant`, not
  `SuggestVariant`: an NVIDIA card suggests CUDA, but llama.cpp publishes no Linux CUDA asset, so on
  Linux the honest recommendation is Vulkan. A variant with no pattern for this GOOS is not offered.
- **`probeComponents`** — a deliberately SHORT list (llama-server, sd-server, yt-dlp), all three
  default-selected. Not `backends.Catalog()`: first run is not the place to explain vLLM's Python
  requirement or expose per-version pickers.
- **`Scan`** — runs `autogen.DiscoverGgufModels`, not a `*.gguf` count, so the number shown is the
  number of rows the dashboard will have (shards collapse, projectors and draft sidecars pair to
  their parent). The walk is detached and the caller's deadline wins; a slow network share answers
  "still scanning" instead of wedging the wizard.
- **`setSettingsKey`** — edits **lines**, never round-trips through yaml.v3, which discards comments
  on any node it rebuilds. Scoped to the top-level `settings:` block and stops at the next unindented
  line, so it cannot rewrite a same-named key inside `overrides:` (which the old PowerShell installer
  did).
- **`runWindow`** (Windows) — creates the webview, strips the caption, applies the icon, binds
  `qmDrag` / `qmMinimize` / `qmMaximize` / `qmClose` / `qmPickFolder`, then navigates.

## Gotchas / conventions

- **Loopback is not a trust boundary.** Any page in any background tab can POST to `127.0.0.1`, and
  this server runs an installer. Every mutating endpoint requires a per-run token in the
  **`X-QM-Setup-Token` header** — a custom header forces a CORS preflight, which is answered with no
  CORS headers at all — plus `isLoopbackHost` against DNS rebinding. The token is injected into
  `index.html` at serve time so it never appears in a URL a referrer could carry off-machine.
- **The window itself is not this package's problem.** Frameless setup, the `WM_NCCALCSIZE` top-edge
  fix, the drag/min/max/close bindings, the icon and the folder picker all live in
  [`internal/nativewin`](../nativewin/CLAUDE.md) — read that before touching anything that draws.
- **A `.syso` is linked by the `main` package beside it**, which is why
  `cmd/quartermaster-setup/resource_windows_amd64.syso` exists separately from the repo-root one
  (`make versioninfo-setup`; both are committed, so a release build picks them up with no script
  change). That resource is the **file** icon Explorer shows; the taskbar/Alt-Tab icon is a separate
  job that `nativewin.ApplyIcon` does at runtime, and both are needed.
- **Inno `[Code]` runs even under `/VERYSILENT`**, and `/TASKS=` is passed unconditionally including
  empty: Inno's default when the switch is absent is "whatever the script marks checked", which would
  silently opt a user into a task they left unticked. Every task in the script (`startmenu`,
  `desktopicon`, `autostart`) is therefore marked `unchecked` and decided here — including the Start
  Menu group, which used to be unconditional. **Unticking removes**: Inno leaves icons from a
  previous install alone when their task is deselected, so `[InstallDelete]` deletes `{group}` and
  the desktop `.lnk` under `Tasks: not …`, which is what makes a second wizard run over an existing
  install able to take a shortcut away rather than only add one.
- **The unix wizard is a bootstrapper, not a copier.** Windows carries its payload (the embedded
  Inno package); unix has nothing to embed, so `place` copies the binary beside it when there is one
  (an unpacked tarball, a dev tree) and otherwise downloads the release asset through
  `update.FetchBinary`, verified against the release digest. `hasSiblingBinary` is what picks, and it
  matches `binaryGlobs` only: a stray `LICENSE` in a Downloads folder must not be read as a payload
  and send the install down a copy path with no binary in it. The setup programs ship as
  `quartermaster-setup-{linux-amd64,linux-arm64,darwin-arm64}`, built in `build-release.ps1` step 5b
  and hashed into `SHA256SUMS` with everything else.
- **A dev build embeds a placeholder installer**, so `place` checks `len(innoSetup)` against
  `minInstallerBytes` and falls back to `placeCopy` rather than executing a 0-byte exe.
- **The UI is a second bundle, not a dashboard route.** It must render before anything is installed
  and must not carry chart.js/mermaid/katex to draw three steps. Build it with
  `npm run build:setup`; a binary built without it serves an explanatory string, not a blank window.
- **`native.isNative`** (`ui-svelte/src/lib/native.ts`) is the feature test for the whole native
  layer — the custom title bar and the Browse buttons render only when the bridge is present, so the
  browser fallback degrades to an ordinary page with no build flag.
- **The exe carries its own launch flags; there is no launcher script any more.** Every entry point
  the installer creates -- Start menu, post-install "Launch now", the `{userstartup}` shortcut, and
  `launch()` here -- runs the exe directly. `bundle.go` (`applyBundleDefaults`) fills in `-config`,
  `-generate`, both listeners, `-watch-config` and `-app` whenever it finds
  `config\quartermaster-generate.yaml` beside the executable, resolving every path against the exe
  directory and `chdir`-ing there, so the flag set lives in one place and a shortcut cannot get it
  wrong. Only the autostart shortcut differs: it passes `-tray`, which suppresses the `-app`
  default, because a window appearing unasked at every login is not what ticking "start with
  Windows" means. There is no launcher script: `start.cmd` was retired once the flags moved into
  `bundle.go`, and `installer.iss` deletes both the stale `{app}\start.cmd` and any `{userstartup}`
  shortcut still aimed at it.
- **The installed binary is `Quartermaster.exe`, the release asset is not.** CI still publishes
  `quartermaster-windows-amd64.exe` because `internal/update.assetName()` matches that name exactly;
  `build-release.ps1` stages a second copy under the capitalised name for the installer only, and
  `installer.iss` excludes the asset-named one from `{app}` plus deletes it on upgrade. `launch()`
  in `place_windows.go` tries both names, in that order, so a dev-tree `placeCopy` (which only has
  the build artifact) still starts. `filepath.Glob` is case-sensitive, hence the explicit
  `Quartermaster.exe` entry in `devCopyGlobs`.
- **A second double-click raises the first window instead of failing.** `-app` probes
  `GET /api/app/show` on loopback before it touches the config, and exits if a running instance
  answers -- see `singleinstance.go`. Doing this later would be too late: the boot path in between
  runs `autogen.EnsureConfig`, which can rewrite the generated config the running instance is
  watching.

## Build

- `make setup-windows` — UI bundle + `-H=windowsgui` binary into `build/`.
- `make versioninfo-setup` — regenerate the icon/version resource after editing `versioninfo.json`.
- `npm run build:setup` (from `ui-svelte/`) — the UI bundle alone.
- `packaging/windows/build-release.ps1` copies the compiled Inno installer over
  `cmd/quartermaster-setup/inno/setup.exe` before building the release wizard.

## Connections

Depends on: `internal/autogen` (model discovery for `Scan`, sidecar backend list), `internal/backends`
(the download/install manager and the catalog the variant list is read from), `internal/perf` +
`internal/logmon` (the one-shot GPU probe), `internal/peimports` (the missing-DLL preflight hint).

Called by: `cmd/quartermaster-setup` only. Nothing in the server imports this package, and nothing
here imports the server.
