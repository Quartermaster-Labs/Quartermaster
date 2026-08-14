<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchCanon, type CanonStats } from "../stores/api";
  import { observeTab } from "../stores/observe";

  let stats = $state<CanonStats | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  async function tick() {
    const s = await fetchCanon();
    if (s) stats = s;
  }

  // Panel stays mounted across tab switches; only poll while Context is active.
  $effect(() => {
    if ($observeTab !== "context") return;
    void tick();
    timer = setInterval(() => void tick(), 2000);
    return () => {
      if (timer) clearInterval(timer);
      timer = null;
    };
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
  });

  function fmtBytes(n?: number): string {
    if (!n) return "0 B";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`;
  }

  function fmtTime(t: string): string {
    const d = new Date(t);
    return isNaN(d.getTime()) ? t : d.toLocaleTimeString();
  }

  function fmtNum(n?: number): string {
    return (n ?? 0).toLocaleString();
  }

  const counters = $derived(stats?.counters);
  const rewritePct = $derived(
    counters && counters.seen > 0 ? Math.round((100 * counters.rewritten) / counters.seen) : 0
  );
</script>

<div class="h-full overflow-auto p-1 pretty-scroll">
  {#if !stats}
    <div class="text-txtsecondary text-sm">Loading…</div>
  {:else}
    <div class="card p-3 text-xs text-txtsecondary mb-3">
      Volatile spans (e.g. sub-day timestamps) are stripped from the system prompt of every
      chat request so its stable prefix stays byte-identical turn-to-turn - letting llama-server
      reuse the KV cache instead of reprefilling. Non-lossy: the model still sees date granularity.
    </div>

    <!-- Summary cards -->
    <div class="grid grid-cols-2 md:grid-cols-3 gap-3 mb-3">
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Requests canonicalized</div>
        <div class="text-2xl font-mono text-green-500">{fmtNum(counters?.rewritten)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">{rewritePct}% of {fmtNum(counters?.seen)} chat requests</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Bytes trimmed</div>
        <div class="text-2xl font-mono">{fmtBytes(counters?.bytesRemoved)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">total removed from prompts</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Chat requests seen</div>
        <div class="text-2xl font-mono">{fmtNum(counters?.seen)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">inspected for volatile spans</div>
      </div>
    </div>

    <!-- Recent rewrites -->
    <div class="card p-3 min-h-0">
      <div class="text-sm font-semibold mb-2">Recent rewrites</div>
      {#if !stats.events?.length}
        <div class="text-xs text-txtsecondary">No rewrites yet - no client sent a volatile prefix.</div>
      {:else}
        <div class="overflow-auto max-h-[28rem] font-mono text-xs pretty-scroll">
          {#each stats.events as e (e.time + e.model + e.rule)}
            <div class="flex items-center gap-2 py-0.5 border-t border-border">
              <span class="text-txtsecondary w-20 shrink-0">{fmtTime(e.time)}</span>
              <span class="text-sky-500 w-20 shrink-0">{e.rule}</span>
              <span class="truncate">{e.model}</span>
              {#if e.bytes}<span class="text-txtsecondary">· −{fmtBytes(e.bytes)}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>
