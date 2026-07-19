# Server-owned turn runner (playground) — design

## Goal

A playground chat turn must run to completion **server-side**, decoupled from the
browser. Ask a question, close the tab, reopen 10 min later → the answer (incl.
tool rounds, reasoning-budget finalize, compaction, title) is there. Today the
whole turn loop runs in the browser (`ui-svelte/.../ChatInterface.svelte`); a tab
close kills it. This moves the loop into Go.

## Ownership model (load-bearing) — server-owns-chats, NO sidecar

- **`chats/<user>.json` is the single source of truth.** The server writes the
  in-flight assistant message straight into the target session as it streams
  (periodic flush + final). No sidecar, no duplicated conversation store.
- **Merge-guard (`guardedChatsPut`).** While a turn generates for chat C, a
  whole-array client PUT can't revert C's in-flight message: on PUT the server
  swaps the client's copy of session C for its own on-disk authoritative copy;
  every other session takes the client's version. Sessions round-trip as opaque
  `map[string]any`, so unknown client fields (searches, citations, instructions)
  are preserved.
- **Live view = in-memory snapshot+tail.** SSE sends the accumulated
  content/reasoning once (Replace=true) then live deltas. Nothing persisted for
  replay — a finished turn already lives in `chats.json`, so a reopened tab just
  loads it. `GET /api/chats/turn/state` reports which chat (if any) is generating
  so the tab knows to resubscribe.
- **Client pre-persists the session.** Before `POST /api/chats/turn`, the client
  saves the session with the user message (+ an empty trailing assistant bubble)
  so the server has a session to write into. Server appends the ANSWER, never the
  whole conversation.
- **Client sends the assembled request.** The client keeps building the system
  prompt / tool defs / params / budget (`systemPrompt.ts`, tool-def consts stay)
  and POSTs the full thing. The server does NOT rebuild the prompt — it only runs
  the loop + dispatches tools. Only the wiki corpus is ported to Go
  (`wikidata.go`, Phase 2) so headless `wiki_search` works.

Crash caveat: a server crash mid-turn loses the un-flushed tail (bounded to
`turnFlushInterval`, 2 s). Same exposure as any design.

## Endpoints

- `POST   /api/chats/turn`             — start a turn. Body: `{chatId, model,
  messages, tools, temperature, max_tokens, …phase 2+: reasoningBudget, webSearch,
  wiki, searxngUrl, summary, compactAt, keepRecent}`. 409 if a turn is already
  running for this user (single in-flight, mirrors client `genId`). Returns
  immediately with `{chatId}`.
- `GET    /api/chats/turn/stream?chatId=` — SSE. Snapshot (Replace=true) then live
  tail until `done`/`error`. 204 if that chat isn't generating.
- `GET    /api/chats/turn/state`       — `{chatId, running}` for the generating
  chat, or `{}`. Reopen reconciliation.
- `DELETE /api/chats/turn?chatId=`     — Stop: cancel the goroutine; the partial
  answer stays in `chats.json`.
- `POST   /api/chats/turn/approve`     — accept/deny a pending qm-tools
  `quartermaster_configure` change (the before→after diff card). The turn parks on
  a `pendingApproval` until answered; a 5-min timeout counts as deny. Accept
  applies via the config hot-reload path (no eviction).

## SSE event protocol

One JSON object per SSE `data:` line, `{seq, kind, …}`:
`reasoning`(delta) · `content`(delta) · `search`(query,kind,results,sources,
citations,at,reasoningAt,duringReasoning) · `round` · `budget-finalize` ·
`compaction`(summary,compactedCount) · `title`(title) · `done`(genTimeMs) ·
`error`(message). These reconstruct exactly what the client loop used to compute
locally, so the thin viewer just paints them.

## Runner internals (Go)

One goroutine per active turn. Self-calls quartermaster's own inference loopback
(`selfBase + /v1/chat/completions`, streamed) so template/canon/routing/slotcache
all apply. The loopback call carries `Authorization: Bearer <key>` when API keys
are configured (`pickSelfKey` — prefers an unscoped key, else one scoped to the
model), since `/v1` is gated; empty when keys are off. Mirrors the client loop:
round loop → tool dispatch. The reasoning budget is a SOFT cap enforced at round
boundaries by the runner (thinking off for later rounds once cumulative thinking
passes the budget), not llama.cpp's native `reasoning_budget`. Live
view is an in-memory
snapshot+tail (no sidecar); the growing answer + searches + citations are flushed
into `chats.json` every `turnFlushInterval` and on terminal state, so a
crash/refresh mid-turn keeps the answer-so-far.

**Compaction + title-gen stay CLIENT-side** (not in the runner): they run *after*
the answer, so the thin viewer does them on completion/reconnect
(`maybeCompact`/`maybeTitle`). A closed tab just catches up on the next turn —
nothing mid-answer is lost, and the server surface stays small.

## Phases

1. **Backbone** — runner + SSE + server-owns-chats persistence, plain single-round
   turns.
2. **Tool loop** — web (SearXNG) + wiki (`turnstools.go`, corpus embedded from the
   shared `wiki_articles.json`), citation numbering, per-turn caps, throttle/cache.
3. **Reasoning budget** — SOFT, round-boundary. `runLoop` tracks cumulative
   thinking (~4 bytes/token) and, once it passes the budget, sends
   `enable_thinking:false` for subsequent rounds so the model must answer. No native
   `reasoning_budget` — that hard-closes `</think>` mid-generation, which on a
   tool-using model mid-search derails it into dumping its in-thinking search stream
   as the answer (fabricated result blocks, no real tool call, no reply). The soft
   cut lets every round finish its thought + any in-flight tool call; a lone
   overthinking round is bounded by `max_tokens`. Also replaced the even-older
   max_tokens-cap + prefill-continue finalize dance (fragile on hybrid exact-prefix
   KV — reprocessed the whole context and re-thought).
4. **Compaction + title-gen** — NOT server-side (deliberate, see Runner internals):
   they stay client-side, run by the viewer after the answer / on reconnect.
5. **Client thin-viewer** — `ChatInterface.svelte` pre-persists the session, POSTs
   `/api/chats/turn`, views the SSE stream, syncs the authoritative copy, then runs
   compaction/title; `attachIfGenerating` reconnects a running turn on (re)mount;
   Stop → `DELETE /api/chats/turn`.

## Status

- [x] Phase 1  - [x] Phase 2  - [x] Phase 3  - [x] Phase 4 (client-side by design)  - [x] Phase 5
- Builds clean (`go build ./...`, `go vet`, `svelte-check`). NOT yet run live /
  `make test-*` (user runs make).
