# Vendored assets

## `titlegen-flan-t5-small-q8_0.gguf` (79 MiB)

The title model (see `../titlegen.go`): reasoning-box gists and chat titles.

**Not in this repo, and not in the binary.** It is downloaded once on first use
into `<dir(quartermaster-generate.yaml)>/titlegen/`, pinned by URL and SHA-256 in
`../titlegen_asset.go`. The Windows installer used to prefetch it, through a
`fetch-backend.ps1` run that no longer happens; the lazy path was always the real
one, and every non-Windows install already relied on it. When it cannot be
fetched, chat titles fall back to the chat model client-side — no error, just a
slightly worse title.

It used to be a `//go:embed`, which put 79 MiB into every clone, every release
archive and every self-update for a feature that degrades gracefully.

- Source: [`google/flan-t5-small`](https://huggingface.co/google/flan-t5-small) —
  T5-small (80M) with FLAN instruction tuning.
- Converted with llama.cpp `convert_hf_to_gguf.py` (f16), then `llama-quantize` to
  `Q8_0`.
- License: Apache-2.0 (see `../../../THIRD-PARTY-NOTICES.md`).
- Hosted as the `assets-v1` release asset on this repo, because what we need is
  this specific conversion + quantization rather than the upstream safetensors.

### Why not a title/summarization fine-tune

A headline fine-tune (e.g. `fabiochiu/t5-small-medium-title-generation`, tried
first at both small and base size) is trained on article→title and behaves
*extractively*: it emits the input's own opening clause. That reads acceptably on
a paragraph of reasoning prose, but a user's chat opener is an instruction, so its
opening clause is the request verb — titles came out as truncated copies
("How do I stop my llama-server from spilling into shared VRAM on windows when I
load"). FLAN's instruction tuning, driven by the few-shot prompt in
`titlegenShots`, produces an abstractive topic phrase instead
("How to stop llama-server from spilling into shared VRAM on Windows"), and it
covers both reasoning spans and chat openers with one model.

`flan-t5-base` is better still on chat openers but is 264 MiB — 3× the download.
Set `QM_TITLEGEN_MODEL` to a converted `flan-t5-base` gguf if you want it locally.

It runs on the CPU under `llama-completion` — llama.cpp's **server** has no
encoder-decoder path, so this model cannot be served as a normal backend model.
