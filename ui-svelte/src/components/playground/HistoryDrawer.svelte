<script lang="ts" module>
  // One row of the drawer. `count` feeds the meta line ("12 msgs · 3m ago").
  export type DrawerSession = { id: string; title: string; updatedAt: number; count: number };
</script>

<script lang="ts">
  import { Plus, Search, Trash2, X } from "lucide-svelte";
  import { tip } from "../../lib/tooltip";
  import { dayBucket, relTime, type DayBucket } from "../../lib/playgroundThreads";

  interface Props {
    heading: string;
    sessions: DrawerSession[];
    activeId: string | null;
    generatingId: string | null;
    // "msg" / "turn" / "take": pluralised with a plain "s".
    unit: string;
    emptyLabel: string;
    newTip?: string;
    onNew?: () => void;
    onOpen: (id: string) => void;
    onDelete: (id: string) => void;
    // Image-like tabs show a strip of results under the title.
    thumbsFor?: (id: string) => string[];
  }

  let { heading, sessions, activeId, generatingId, unit, emptyLabel, newTip, onNew, onOpen, onDelete, thumbsFor }: Props = $props();

  let query = $state("");

  // Re-bucket once a minute so "Today" rolls over and "3m ago" keeps moving
  // while the drawer sits open.
  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 60_000);
    return () => clearInterval(t);
  });

  const ORDER: DayBucket[] = ["Today", "Yesterday", "Last 7 days", "Older"];

  let groups = $derived.by(() => {
    const q = query.trim().toLowerCase();
    const rows = [...sessions]
      .filter((s) => !q || (s.title || emptyLabel).toLowerCase().includes(q))
      .sort((a, b) => b.updatedAt - a.updatedAt);
    const by = new Map<DayBucket, DrawerSession[]>();
    for (const s of rows) {
      const b = dayBucket(s.updatedAt, now);
      if (!by.has(b)) by.set(b, []);
      by.get(b)!.push(s);
    }
    return ORDER.filter((b) => by.has(b)).map((b) => ({ label: b, rows: by.get(b)! }));
  });
</script>

<!-- Fixed width so the slide-in transition on the wrapper clips it instead of
     reflowing the rows while it animates. -->
<aside class="w-[16.25rem] h-full flex flex-col min-h-0 bg-rail border-r border-card-border-inner">
  <div class="flex items-center gap-2 pl-4 pr-2 h-10 border-b border-card-border-inner shrink-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">{heading}</span>
    <span class="font-mono text-micro text-txtsecondary tabular-nums">{sessions.length}</span>
    {#if onNew && newTip}
      <button class="btn btn--sm btn--icon ml-auto h-7" onclick={onNew} use:tip={newTip} aria-label={newTip}>
        <Plus class="w-3.5 h-3.5" />
      </button>
    {/if}
  </div>

  <div class="px-3 py-2 shrink-0">
    <div class="relative">
      <Search class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-txtsecondary pointer-events-none" />
      <input
        bind:value={query}
        type="text"
        placeholder="Search…"
        onkeydown={(e) => e.key === "Escape" && (query = "")}
        class="w-full rounded border border-card-border bg-background pl-7 pr-6 py-1 text-xs focus:outline-none focus:border-primary"
      />
      {#if query}
        <button
          class="absolute right-1 top-1/2 -translate-y-1/2 p-0.5 text-txtsecondary hover:text-txtmain"
          onclick={() => (query = "")}
          aria-label="Clear search"
        >
          <X class="w-3 h-3" />
        </button>
      {/if}
    </div>
  </div>

  <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll pb-2">
    {#each groups as g (g.label)}
      <div class="px-4 pt-3 pb-1 text-micro font-medium uppercase tracking-wide text-txtsecondary">{g.label}</div>
      {#each g.rows as s (s.id)}
        {@const on = s.id === activeId}
        {@const thumbs = thumbsFor ? thumbsFor(s.id) : []}
        <div
          class="group/row relative flex items-start gap-2 pl-4 pr-2 py-2 transition-colors {on
            ? 'bg-secondary text-txtmain shadow-[inset_2px_0_var(--color-primary)]'
            : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
        >
          <button class="flex-1 min-w-0 flex flex-col gap-0.5 text-left" onclick={() => onOpen(s.id)}>
            <span class="flex items-center gap-1.5 min-w-0">
              {#if s.id === generatingId}
                <span class="w-1.5 h-1.5 shrink-0 rounded-full bg-primary reason-glow" use:tip={"Generating…"}></span>
              {/if}
              <span class="truncate text-[0.8125rem] {on ? 'text-txtmain' : ''}">{s.title || emptyLabel}</span>
            </span>
            <span class="font-mono text-micro text-txtsecondary tabular-nums">
              {s.count} {unit}{s.count === 1 ? "" : "s"} · {relTime(s.updatedAt, now)}
            </span>
            {#if thumbs.length}
              <span class="flex gap-1 mt-1">
                {#each thumbs as th, i (i)}
                  <img src={th} alt="" class="w-11 h-8 rounded object-cover border border-card-border bg-secondary" />
                {/each}
              </span>
            {/if}
          </button>
          <button
            class="shrink-0 p-1 rounded text-txtsecondary opacity-0 group-hover/row:opacity-100 focus:opacity-100 hover:text-error transition-opacity"
            onclick={(e) => { e.stopPropagation(); onDelete(s.id); }}
            use:tip={"Delete"}
            aria-label="Delete"
          >
            <Trash2 class="w-3.5 h-3.5" />
          </button>
        </div>
      {/each}
    {:else}
      <p class="px-4 py-6 text-xs text-txtsecondary text-center">{query ? "No matches." : "Nothing here yet."}</p>
    {/each}
  </div>
</aside>
