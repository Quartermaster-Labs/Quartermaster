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
