<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { HelpCircle } from "lucide-svelte";
  import { onDestroy } from "svelte";
  import { fetchKvCache, type KvCacheStats } from "../stores/api";
  import { observeTab } from "../stores/observe";

  // The two file categories differ in a way that is not obvious from the tables:
  // sessions are per chat and user-triggered, preamble caches are per agent and
  // minted unprompted. Spelled out in the tab tooltips.
  const SESSIONS_HELP =
    "One file per conversation: the whole slot KV of a chat, saved when it is about to be evicted and restored (instead of reprocessed) when that chat comes back. Keyed by the conversation, so a chat overwrites its own file each turn. Only saved past the min-context threshold, and LRU-pruned to the disk / session caps in Settings.\n\nPer model: enable \"Save KV cache to disk\" in the model config editor.";
  const PREAMBLE_HELP =
    "One file per (model, agent): the system prompt + tool definitions alone, prefilled once and reused as a seed by every cold load that sends the same preamble. That is why they appear without you starting a chat: the first request from an agent mints one. They are big, exempt from the LRU disk cap (only the newest 3 per model are kept), and skipped on hybrid/recurrent models, which cannot rewind a partial prefix.\n\nPer model: \"Preamble caches\" in the model config editor; fleet-wide in Settings -> KV cache.";

  let stats = $state<KvCacheStats | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  async function tick() {
    const s = await fetchKvCache();
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
    "restore-hit": { cls: "text-sky-500", label: "load" },
    "restore-seed": { cls: "text-sky-500", label: "seed" },
    "seed-pending": { cls: "text-sky-400", label: "seed?" },
    "preamble-hit": { cls: "text-sky-500", label: "pre load" },
    "preamble-mint": { cls: "text-purple-400", label: "pre mint" },
    save: { cls: "text-amber-500", label: "save" },
    miss: { cls: "text-txtsecondary", label: "miss" },
    error: { cls: "text-red-500", label: "error" },
  };

  // Reading a KV file off disk is NOT a cache hit - the upstream can load it and
  // still reprefill the whole prompt (it does, on some architectures). So an op
  // that loads KV is painted by its OUTCOME, and stays amber until the next
  // request reports how many tokens it actually reused. Only `reused` goes green.
  const outcomeStyle: Record<string, { cls: string; note: string }> = {
    pending: { cls: "text-amber-500", note: "awaiting reuse" },
    reused: { cls: "text-green-500", note: "reused" },
    "no-reuse": { cls: "text-red-500", note: "reprefilled anyway" },
    unconfirmed: { cls: "text-txtsecondary", note: "unconfirmed - model stopped first" },
  };

  function rowStyle(e: { op: string; outcome?: string }): {
    cls: string;
    label: string;
    note: string;
  } {
    const base = opStyle[e.op] ?? { cls: "text-txtsecondary", label: e.op };
    const out = e.outcome ? outcomeStyle[e.outcome] : undefined;
    return out ? { cls: out.cls, label: base.label, note: out.note } : { ...base, note: "" };
  }

  let kvTab = $state<"sessions" | "preamble">("sessions");

  const counters = $derived(stats?.counters);
  // Confirmed reuse is the honest metric: the request after a restore actually
  // reported cached_tokens > 0 from the upstream.
  const confirmTotal = $derived(
    (counters?.confirmedReuses ?? 0) + (counters?.confirmedMisses ?? 0)
  );
  const confirmPct = $derived(
    confirmTotal > 0 ? Math.round((100 * (counters?.confirmedReuses ?? 0)) / confirmTotal) : 0
  );
  // Loads still waiting on their request. A big prefill can hold one of these open
  // for minutes, which is exactly the window in which a "hit" used to look settled.
  const pending = $derived((stats?.events ?? []).filter((e) => e.outcome === "pending").length);
  function fmtNum(n?: number): string {
    return (n ?? 0).toLocaleString();
  }
</script>

<!-- Bands, not boxes: each section spans the page and is closed off by a
     hairline, so the readout reads as one continuous surface. -->
<div class="h-full overflow-auto pretty-scroll bg-surface">
  {#if !stats}
    <div class="px-3 py-4 text-txtsecondary text-sm">Loading…</div>
  {:else if !stats.enabled}
    <div class="px-3 py-4 text-sm text-txtsecondary">
      Slot KV cache is <span class="text-txtmain font-semibold">disabled</span>. Enable it in the
      dashboard GPU-memory settings (and give models <code>--slot-save-path</code>) to persist and
      reuse conversation KV across model swaps.
    </div>
  {:else}
    <!-- Summary tiles: one card, hairline-separated. gap-px over the divider
         colour draws the rules — unlike divide-x it stays correct when the grid
         rewraps to 3 or 2 columns. -->
    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-px bg-card-border-inner border-b border-card-border-inner">
      <div class="bg-surface p-3">
        <div class="text-xs text-txtsecondary">Tokens reused (confirmed)</div>
        <div class="text-2xl font-mono text-green-500">{fmtNum(counters?.cachedTokensSeen)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          cached_tokens from upstream - actual prefill skipped
        </div>
      </div>
      <div class="bg-surface p-3">
        <div class="text-xs text-txtsecondary">Restore success</div>
        <div class="text-2xl font-mono">{confirmPct}%</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          {counters?.confirmedReuses ?? 0} reused / {counters?.confirmedMisses ?? 0} no-reuse
          {#if pending}<span class="text-amber-500">· {pending} awaiting</span>{/if}
        </div>
      </div>
      <div class="bg-surface p-3">
        <div class="text-xs text-txtsecondary">KV files loaded · exact / seed</div>
        <div class="text-2xl font-mono">
          {counters?.restoreHits ?? 0}
          <span class="text-sky-500">/ {counters?.restoreSeeds ?? 0}</span>
        </div>
        <div class="text-xs text-txtsecondary mt-0.5">
          reads attempted, not reuse - see restore success
        </div>
        <div class="text-xs text-txtsecondary">
          {counters?.saves ?? 0} saves · {counters?.misses ?? 0} miss · {counters?.errors ?? 0} err
        </div>
      </div>
      <div class="bg-surface p-3">
        <div class="text-xs text-txtsecondary">Preamble seeds</div>
        <div class="text-2xl font-mono">
          {counters?.preambleHits ?? 0}
          <span class="text-purple-400">/ {counters?.preambleMints ?? 0}</span>
        </div>
        <div class="text-xs text-txtsecondary mt-0.5">cold-load hits / mints (system+tools)</div>
      </div>
      <div class="bg-surface p-3">
        <div class="text-xs text-txtsecondary">Disk</div>
        <div class="text-2xl font-mono">{fmtBytes(stats.diskBytes)}</div>
        <div class="text-xs text-txtsecondary mt-0.5">
          {stats.files?.length ?? 0} / {stats.maxFiles} files · cap {fmtBytes(stats.maxBytes)}
        </div>
      </div>
    </div>

    <!-- Live slots: only meaningful once some model runs more than one slot -->
    {#if stats.slots?.length}
      <div class="p-3 border-b border-card-border-inner">
        <div class="text-sm font-semibold mb-2">
          Live slots
          <span class="text-txtsecondary font-normal text-xs">
            which conversation sits on which server slot
          </span>
        </div>
        <div class="overflow-auto max-h-[14rem] pretty-scroll">
          <table class="data-table w-full text-xs font-mono">
            <thead class="text-txtsecondary text-left sticky top-0 bg-background">
              <tr class="rule">
                <th class="py-1.5 pr-2">Model</th>
                <th class="py-1.5 pr-2 text-right">Slot</th>
                <th class="py-1.5 pr-2">Key</th>
                <th class="py-1.5 pr-2">State</th>
                <th class="py-1.5 pr-2">Last used</th>
                <th class="py-1.5">Preamble</th>
              </tr>
            </thead>
            <tbody>
              {#each stats.slots as sl (sl.model + "#" + sl.slot)}
                <tr class="rule">
                  <td class="py-1.5 pr-2">{sl.model}</td>
                  <td class="py-1.5 pr-2 text-right">{sl.slot}</td>
                  <td class="py-1.5 pr-2 text-txtsecondary">{sl.key}</td>
                  <td class="py-1.5 pr-2 {sl.dirty ? 'text-amber-500' : 'text-txtsecondary'}">
                    {sl.dirty ? "unsaved" : "saved"}
                  </td>
                  <td class="py-1.5 pr-2 text-txtsecondary">{fmtTime(sl.lastUsed)}</td>
                  <td class="py-1.5 text-txtsecondary truncate max-w-[16rem]" use:tip={sl.preamble}>
                    {sl.preamble ?? ""}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}

    <!-- Saved files and the event log are one surface split by a divider. -->
    <div class="grid grid-cols-1 lg:grid-cols-2 divide-y lg:divide-y-0 lg:divide-x divide-card-border-inner">
      <!-- Persisted sessions + preamble caches: two tabs of one box -->
      <div class="p-3 min-h-0">
        <div class="flex items-center gap-1 mb-2 border-b border-border">
          <button
            class="px-2 py-1 text-sm font-semibold border-b-2 -mb-px transition-colors {kvTab === 'sessions'
              ? 'border-primary text-primary'
              : 'border-transparent text-txtsecondary hover:text-txtmain'}"
            onclick={() => (kvTab = "sessions")}
          >
            Persisted sessions
            <span class="text-txtsecondary font-normal">{stats.files?.length ?? 0}</span>
          </button>
          {@render hint(SESSIONS_HELP)}
          <button
            class="px-2 py-1 text-sm font-semibold border-b-2 -mb-px transition-colors {kvTab === 'preamble'
              ? 'border-primary text-primary'
              : 'border-transparent text-txtsecondary hover:text-txtmain'}"
            onclick={() => (kvTab = "preamble")}
          >
            Preamble caches
            <span class="text-txtsecondary font-normal">{stats.preambleFiles?.length ?? 0}</span>
          </button>
          {@render hint(PREAMBLE_HELP)}
        </div>

        {#if kvTab === "sessions"}
          {#if !stats.files?.length}
            <div class="text-xs text-txtsecondary">No saved KV files yet.</div>
          {:else}
            <div class="overflow-auto max-h-[28rem] pretty-scroll">
              <table class="data-table w-full text-xs font-mono">
                <thead class="text-txtsecondary text-left sticky top-0 bg-background">
                  <tr class="rule">
                    <th class="py-1.5 pr-2">Model</th>
                    <th class="py-1.5 pr-2">Key</th>
                    <th class="py-1.5 pr-2 text-right">Size</th>
                    <th class="py-1.5 pr-2">Saved</th>
                    <th class="py-1.5">Preamble</th>
                  </tr>
                </thead>
                <tbody>
                  {#each stats.files as f (f.model + f.key)}
                    <tr class="rule">
                      <td class="py-1.5 pr-2">{f.model}</td>
                      <td class="py-1.5 pr-2 text-txtsecondary">{f.key}</td>
                      <td class="py-1.5 pr-2 text-right">{fmtBytes(f.bytes)}</td>
                      <td class="py-1.5 pr-2 text-txtsecondary">{fmtTime(f.modAt)}</td>
                      <td class="py-1.5 text-txtsecondary truncate max-w-[16rem]" use:tip={f.preamble}>
                        {f.preamble ?? ""}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        {:else}
          <div class="text-xs text-txtsecondary mb-2">system+tools seeds, reused on cold load</div>
          {#if !stats.preambleFiles?.length}
            <div class="text-xs text-txtsecondary">No preamble caches yet.</div>
          {:else}
            <div class="overflow-auto max-h-[28rem] pretty-scroll">
              <table class="data-table w-full text-xs font-mono">
                <thead class="text-txtsecondary text-left sticky top-0 bg-background">
                  <tr class="rule">
                    <th class="py-1.5 pr-2">Model</th>
                    <th class="py-1.5 pr-2">Hash</th>
                    <th class="py-1.5 pr-2 text-right">Size</th>
                    <th class="py-1.5 pr-2">Minted</th>
                    <th class="py-1.5">Preamble</th>
                  </tr>
                </thead>
                <tbody>
                  {#each stats.preambleFiles as f (f.model + f.key)}
                    <tr class="rule">
                      <td class="py-1.5 pr-2">{f.model}</td>
                      <td class="py-1.5 pr-2 text-txtsecondary">{f.key}</td>
                      <td class="py-1.5 pr-2 text-right">{fmtBytes(f.bytes)}</td>
                      <td class="py-1.5 pr-2 text-txtsecondary">{fmtTime(f.modAt)}</td>
                      <td class="py-1.5 text-txtsecondary truncate max-w-[16rem]" use:tip={f.preamble}>
                        {f.preamble ?? ""}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        {/if}
      </div>

      <!-- Recent events -->
      <div class="p-3 min-h-0">
        <div class="text-sm font-semibold mb-2">
          Recent activity
          <span class="text-txtsecondary font-normal text-xs">
            a load turns green only once a request confirms it reused the KV
          </span>
        </div>
        {#if !stats.events?.length}
          <div class="text-xs text-txtsecondary">No activity yet.</div>
        {:else}
          <div class="overflow-auto max-h-[28rem] font-mono text-xs pretty-scroll">
            {#each stats.events as e (e.seq ?? e.time + e.op + e.key)}
              {@const s = rowStyle(e)}
              <div class="flex items-center gap-2 py-0.5 border-t border-border">
                <span class="text-txtsecondary w-20 shrink-0">{fmtTime(e.time)}</span>
                <span class="{s.cls} w-16 shrink-0">{s.label}</span>
                <span class="truncate">{e.model}</span>
                {#if e.slot > 0}<span class="text-txtsecondary shrink-0">slot {e.slot}</span>{/if}
                {#if e.key}<span class="text-txtsecondary">{e.key}</span>{/if}
                {#if e.tokens}<span class="text-txtsecondary">· {e.tokens} tok</span>{/if}
                {#if e.bytes}<span class="text-txtsecondary">· {fmtBytes(e.bytes)}</span>{/if}
                {#if s.note}<span class="{s.cls} shrink-0">· {s.note}</span>{/if}
                {#if e.detail}<span class="text-txtsecondary">· {e.detail}</span>{/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

{#snippet hint(text: string)}
  <span
    class="inline-flex shrink-0 align-middle text-txtsecondary cursor-help hover:text-txtmain"
    use:tip={text}
    aria-label={text}><HelpCircle size={12} /></span>
{/snippet}
