# internal/server — the model hub API (fork)

The `/api/hub/*` surface over `internal/hub`, backing the UI's `/browse` page
(`ui-svelte/browse.md`). Route list in [`routes.md`](routes.md). Admin-gated.

## `hubapi.go`

Search, repo detail, and download start/poll/pause/resume/cancel. `hubPartialMaxAge` is the age
gate for the startup `hub.SweepPartials` orphan sweep, kicked off in `server.go` beside the
`OnComplete` wiring.

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

## `revealfolder.go` — `POST /api/hub/reveal`

Opens a downloaded model's folder in the OS file manager (Explorer / `open` / `xdg-open`), backing
the Browse page's folder button and the Downloads menu's clickable paths.

Shelling out on the SERVER is only sane because the dashboard is already `adminChain`-gated: a
browser cannot open a local folder, and quartermaster is a local tool whose UI and models tree
share a box.

Two guards: `revealTarget` requires the path to resolve **inside** the models root (`filepath.Rel`
containment; a file resolves to its parent dir) and to already exist. An empty/absent body means
the models root itself. The path is **one argv element**, never interpolated into a shell.

`openInFileManager` **starts and never waits** — Explorer exits non-zero on successful opens and
`xdg-open` can outlive the handler, so only a failure to *spawn* is reported (that is the case
worth telling the user about); a goroutine reaps the child. It deliberately does **not** call
`hideConsole` — explorer/open/xdg-open are GUI launchers with no console to hide, and on Windows
the `SW_HIDE` in that `STARTUPINFO` is inherited by the window the shell opens for us, so the call
"succeeds" with nothing on screen.
