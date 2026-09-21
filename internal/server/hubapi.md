# internal/server — the model hub API (fork)

The `/api/hub/*` surface over `internal/hub`, backing the UI's `/browse` page
(`ui-svelte/browse.md`). Route list in [`routes.md`](routes.md). Admin-gated.

## `hubapi.go`

Search, repo detail, and download start/poll/pause/resume/cancel, plus `POST /api/hub/clear`
(`handleAPIHubClear` → `Manager.ClearFinished`), which dismisses the terminal rows from the job
list — history only, no bytes, nothing running or paused touched. `hubPartialMaxAge` is the age
gate for both `hub.Manager.Restore(hubPartialMaxAge)` — which brings downloads that were in
flight when the process last died back as paused jobs with their progress read off the `.part`
files — and the `hub.SweepPartials` orphan sweep. Both run from `StartHubDownloads`, which
`main` calls once the autogen admin (and with it the models root) is attached; run from
`server.New` they would resolve an empty root and quietly do nothing.

It owns the two things the engine deliberately doesn't:

- **`hubModelsRoot`** — the `-models-dir` override, else `autogen.LoadBaseSettings().ModelsRoot`,
  **re-read every call** so a live reload can move it.
- **`hubToken`** — `HF_TOKEN` / `HUGGING_FACE_HUB_TOKEN` / `HUGGINGFACE_TOKEN`.

`sendHubError` maps a `*hub.AuthError` to **403 with the accept-the-license wording**, everything
else to 502 — the browser renders these bodies verbatim, so they are written to be read.

`Manager.OnComplete` is wired to `s.regenReload()`, so a finished download registers itself instead
of waiting on the `-watch-models` poll.

## `hubapi_estimate.go` — `GET /api/hub/estimate`

The browser's **real** pre-download sizing: how much *context* a candidate leaves room for, not
just whether its bytes fit. Range-fetches the file's GGUF header via `hub.FetchRange`, parses it
with `autogen.ReadGgufMetadataFrom`, and runs `autogen.EstimatePlan` with `Ctx: 0` (= the largest
window that fits `targetVramGB`). It lives here rather than in `internal/hub` because this is the
only package importing both.

Three things it must not get wrong:

- A **sharded** set is totalled from the hub's own listing over the candidate's `Group` — shard 1's
  own length prices a fifth of the weights.
- The size handed to the parser is the file's **full** length, never the fetched prefix, which
  would report a model that fits any budget.
- Every failure lands in `Err` with a **200, never an HTTP error** — the row still renders, just on
  the size-only verdict.

### Keeping the bytes down

A real gguf header is **6–8 MB** (the tokenizer vocab dominates it; measured: Qwen3-4B 6.0,
gemma-3-12b and Llama-3.1-8B 8.0), so a naive implementation streams tens of MB per browse row.
Two mechanisms, and both matter:

1. **`hubHeaderMeta` walks `hubHeadSteps` (8 → 24 → 64 MiB)**, but each step fetches only the bytes
   it ADDS via `hub.FetchRange(off, n)` — so guessing low costs a round trip, never a second copy,
   which is what makes an 8 MiB first step affordable where a re-fetch-from-zero retry would not
   be. A truncated header surfaces as an unexpected EOF inside the parser (the header carries no
   length of its own); any other parse error, or a short read, stops the walk.
2. **`hubModelMeta` parses one header per *model*, not per file.** Everything the header answers
   except file size is architecture, and quantizing doesn't change it, so `hubMetaFamily` cuts the
   quant tag out of the name (`Qwen3-8B-Q4_K_M.gguf` → `QWEN3-8B|GGUF`) and every quant shares one
   fetch, with `FileSizeGB` stamped on per caller from the hub's listing. What *follows* the tag is
   **kept** (`…-Q4_K_M-MTP` is a different model), and a name with no quant tag speaks only for
   itself — a repo holds unrelated models, and always holds the projector.

Since the picker sizes a repo's rows concurrently, the entry is a **single-flight job**
(`hubMetaJob`, waiters block on `done`) — otherwise all five rows miss together and pull the same
header five times. **Failures are not cached**, so one cancelled request can't answer for the repo.
30-min cache keyed repo+path+source, with the VRAM target folded in, since that is the only input
that moves the answer.

### One request per REPO, not per row

`path` may be **repeated**, and that is what the picker sends: the batch answers **NDJSON**, one
`hubEstimateResp` per line, flushed as each row resolves (`streamHubEstimates`, fan-out
`hubEstimateFanout` = 4, capped at `hubEstimateMaxPaths`). A single `path` still gets the plain
JSON object it always did.

The reason is the **browser**, not the server. A page gets six connections per origin over
HTTP/1.1, `/api/events` holds one for the life of the session, and each sizing row waits on a CDN
round trip of several MB — so the picker's old pool of five per-row requests left the page with no
socket at all, and **pressing Download did nothing until a header fetch finished**. Server-side
the batch is nearly free: the rows share one header fetch via `hubMetaJob` either way, so all the
fan-out does is keep two unrelated models in a repo from serializing. Rows come back in
**completion order**, which is why each one repeats its `repo`/`path` and the client matches on
those. A dead client is noticed at the feeder (`r.Context()`), not by writing into a closed socket.

## `audiocppcatalog.go` — `GET /api/hub/audiocpp`

The one backend whose weights cannot be found by browsing. audio.cpp publishes ~70 families into a
handful of shared GGUF repos, so a hub search answers with a few hundred loose file names and
nothing saying which family a name belongs to, which files are alternatives to each other, or that
an `f5_tts` gguf is useless without the `vocab.txt` beside it. Upstream's own answer is
`model_specs/*.json`, which every release ships next to the binary, and this serves it as
family -> packages -> the exact repo file set (`internal/audiocpp`). The UI folds these rows
into the ordinary TTS and Transcribe tabs of `/browse` (`ui-svelte/browse.md`).

Three deliberate choices:

- **Read from the INSTALL, not from an embedded copy.** The catalog is then exactly as new as the
  backend the user has, a backend update ships new families for free, and upstream's data has no
  second copy here to drift. `audioCppInstall` finds the exe through autogen's backend registry,
  preferring the ★ row over a derived per-build one.
- **A VIEW over the existing downloader.** A package's files go to `/api/hub/download` as an
  ordinary `hub.StartRequest`, so resume, pause, the journal, the manifest and the free-disk check
  are all unchanged code. What this endpoint adds is *which files to ask for*. `local` is judged
  per FILE off `hub.LocalFiles` (one walk per repo, not per package): a gguf present without its
  sidecar is not installed. `sizeBytes` is the whole SET, priced from one `Source.Detail` call
  per repo with the on-disk copy as the offline fallback; a set only partly priced reports 0,
  since a partial sum reads as a small download for a large one.
- **Servability comes from `autogen.AudioCppFamilySupport`, not from a second table.** A catalog
  that decided this for itself would advertise families the emitter then refuses. Unsupported
  families are listed with a `reason` rather than hidden, and the two failures read differently:
  a known family with no class here (music, stem separation, codecs) is a roadmap item, while an
  unknown one means the installed backend is newer than autogen's table.

## `revealfolder.go` — `POST /api/hub/reveal`

Opens a downloaded model's folder in the OS file manager (Explorer / `open` / `xdg-open`), backing
the Browse page's folder button and the Downloads menu's clickable paths.

Shelling out on the SERVER is only sane because the dashboard is already `adminChain`-gated: a
browser cannot open a local folder, and quartermaster is a local tool whose UI and models tree
share a box.

Two guards: `revealTarget` requires the path to resolve **inside** the models root and to already
exist. Containment compares the **resolved** paths (`filepath.EvalSymlinks` on both sides, via
`realPath`) so a symlink under the root pointing at `/etc` cannot pass a textual prefix check; a
file resolves to its parent dir. A **relative** path is relative to the models root, not the
process CWD — that is the form the in-app browser's breadcrumbs send. An empty/absent body means
the models root itself. The path is **one argv element**, never interpolated into a shell.

`canReveal(r)` gates the whole thing: shelling out only helps a caller on **this** box with a
**desktop session**. It is false for a non-loopback `RemoteAddr` (admin access can be widened past
loopback with `-admin-allow`/`-admin-open`) and false on Linux with no `xdg-open` on `PATH` — the
headless-container case from issue #66, where the old code spawned into the void or reported a
missing binary. Such a request gets a **409**, and the same flag rides along on
`GET /api/hub/sources` as `canReveal` so the UI knows before it offers the button.

## `filebrowser.go` — `GET /api/hub/files?path=`

The answer for everyone `canReveal` says no to: a **read-only** listing of one directory at or
under the models root, which the browser renders itself. Nothing here renames, moves, deletes or
serves file *content*; it reports names, sizes and mtimes, and reuses `revealTarget` as its one
containment guard.

The DTO carries `root`, `path`, `rel` (slash form, `""` at the root), `parent` (`""` **at** the
root, so the UI cannot offer an "up" the server would refuse) and `entries`. Entries are folders
first then case-insensitive by name (a models tree is repo dirs holding shards; the other order
buries the dirs), a symlink reports what it *points at*, and a directory beyond
`hubFilesMaxEntries` (4000) is cut with `truncated: true` rather than handed over as a 50 MB JSON
document. `listFolderEntries` is split out so that shape is testable without a `Server`.

`openInFileManager` **starts and never waits** — Explorer exits non-zero on successful opens and
`xdg-open` can outlive the handler, so only a failure to *spawn* is reported (that is the case
worth telling the user about); a goroutine reaps the child. It deliberately does **not** call
`hideConsole` — explorer/open/xdg-open are GUI launchers with no console to hide, and on Windows
the `SW_HIDE` in that `STARTUPINFO` is inherited by the window the shell opens for us, so the call
"succeeds" with nothing on screen.
