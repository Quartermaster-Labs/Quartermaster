# internal/server — the `-generate` config editor (fork)

The endpoints behind the dashboard's cogwheel, Settings modal and Backends tab. **Every handler
here 501s when `s.autogen == nil`** — they are the `-generate` surface. Route list in
[`routes.md`](routes.md); the gate itself is `AutogenAdmin`/`SetAutogenAdmin` (`configapi.go`).

| File | Role |
|---|---|
| `configapi.go` | Per-model override editor, named variants, display-name rename, cmd preview. Shared helpers live here: `resolveModelGguf`, `findSidecarOverride`, `regenAndReload`, `writeJSON`. |
| `configapi_dto.go` | Wire types (`variantDTO`, `overrideDTO`, `modelConfigResp`) + conversions to/from `autogen.Override`/`VariantSpec` (`applyOverrideDTO`, `applyVariantPatch`). **Sparse by design: a zero field means "keep auto-computing".** |
| `configapi_settings.go` | Global settings editor: the GPU-memory card (VRAM target / headroom / max-RAM + `ttlSec` idle eviction, `0` = never), the backend registry, slot-cache knobs, and the native folder/file pickers. |
| `configapi_estimate.go` | Load-plan estimate: rebuilds an `autogen.EstimateInput` from a *rendered launch command* (`estimateInputFromCmd`, `forcedOffloadFromCmd`) so the UI's VRAM/KV breakdown reflects what is actually configured. |
| `configapi_adhoc.go` | Ad-hoc launch commands: `renderAdhocCmd` (a sparse patch over the effective override → a fully sized cmd with `${PORT}`) plus the load/unload endpoints that inject one into the live router **without persisting anything**. Also used by `ensureCtxVariant`. |
| `configapi_apikeys.go` | The local admin API-key manager over the sidecar's managed keys (`autogen.{Load,Upsert,Delete}SidecarAPIKey`). |
| `backendsapi.go` | Managed backend installs — the `/api/backends/*` surface over `internal/backends` (catalog, upstream releases, install job start + poll, activate/rollback, uninstall) plus the registry write-back `registerManagedBackend`. |
| `backendsources.go` | Tracked custom backend repos — `/api/backends/sources*` (list, one release's assets, save, delete) plus `/api/backends/{component}/resolve`. Converts `autogen.BackendSource` rows into `backends.Component`s via the `Manager.Sources` hook, and **derives every asset pattern server-side** from the asset the user picked. |
| `pickfolder_{windows,linux,other}.go` | Native folder-picker dialog (`pickFolder()` — WinForms / zenity / unsupported) backing `POST /api/pick-folder` and `POST /api/settings/root/pick`. |
| `pickfile_spec.go` | `pickSpecs`, the **server-side whitelist** of open-file dialog kinds (`backend`, `template`) with their Windows/zenity filter strings. |

## Gotchas

- **A save is a regen + hot reload.** A successful edit upserts the sidecar override/settings, calls
  `autogen.EnsureConfig`, then hot-reloads (the SIGHUP path). That is slow — it re-reads gguf
  metadata — and acceptable only because it is a settings save. The reload itself is in-place; see
  `Server.ApplyConfig` in [`http-core.md`](http-core.md).
- **A spec must never be built from request data.** The platform `pickFile` implementations
  interpolate `pickSpecs` entries into a shell/PowerShell command line, so `/api/pick-file` rejects
  any kind not in that map.
- **Managed and manual backends share ONE registry.** `registerManagedBackend` upserts a normal
  `autogen.BackendEntry` row (`id: managed-<component>`) flagged `Managed` with
  `Component`/`Version`/`Variant`, so per-model pinning, the ★ class default and `deriveBackendExes`
  are untouched. Two consequences:
  1. The row's **id is kept** across updates, so a per-model `Override.Backend` survives a version
     bump.
  2. `PUT /api/settings/backends` (the manual editor, which sends the whole list) **restores managed
     provenance from the stored row by id** rather than trusting the client — a manual save can't
     strip `Managed` or repoint the path away from the active build.

  Managed rows render read-only in the manual editor, and a managed row becomes the ★ class default
  only when its class is empty. `backendsapi.go` sits **alongside** the hand-entered rows, never
  replacing them: several backends have no installable upstream release.
- **`GET /api/backends/catalog` is local-only and never calls GitHub**, so the Backends tab opens
  offline; releases are fetched lazily per component.
- **A tracked repo is just another catalog row.** `s.trackedSources` (`backendsources.go`) is wired
  into `Manager.Sources` in `server.New`, so `m.Catalog()`/`m.Find()` return the user's repos merged
  after the built-ins and every downstream path — install, activate, ★ default, rollback, the
  registry write-back — works on them unchanged. Consequences worth knowing:
  1. **The catalog handler must call `m.Catalog()`, not the package-level `backends.Catalog()`.**
     The package function only knows the static table, so a custom card silently disappears.
     `registerManagedBackend` and the releases handler have the same requirement (`m.Find`).
  2. **A tracked id can never shadow a built-in.** New ids are minted as `custom-<repo-slug>` and
     checked against `backends.Find`, and `Manager.Catalog` drops a colliding source as a second
     line of defence — otherwise a user-controlled repo would take over a built-in's install
     directory *and* its registry row.
  3. **The repo string reaches an api.github.com URL path**, so `backends.ValidateRepo`/`ParseRepo`
     bound it to `owner/name` before any request is built. Never skip that on a new entry point.
- **The user never writes an asset regex, and no endpoint accepts one.** `POST /api/backends/sources`
  takes the asset *names* the user ticked plus the tag they came from, and derives the patterns with
  `backends.DeriveUnique` against that release's full asset list — which is also what pins
  llama.cpp-style side-by-side CUDA builds instead of matching both. An asset the client claims but
  the release doesn't have is a 400; an unresolvable derivation falls back to the literal file name,
  so a future release fails closed ("re-pick") rather than installing the wrong binary.
- **`GET /api/backends/{component}/resolve` is what the UI shows instead of the pattern.** It reports
  the file an install would download right now, and on a miss the closest asset by name similarity —
  a rename upstream then reads as "closest is X, re-pick" rather than a bare failure.
- **Stopping tracking is refused while builds are installed** (409). The registry may point at one of
  them, and untracking must not orphan an install directory or silently swap out a running backend's
  executable.
