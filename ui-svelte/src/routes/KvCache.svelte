<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { fetchKvCache, type KvCacheStats } from "../stores/api";

  let stats = $state<KvCacheStats | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  async function tick() {
    const s = await fetchKvCache();
    if (s) stats = s;
  }

  onMount(() => {
    void tick();
    timer = setInterval(() => void tick(), 2000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
  });

  function fmtBytes(n?: number): string {
    if (!n) return "0 B";
    const u = ["B", "KB", "MB", "GB", "TB"];
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

  // op -> tailwind text color + label, for the event log.
  const opStyle: Record<string, { cls: string; label: string }> = {
    confirm: { cls: "text-green-500", label: "reuse" },
    "confirm-miss": { cls: "text-red-500", label: "no reuse" },
    "restore-hit": { cls: "text-green-400", label: "hit" },
    "restore-seed": { cls: "text-sky-500", label: "seed" },
    "seed-pending": { cls: "text-sky-400", label: "seed?" },
    "preamble-hit": { cls: "text-green-400", label: "pre hit" },
    "preamble-mint": { cls: "text-purple-400", label: "pre mint" },
    save: { cls: "text-amber-500", label: "save" },
    miss: { cls: "text-txtsecondary", label: "miss" },
    error: { cls: "text-red-500", label: "error" },
  };

  const counters = $derived(stats?.counters);
  // Confirmed reuse is the honest metric: the request after a restore actually
  // reported cached_tokens > 0 from the upstream.
  const confirmTotal = $derived(
    (counters?.confirmedReuses ?? 0) + (counters?.confirmedMisses ?? 0)
  );
  const confirmPct = $derived(
    confirmTotal > 0 ? Math.round((100 * (counters?.confirmedReuses ?? 0)) / confirmTotal) : 0
  );
  function fmtNum(n?: number): string {
    return (n ?? 0).toLocaleString();
  }
</script>

<div class="h-full overflow-auto p-1">
  {#if !stats}
    <div class="text-txtsecondary text-sm">Loading…</div>
  {:else if !stats.enabled}
    <div class="card p-4 text-sm text-txtsecondary">
      Slot KV cache is <span class="text-txtmain font-semibold">disabled</span>. Enable it in the
      dashboard GPU-memory settings (and give models <code>--slot-save-path</code>) to persist and
      reuse conversation KV across model swaps.
    </div>
  {:else}
    <!-- Summary cards -->
    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-3 mb-3">
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Tokens reused (confirmed)</div>
        <div class="text-2xl font-mono text-green-500">{fmtNum(counters?.cachedTokensSeen)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          cached_tokens from upstream — actual prefill skipped
        </div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Restore success</div>
        <div class="text-2xl font-mono">{confirmPct}%</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          {counters?.confirmedReuses ?? 0} reused / {counters?.confirmedMisses ?? 0} no-reuse
        </div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Restores · hits / seeds</div>
        <div class="text-2xl font-mono">
          {counters?.restoreHits ?? 0}
          <span class="text-sky-500">/ {counters?.restoreSeeds ?? 0}</span>
        </div>
        <div class="text-xs text-txtsecondary mt-0.5">
          {counters?.saves ?? 0} saves · {counters?.misses ?? 0} miss · {counters?.errors ?? 0} err
        </div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Preamble seeds</div>
        <div class="text-2xl font-mono">
          {counters?.preambleHits ?? 0}
          <span class="text-purple-400">/ {counters?.preambleMints ?? 0}</span>
        </div>
        <div class="text-xs text-txtsecondary mt-0.5">cold-load hits / mints (system+tools)</div>
      </div>
      <div class="card p-3">
        <div class="text-xs text-txtsecondary">Disk</div>
        <div class="text-2xl font-mono">{fmtBytes(stats.diskBytes)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          {stats.files?.length ?? 0} / {stats.maxFiles} files · cap {fmtBytes(stats.maxBytes)}
        </div>
      </div>
    </div>

    <!-- Preamble caches: one system+tools seed per agent/environment -->
    {#if stats.preambleFiles?.length}
      <div class="card p-3 mb-3">
        <div class="text-sm font-semibold mb-2">
          Preamble caches <span class="text-txtsecondary font-normal">· system+tools seeds, reused on cold load</span>
        </div>
        <div class="overflow-auto max-h-[16rem]">
          <table class="w-full text-xs font-mono">
            <thead class="text-txtsecondary text-left sticky top-0 bg-background">
              <tr>
                <th class="py-1 pr-2">Model</th>
                <th class="py-1 pr-2">Hash</th>
                <th class="py-1 pr-2 text-right">Size</th>
                <th class="py-1 pr-2">Minted</th>
                <th class="py-1">Preamble</th>
              </tr>
            </thead>
            <tbody>
              {#each stats.preambleFiles as f (f.model + f.key)}
                <tr class="border-t border-border">
                  <td class="py-1 pr-2">{f.model}</td>
                  <td class="py-1 pr-2 text-txtsecondary">{f.key}</td>
                  <td class="py-1 pr-2 text-right">{fmtBytes(f.bytes)}</td>
                  <td class="py-1 pr-2 text-txtsecondary">{fmtTime(f.modAt)}</td>
                  <td class="py-1 text-txtsecondary truncate max-w-[20rem]" title={f.preamble}>
                    {f.preamble ?? ""}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}

    <div class="grid grid-cols-1 lg:grid-cols-2 gap-3">
      <!-- Persisted sessions -->
      <div class="card p-3 min-h-0">
        <div class="text-sm font-semibold mb-2">Persisted sessions</div>
        {#if !stats.files?.length}
          <div class="text-xs text-txtsecondary">No saved KV files yet.</div>
        {:else}
          <div class="overflow-auto max-h-[28rem]">
            <table class="w-full text-xs font-mono">
              <thead class="text-txtsecondary text-left sticky top-0 bg-background">
                <tr>
                  <th class="py-1 pr-2">Model</th>
                  <th class="py-1 pr-2">Key</th>
                  <th class="py-1 pr-2 text-right">Size</th>
                  <th class="py-1 pr-2">Saved</th>
                  <th class="py-1">Preamble</th>
                </tr>
              </thead>
              <tbody>
                {#each stats.files as f (f.model + f.key)}
                  <tr class="border-t border-border">
                    <td class="py-1 pr-2">{f.model}</td>
                    <td class="py-1 pr-2 text-txtsecondary">{f.key}</td>
                    <td class="py-1 pr-2 text-right">{fmtBytes(f.bytes)}</td>
                    <td class="py-1 pr-2 text-txtsecondary">{fmtTime(f.modAt)}</td>
                    <td class="py-1 text-txtsecondary truncate max-w-[16rem]" title={f.preamble}>
                      {f.preamble ?? ""}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>

      <!-- Recent events -->
      <div class="card p-3 min-h-0">
        <div class="text-sm font-semibold mb-2">Recent activity</div>
        {#if !stats.events?.length}
          <div class="text-xs text-txtsecondary">No activity yet.</div>
        {:else}
          <div class="overflow-auto max-h-[28rem] font-mono text-xs">
            {#each stats.events as e (e.time + e.op + e.key)}
              {@const s = opStyle[e.op] ?? { cls: "text-txtsecondary", label: e.op }}
              <div class="flex items-center gap-2 py-0.5 border-t border-border">
                <span class="text-txtsecondary w-20 shrink-0">{fmtTime(e.time)}</span>
                <span class="{s.cls} w-12 shrink-0">{s.label}</span>
                <span class="truncate">{e.model}</span>
                {#if e.key}<span class="text-txtsecondary">{e.key}</span>{/if}
                {#if e.tokens}<span class="text-txtsecondary">· {e.tokens} tok</span>{/if}
                {#if e.bytes}<span class="text-txtsecondary">· {fmtBytes(e.bytes)}</span>{/if}
                {#if e.detail}<span class="text-txtsecondary">· {e.detail}</span>{/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>
