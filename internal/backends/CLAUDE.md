# internal/backends

## Purpose

Downloads and manages inference-backend binaries from their upstream GitHub
releases — the LM-Studio-style "install a backend" flow behind Settings →
Backends. It knows which project ships each backend, picks the release asset
matching the host's GPU, unpacks it into a versioned folder under the bundle's
`bin/`, and reports what is installed.

It is the in-app replacement for `packaging/windows/fetch-backend.ps1`, which
could only run at install time and only wrote the generate YAML.

**It does not replace manual "bring your own path" backends, and cannot.** Three
of the fork's backends have no installable upstream release: `tts-server`
(qwentts.cpp, built locally), `sam3_server` (the user's own sibling repo), and
the hand-patched sd-server carrying vendored gfx1100 Tensile kernels (distinct
from upstream's stock ROCm build, which *is* installable). Managed installs
therefore *coexist* with hand-entered rows in one registry, distinguished by
`BackendEntry.Managed`.

## Key files

| File | Role |
|---|---|
| `catalog.go` | Package doc + the static `Component`/`Variant` table, asset-name matching (`SelectAsset`, `MatchAssets`), and GPU→variant selection (`SuggestVariant`, `DefaultVariant`). |
| `github.go` | Releases API client: `Release`/`Asset`, `ghClient.Releases` (30 newest, 10-minute cache, `GITHUB_TOKEN` when set, explicit rate-limit message), `pickRelease`, `validAssetURL`. |
| `install.go` | On-disk layout and I/O: `ComponentDir`/`InstallDir`, `Installed`/`AllInstalled`, the `.qm-install.json` manifest, `Uninstall`, `download` + `progressReader`, `extract` (zip / tar.gz) with `safeJoin`, `findExe`. |
| `derive.go` | Turns one **picked asset name** into a match pattern (`DerivePattern`, `DeriveUnique`), plus repo validation (`ValidateRepo`/`ParseRepo`), `SuggestLabel` and `ClosestAsset`. This is what lets a user track their own repo without writing a regex. |
| `manager.go` | `Manager` — the `Sources` hook that merges user-tracked repos into the catalog (`Catalog`/`Find`), `Resolve` (what would be downloaded right now), the job list, phases, and the `run()` install goroutine (resolve → download → stage → extract → manifest → rename → `OnInstalled`). |
| `backends_test.go` | Pins asset matching per component/OS, variant suggestion, zip-slip rejection, manifest-gated scanning, versioned dirs, release picking, URL allowlisting. |

## Important types & functions

- `Component` (`catalog.go`) — one installable backend: `Repo`, the autogen
  registry `Kind` it registers as (**empty for `yt-dlp`** — installed but never
  registered), per-GOOS `Exe` name, `Bare` (the asset IS the executable), and its
  `Variants`.
- `Variant` (`catalog.go`) — a GPU-runtime flavour
  (`vulkan`/`cuda`/`rocm`/`cpu`/`any`) holding per-GOOS asset-name regexes, plus
  `Extra` assets unpacked alongside (the separately-shipped cudart zips) and
  `PairKey`, the capture that ties an extra to its primary.
- `Manager` (`manager.go`) — owns the install root, the GitHub client and the
  job list. Callers wire two hooks: `GpuNames` (variant auto-selection) and
  `OnInstalled` (registry write-back).
- `Manager.Install(comp, variant, version)` (`manager.go`) — starts the job
  and returns its id; `version` "" or "latest" resolves to the newest
  non-prerelease. Refuses a second concurrent install **of the same component**;
  different components install in parallel.
- `Job` (`manager.go`) — `resolving → downloading → extracting → registering →
  done | error`, with byte counters. Errors land on the job, never on a caller.
- `Installed` (`install.go`) — one build on disk (absolute `Exe`, `Dir`,
  version, variant, size, install time).

## Data flow / how it works

1. **Resolve** — list the repo's 30 newest releases (cached 10 min) and pick the
   requested tag, or the newest non-prerelease.
2. **Match** — `MatchAssets` runs the variant's regexes over the release's asset
   names, most-preferred first, and returns the primary asset plus any extras.
3. **Download** — stream to a temp file with progress callbacks throttled to one
   per 512 KiB.
4. **Stage** — extract into `<final>.tmp`, never into the destination.
5. **Locate + record** — `findExe` walks the tree for the component's executable
   (release archives nest it differently per project and per build), then the
   manifest is written **inside the staging dir**.
6. **Commit** — `RemoveAll(final)` + `Rename(staging, final)`.
7. **Register** — `OnInstalled` points the backend registry row at the new exe
   (see `internal/server/backendsapi.go`), regenerates the config and hot-reloads.

## Gotchas / conventions

- **The install root is the exe's own directory, and `QM_BACKENDS_DIR` overrides
  it.** `defaultRoot()` matches the bundle-relative layout the Windows installer
  and every other runtime path uses, which is wrong in exactly one place: a
  container, where the exe sits on a read-only image layer and every install
  would vanish with the next `docker run`. `docker/app/Dockerfile` sets the env
  var to a directory under the mounted `/data` volume. Note the binary itself
  deliberately stays outside that volume: mounting one over it would shadow the
  newer binary on every image update.

- **An install never steals ★ from an existing row.** `registerManagedBackend`
  only marks the new row as its class default when no row of that class exists
  yet, so a hand-entered backend the user set up earlier keeps winning and the
  managed build sits unused. That is the right default (their choice stands) but
  it is invisible, so the catalog DTO carries
  `isDefault`/`defaultOwner`/`defaultImplicit` and the card shows "Installed, but
  not in use — X is the default" with a `POST /api/backends/default` action.
  **"In use" is a per-class fact, "active" is a per-component one, and conflating
  them is a real bug that shipped**: every component with an install has its own
  registry row pointing at one of its builds, so painting that build "in use"
  made three llama backends all claim it while only one launched. The build row
  says *selected* unless the component also wins its class, and only the winning
  card carries the primary "in use" chip. `activate` is a different axis
  entirely: it picks *which build of this component* the row points at, never ★.
- **`classDefault` must mirror `resolveBackend`, fallback included.** Auto-pick is
  "the ★ row of the class, **else the first row of the class**" — so with no ★
  anywhere one backend is still silently the winner. Reporting "no default is
  set" on every card of that class (what the first cut did) leaves the user
  unable to tell which binary runs; `defaultImplicit` marks a win that came from
  list order so the UI can say "runs because it was registered first" instead of
  calling it a default.
- **Custom repos come in through `Manager.Sources`, a hook — not an import.** The user's tracked
  repos live in the autogen sidecar, but this package depends on the standard
  library alone, so `internal/server` supplies them as a `func() []Component`
  that `Catalog`/`Find` merge *after* the built-ins. A source whose id collides
  with a built-in is dropped, so a user-controlled repo can never take over a
  built-in's install directory or registry row.
- **Patterns for tracked repos are DERIVED, never typed** (`derive.go`). The user
  picks a real asset out of a real release; version-ish tokens (build numbers,
  dates, shas, `rc1`) become wildcards and everything else stays literal, so
  next week's build of the same flavour still matches. Two properties matter more
  than the cleverness, because the result decides which binary gets executed:
  `DeriveUnique` verifies against the whole release and **tightens rightmost-first
  until exactly one asset matches** (this is what pins llama.cpp's side-by-side
  CUDA 12.4/13.3 builds — the same guarantee `Variant.PairKey` gives the static
  table, reached from an example instead of a hand-written capture); and when
  nothing can be made unambiguous it **falls back to the literal file name**, so a
  changed release surfaces as "re-pick this asset" rather than the wrong build on
  disk. `Variant.Exemplar` keeps the picked name so `ClosestAsset` can say what
  the nearest thing is when upstream renames its assets.
- **Nightly-only repos need `Component.AllowPrerelease`.** `pickRelease` skips
  prereleases for "latest", which resolves to *nothing* on a fork that only ever
  publishes nightlies (lemonade's llamacpp-rocm is exactly this). The server sets
  the flag automatically when a tracked repo's release history contains no stable
  release, rather than asking the user about GitHub semantics.
- **The catalog is the one maintenance point.** When upstream renames its release
  assets, the fix is a regex in `catalog.go` — and `TestBackends_MatchAssets`
  pins the current naming for every component, so a rename breaks the test rather
  than silently installing the wrong flavour.
- **The manifest is the only "installed" signal.** A directory without
  `.qm-install.json`, or one whose recorded exe has vanished, is not an install.
  Combined with staging + rename this means a failed or half-finished extract can
  never masquerade as a usable build.
- **Versioned side-by-side, nothing is ever overwritten.**
  `<root>/bin/<component>/<version>-<variant>` — an update can't brick a working
  setup, and a rollback is one activate away. Uninstall is the only removal.
  `sanitizeSeg` keeps a hostile tag inside the component directory.
- **Two hostile-input guards, both tested.** `validAssetURL` (https + a GitHub
  host allowlist, copied from `internal/update`) so a poisoned API response can't
  make us fetch and run an arbitrary binary; `safeJoin` so a crafted archive
  can't write outside the install directory. Both extractors skip devices and
  fifos deliberately.
- **Links must be kept, and are the other half of a path-escape trick.**
  llama.cpp's Linux bundles ship every library as a SONAME chain of symlinks
  (`libllama-common.so` → `.so.0` → `.so.0.3.0`) and the ELF headers reference
  the MIDDLE name, so an extractor that drops links leaves a bundle that cannot
  load: every spawn dies with `libllama-common.so.0: cannot open shared object
  file`, whatever is on `LD_LIBRARY_PATH`. `applyLinks` defers them to a second
  pass (an archive lists links before their target), rejects any target that
  resolves outside the install directory (a leading `/` counts as absolute even
  on Windows), and falls back to copying the target where symlinks need a
  privilege. A zip stores a symlink as a tiny entry whose *content* is the
  target path; written out verbatim it becomes a text file wearing a library's
  name, so it gets the same treatment.
- **Rate limits are real.** Unauthenticated GitHub API calls are 60/hour per IP,
  which is why release listings are cached for 10 minutes and only fetched when
  the user opens a version picker or hits Check — the catalog endpoint itself
  never calls GitHub, so the settings tab opens instantly and works offline.
  `GITHUB_TOKEN` is used when present.
- **`yt-dlp` is installed but not registered** (`Kind: ""`). It is a helper for
  the chat `media_transcript` tool, not a backend;
  `internal/server/youtube.go`'s `ytDlpPath` checks a managed install first, then
  PATH, then the bundle directory.
- **Extras are best-effort, but must be *paired*.** A missing cudart zip logs and
  continues rather than discarding an otherwise-good llama-server. When one
  release ships several builds of a variant (llama.cpp publishes CUDA 12.4 and
  13.3 side by side, each with its own cudart), `Variant.PairKey` captures the
  toolkit version from the chosen primary and `{v}` in the extra pattern is
  substituted with it. Matching extras by list order alone eventually ships the
  wrong runtime.
- **Asset naming is not uniform across projects.** llama.cpp moved its
  Linux/macOS archives to `.tar.gz` while Windows stayed `.zip`;
  stable-diffusion.cpp capitalises its platform segment (`Linux-Ubuntu`,
  `Darwin-macOS`), so every sd pattern is `(?i)`. Both are the kind of break that
  is invisible until an install fails, which is what `TestBackends_MatchAssets`
  (verbatim upstream asset lists) exists to catch.
- **A catalog entry is not always installable at all (`Manual`).** vLLM is a
  first-class backend in autogen (`kind: vllm`, its own emitter) but its releases
  attach *Python wheels*, not executables
  (`vllm-0.26.0+cu129-cp38-abi3-manylinux_2_28_x86_64.whl`) — there is no Windows
  wheel at all and the ROCm build is source-/container-only. Installing it means
  provisioning a Python environment and letting pip pull torch from PyPI, which
  is a different installer, not a different regex. It is catalogued with
  `Manual: true` + `Setup` so the UI can describe the engine instead of hiding a
  supported backend; `Install()` refuses, and the card shows the setup text with
  no install controls. Add a manual entry for any engine we can *drive* but not
  *download*.
- **An upstream archive is not always complete, and a broken one is silent.**
  stable-diffusion.cpp's Windows ROCm asset is built against AMD's ROCm *pip
  wheels* and packaged with `7z a ... .\build\bin\*` — no runtime is copied (the
  CUDA job in the same workflow does copy its cudart/cublas), so the zip holds
  three files and imports `amdhip64_7.dll`/`hipblas.dll` from nowhere. It runs
  only on the CI machine. A binary whose DLL graph is incomplete dies with
  STATUS_DLL_NOT_FOUND before `main`, so nothing reaches the process log and the
  failure surfaces as `upstream command exited prematurely` at generation time,
  pointing at the model instead of at the packaging. `Manager.Preflight` (wired
  to `peimports.Hint`) walks the import graph after the install commits and puts
  the missing library on `Job.Warning`; the same check runs per build in the
  catalog DTO, because a job scrolls away and the broken build does not. The
  install still succeeds — the bits are on disk and work the moment the runtime
  sits beside them.
- **The newest release is not always installable.** Real-ESRGAN's latest tag is
  source-only. `pickRelease` takes an `installable` predicate and, for "latest",
  skips releases with no matching asset for the requested variant/OS.

## Connections

- **Depends on:** the standard library only (`net/http`, `archive/zip`,
  `archive/tar`). Binary inspection is platform-specific, so it arrives through
  the `Preflight` hook rather than an import, the same way `Sources` keeps
  sidecar persistence out of here.
- **Called by:** `internal/server` — `Server.backends` is constructed in
  `server.New` with `GpuNames: s.gpuNames` and `OnInstalled:
  s.registerManagedBackend`; the HTTP surface is `internal/server/backendsapi.go`
  (`/api/backends/*`). `internal/server/youtube.go` also scans for a managed
  `yt-dlp`.
- **Writes into:** the autogen sidecar backend registry
  (`autogen.BackendEntry.Managed/Component/Version/Variant`), indirectly via that
  callback.
