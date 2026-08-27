<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# KV-cache disk save (survive eviction)

**KV-cache disk save** (dashboard Settings → KV Cache) persists a conversation's KV cache to disk so a long prompt isn't re-processed from scratch after the model is evicted.

Why: llama-server has a single slot - any new request can push out the resident conversation. Normally returning to that chat means re-prefilling the whole prompt, which for a 30k+ context can take minutes. With this on, Quartermaster snapshots the KV before eviction and restores it instead. Measured on a 27B model at 32k context: prefill dropped from 60.6s to 0.35s, with 32,032 of 32,057 tokens reused across a full process restart.

- **Turn it on**: two gates - the global **Enable** switch (plus Directory, min-tokens-to-save, max disk GB, max sessions) in Settings → KV Cache, AND a per-model **Save KV cache to disk** checkbox in the cogwheel editor. A model without the checkbox is left alone.
- **What counts as one conversation**: each conversation gets its own snapshot file. The playground sends a stable per-chat id, so a new chat is always a new file. Other clients (pi, Aider, anything using a plain OpenAI SDK) send no id, so the conversation is identified by a hash of its **first system + first user message**. Two sessions that open with the exact same text therefore share one file. That is safe - the restored prefix is validated, so the worst case is a slower request, never a wrong answer, and the shared opening is usually reused for free. The only real cost is that the two sessions overwrite each other's snapshot, so only one of them gets a full restore later.
- **Check it's working**: Observe → Context → KV Cache lists saved files and confirm-hit / confirm-miss counts - a *confirm* means the restored KV was actually reused on the next request, not just loaded.
- **Disk cost**: roughly 40 KB per token, so a 60k-token session is around 2.4 GB. The max-disk-GB and max-sessions caps evict the least recently used snapshots.

**Hybrid / recurrent models (Qwen3.5 / 3.6 / 3.8) work, with one limit.** These architectures keep a rolling state rather than a per-token history, so a saved state can be continued *forward* from exactly where it was saved, but never rewound to an earlier point. In practice that is fine - chats only ever grow - so save/restore gives the same speedup as on standard models. Two narrower paths are skipped automatically for them:

- **Preamble seeding** (pre-warming a brand-new chat with a shared system+tools prefix) is off, because that shortcut requires rewinding.
- **Restoring a snapshot larger than the incoming prompt** is skipped, since that also needs a rewind and would reuse nothing. This only comes up if a client edits or truncates its own history.

Standard transformer models get every path, including preamble seeding.
