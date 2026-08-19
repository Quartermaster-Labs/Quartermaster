#!/usr/bin/env python3
"""kvcache_probe.py — prove whether quartermaster's on-disk slot KV cache is
actually REUSED (not just written) and measure how much faster it makes prefill.

Ground truth is the upstream-reported cached_tokens, surfaced two ways:
  1. /api/kvcache counters + event ring (the server's own confirm/confirm-miss),
  2. per-request usage.prompt_tokens_details.cached_tokens.

Modes (arg 1):
  switch  (default) warm model, no process reload — isolates disk save->restore:
          big X(idA) -> tiny Y(idB, evicts+saves X) -> big X(idA, restores X).
          Speedup = cold-X prefill vs restored-X prefill.
  swap    cross-process test (the decisive one for hybrid Qwen3.6):
          big X -> UNLOAD model (saveOnEvict) -> big X (cold load -> restoreOnLoad).
  pi      realistic path: run `pi -p` twice, diff kvcache events (preamble path;
          on a hybrid model expect 'recurrent-skip-seed', i.e. no seed reuse).

Env: QM_BASE (http://localhost:1250), QM_KEY, QM_MODEL, QM_PROMPT_TOKENS (default
32000 so it exceeds the default minSaveTokens=30000 without reconfig; lower BOTH
minSaveTokens and this for faster runs).

ponytail: a throwaway measurement harness, not product code — no retries, no
framework. It reads live server state; if the server changes shape, fix inline.
"""
import json
import os
import subprocess
import sys
import time
import urllib.request

BASE = os.environ.get("QM_BASE", "http://localhost:1250").rstrip("/")
KEY = os.environ.get("QM_KEY", "qm-7f9ead612acb74ed4e6d23f0c758d129dbad9c434b4f17bb")
MODEL = os.environ.get("QM_MODEL", "qwen3.6-35b-a3b-ud-q4_k_xl-64k")
PROMPT_TOKENS = int(os.environ.get("QM_PROMPT_TOKENS", "32000"))


def _req(method, path, body=None, key=False, timeout=600):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    if key:
        r.add_header("Authorization", "Bearer " + KEY)
    with urllib.request.urlopen(r, timeout=timeout) as resp:
        raw = resp.read()
    return raw


def kvcache():
    return json.loads(_req("GET", "/api/kvcache"))


def make_prompt(n_tokens):
    # ~1.35 chars/token of English-ish filler; deterministic, uniquely numbered so
    # two distinct conversations don't accidentally share a prefix.
    line = "The quartermaster inventories every crate, ledger line {}, and sealed manifest. "
    out, i = [], 0
    approx_chars = int(n_tokens * 4)  # conservative; real token count printed below
    total = 0
    while total < approx_chars:
        s = line.format(i)
        out.append(s)
        total += len(s)
        i += 1
    return "".join(out)


def chat(system, user, conv_id, max_tokens=1):
    body = {
        "model": MODEL,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "max_tokens": max_tokens,
        "stream": False,
        "cache_prompt": True,
    }
    hdr_id = conv_id
    url = BASE + "/v1/chat/completions"
    data = json.dumps(body).encode()
    r = urllib.request.Request(url, data=data, method="POST")
    r.add_header("Content-Type", "application/json")
    r.add_header("Authorization", "Bearer " + KEY)
    if hdr_id:
        r.add_header("X-Conversation-Id", hdr_id)
    t0 = time.time()
    with urllib.request.urlopen(r, timeout=600) as resp:
        raw = resp.read()
    wall = time.time() - t0
    j = json.loads(raw)
    usage = j.get("usage", {}) or {}
    cached = 0
    det = usage.get("prompt_tokens_details") or {}
    if isinstance(det, dict):
        cached = det.get("cached_tokens", 0) or 0
    timings = j.get("timings") or {}
    prompt_ms = timings.get("prompt_ms")  # llama-server may include this
    return {
        "wall": wall,
        "prompt_tokens": usage.get("prompt_tokens"),
        "cached_tokens": cached,
        "prompt_ms": prompt_ms,
    }


def unload():
    try:
        _req("POST", "/api/models/unload/" + MODEL, body={}, timeout=60)
    except Exception:
        try:
            _req("GET", "/unload", timeout=60)
        except Exception as e:
            print("  (unload failed:", e, ")")
    # give the process time to actually die
    for _ in range(30):
        try:
            running = _req("GET", "/running", timeout=5).decode(errors="ignore")
        except Exception:
            running = ""
        if MODEL not in running:
            return
        time.sleep(1)


def counters_diff(a, b):
    ca, cb = a["counters"], b["counters"]
    return {k: cb[k] - ca.get(k, 0) for k in cb}


def new_events(before, after):
    nb = len(before.get("events", []))
    na = len(after.get("events", []))
    # events newest-first; the first (na-nb) are new
    return after.get("events", [])[: max(0, na - nb)]


def show_req(tag, r):
    ms = f"{r['prompt_ms']:.0f}ms" if r.get("prompt_ms") else "n/a"
    print(
        f"  {tag:14s} wall={r['wall']:6.2f}s  prompt_tok={r['prompt_tokens']}"
        f"  cached_tok={r['cached_tokens']}  llama_prompt_ms={ms}"
    )


def preflight():
    kv = kvcache()
    if not kv.get("enabled"):
        print("SLOT CACHE DISABLED. Enable in the generate control file:")
        print("  settings:\n    slotCache:\n      enable: true\n      minSaveTokens: 4000")
        print("then regen+restart. Aborting.")
        sys.exit(1)
    return kv


def mode_switch(sysprompt):
    print(f"[switch] warm save->restore, prompt ~{PROMPT_TOKENS} tok\n")
    big_x = make_prompt(PROMPT_TOKENS)
    tiny_y = "Say OK."
    kv0 = kvcache()

    r_cold = chat(sysprompt, big_x, "probe-X")
    show_req("cold X", r_cold)
    chat(sysprompt, tiny_y, "probe-Y")  # evicts+saves X (if >= minSaveTokens)
    print("  (sent tiny Y to evict/save X)")
    r_warm = chat(sysprompt, big_x, "probe-X")  # should restore X
    show_req("restored X", r_warm)

    kv1 = kvcache()
    print("\n  counter delta:", counters_diff(kv0, kv1))
    print("  new events (newest first):")
    for e in new_events(kv0, kv1):
        print("   ", e.get("op"), e.get("key", ""), e.get("detail", ""), e.get("tokens", ""))

    if r_cold["prompt_ms"] and r_warm["prompt_ms"]:
        sp = r_cold["prompt_ms"] / max(1, r_warm["prompt_ms"])
        print(f"\n  PREFILL speedup (llama prompt_ms): {sp:.1f}x")
    else:
        sp = r_cold["wall"] / max(0.01, r_warm["wall"])
        print(f"\n  WALL speedup (prompt_ms unavailable, incl fixed overhead): {sp:.1f}x")
    verdict(r_warm, kv0, kv1)


def mode_swap(sysprompt):
    print(f"[swap] cross-process restore (decisive for hybrid), prompt ~{PROMPT_TOKENS} tok\n")
    big_x = make_prompt(PROMPT_TOKENS)
    kv0 = kvcache()

    r_cold = chat(sysprompt, big_x, "probe-X")
    show_req("cold X", r_cold)
    print("  unloading model (saveOnEvict)...")
    unload()
    r_restore = chat(sysprompt, big_x, "probe-X")  # cold load -> restoreOnLoad
    show_req("reload X", r_restore)

    kv1 = kvcache()
    print("\n  counter delta:", counters_diff(kv0, kv1))
    print("  new events (newest first):")
    for e in new_events(kv0, kv1):
        print("   ", e.get("op"), e.get("key", ""), e.get("detail", ""), e.get("tokens", ""))
    verdict(r_restore, kv0, kv1)


def mode_pi(sysprompt):
    print("[pi] realistic preamble path via `pi -p` x2\n")
    prompt = "In one sentence, what is a quartermaster?"
    kv0 = kvcache()
    for i in (1, 2):
        t0 = time.time()
        subprocess.run(
            ["pi", "-p", prompt],
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=600,
        )
        print(f"  pi run {i}: {time.time()-t0:.2f}s")
    kv1 = kvcache()
    print("\n  counter delta:", counters_diff(kv0, kv1))
    print("  new events (newest first):")
    for e in new_events(kv0, kv1):
        print("   ", e.get("op"), e.get("key", ""), e.get("detail", ""))
    print("\n  NOTE: on a hybrid/recurrent model, expect 'recurrent-skip-seed' —")
    print("  preamble seeding is intentionally disabled; disk cache does nothing here.")


def verdict(restored_req, kv0, kv1):
    d = counters_diff(kv0, kv1)
    print("\n  === VERDICT ===")
    if d.get("saves", 0) < 1:
        print("  NO SAVE fired — prompt KV < minSaveTokens. Raise QM_PROMPT_TOKENS")
        print("  or lower slotCache.minSaveTokens, then rerun.")
        return
    reused = restored_req["cached_tokens"] > 0 or d.get("confirmedReuses", 0) > 0
    if reused:
        print(f"  REUSE CONFIRMED. cached_tokens={restored_req['cached_tokens']},"
              f" confirmedReuses+={d.get('confirmedReuses',0)},"
              f" cachedTokensSeen+={d.get('cachedTokensSeen',0)}")
    else:
        print("  RESTORE HAPPENED BUT 0 REUSE (confirm-miss). The KV file loaded but")
        print("  the upstream reprocessed the prompt — the known hybrid GatedDeltaNet")
        print("  limitation (upstream llama.cpp #21831). Disk restore buys nothing here.")


def chat_msgs(messages, conv_id, max_tokens=8):
    """Send an explicit message list (so a turn can be APPENDED to verbatim) and
    return the timings plus the assistant message exactly as the server rendered
    it — replaying that object is what a real client does, and any divergence in
    it would break the token prefix we are trying to reuse."""
    body = {
        "model": MODEL,
        "messages": messages,
        "max_tokens": max_tokens,
        "stream": False,
        "cache_prompt": True,
    }
    r = urllib.request.Request(
        BASE + "/v1/chat/completions", data=json.dumps(body).encode(), method="POST"
    )
    r.add_header("Content-Type", "application/json")
    r.add_header("Authorization", "Bearer " + KEY)
    if conv_id:
        r.add_header("X-Conversation-Id", conv_id)
    t0 = time.time()
    with urllib.request.urlopen(r, timeout=900) as resp:
        raw = resp.read()
    wall = time.time() - t0
    j = json.loads(raw)
    usage = j.get("usage", {}) or {}
    det = usage.get("prompt_tokens_details") or {}
    msg = (j.get("choices") or [{}])[0].get("message") or {}
    return {
        "wall": wall,
        "prompt_tokens": usage.get("prompt_tokens"),
        "cached_tokens": (det.get("cached_tokens", 0) or 0) if isinstance(det, dict) else 0,
        "prompt_ms": (j.get("timings") or {}).get("prompt_ms"),
        "message": msg,
    }


def mode_append(sysprompt):
    """The realistic question: can a RESTORED recurrent state be continued FORWARD?

    swap mode resends an identical prompt, which forces llama.cpp to back up one
    token to produce logits — impossible on a rolling state, so it says nothing
    about real use. Here each turn APPENDS (the pattern pi and every chat client
    use), and a warm append runs first as the control: if the warm one reuses and
    the post-restore one does not, the restore is what upstream rejects.
    """
    print(f"[append] forward continuation, warm control vs post-restore, ~{PROMPT_TOKENS} tok\n")
    conv = "probe-append"
    msgs = [
        {"role": "system", "content": sysprompt},
        {"role": "user", "content": make_prompt(PROMPT_TOKENS)},
    ]
    kv0 = kvcache()

    r1 = chat_msgs(msgs, conv)
    show_req("turn1 cold", r1)

    # Append the assistant reply verbatim + a new user turn: a pure forward
    # extension of the exact token sequence the slot already holds.
    msgs.append(r1["message"])
    msgs.append({"role": "user", "content": "In one short sentence: what did I send you?"})

    r2 = chat_msgs(msgs, conv)
    show_req("turn2 WARM", r2)
    print(f"    ^ control: forward continuation with the model still loaded")

    msgs.append(r2["message"])
    msgs.append({"role": "user", "content": "And in one more short sentence: why?"})

    print("  unloading model (saveOnEvict)...")
    unload()

    r3 = chat_msgs(msgs, conv)
    show_req("turn3 RESTORED", r3)
    print(f"    ^ the real question: same append, but after a save/reload cycle")

    kv1 = kvcache()
    print("\n  counter delta:", counters_diff(kv0, kv1))
    print("  new events (newest first):")
    for e in new_events(kv0, kv1):
        print("   ", e.get("op"), e.get("key", ""), e.get("detail", ""), e.get("tokens", ""))

    print("\n  === VERDICT ===")
    warm_ok = r2["cached_tokens"] > 0
    cold_ok = r3["cached_tokens"] > 0
    print(f"  warm append   cached_tokens={r2['cached_tokens']:>6} of {r2['prompt_tokens']}")
    print(f"  after restore cached_tokens={r3['cached_tokens']:>6} of {r3['prompt_tokens']}")
    if not warm_ok:
        print("  WARM APPEND ALREADY REUSES NOTHING - this model does not even continue")
        print("  forward in-process, so the restore path is not what is failing.")
    elif cold_ok:
        print("  RESTORED STATE CONTINUES FORWARD. Save/reload is viable on this arch:")
        if r1["prompt_ms"] and r3["prompt_ms"]:
            print(f"  prefill {r1['prompt_ms']:.0f}ms cold vs {r3['prompt_ms']:.0f}ms restored.")
    else:
        print("  WARM REUSES, RESTORED DOES NOT. llama.cpp rejects a restored recurrent")
        print("  state for continuation even though the live one continues fine - the")
        print("  restore itself is the blocker, not the rewind.")


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "switch"
    preflight()
    sysprompt = "You are the quartermaster. Answer tersely."
    if mode == "switch":
        mode_switch(sysprompt)
    elif mode == "swap":
        mode_swap(sysprompt)
    elif mode == "append":
        mode_append(sysprompt)
    elif mode == "pi":
        mode_pi(sysprompt)
    else:
        print("unknown mode:", mode, "(use: switch | swap | append | pi)")
        sys.exit(2)


if __name__ == "__main__":
    main()
