<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { scoreLibrary, addScore, abcTitle, type LibraryScore, type ScoreKind } from "../../stores/scoreLibrary";
  import { Search, Upload, FileMusic } from "lucide-svelte";

  // The score library as a list: search, upload, and one row per score. The
  // same list is the Scores mode's left pane and the composer's "From
  // library…" picker, so `onselect` is the only thing the two do differently.
  let {
    selected = "",
    onselect,
    dense = false,
  }: {
    selected?: string;
    onselect: (s: LibraryScore) => void;
    dense?: boolean;
  } = $props();

  // Mirrors the server's cap (maxMusicScoreBytes): a larger score could be
  // kept here but never sent to a generator.
  const MAX_BYTES = 64 << 10;

  let query = $state("");
  let uploadError = $state("");

  const KIND: Record<ScoreKind, string> = { extracted: "From a recording", uploaded: "Uploaded", saved: "Saved" };

  let shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    const list = [...$scoreLibrary].sort((a, b) => b.updatedAt - a.updatedAt);
    if (!q) return list;
    return list.filter((s) => s.title.toLowerCase().includes(q) || (s.origin?.label ?? "").toLowerCase().includes(q));
  });

  async function onFiles(e: Event) {
    const input = e.target as HTMLInputElement;
    const files = [...(input.files ?? [])];
    input.value = "";
    uploadError = "";
    let last = "";
    const skipped: string[] = [];
    for (const f of files) {
      if (f.size > MAX_BYTES) {
        skipped.push(`${f.name} (over ${MAX_BYTES >> 10} KiB)`);
        continue;
      }
      const text = await f.text();
      if (!/^\s*X:/m.test(text) && !/^\s*K:/m.test(text)) {
        skipped.push(`${f.name} (not ABC)`);
        continue;
      }
      last = addScore(text, "uploaded", abcTitle(text, f.name.replace(/\.[^.]+$/, "")));
    }
    if (skipped.length) uploadError = `Skipped ${skipped.join(", ")}`;
    const pick = last && $scoreLibrary.find((s) => s.id === last);
    if (pick) onselect(pick);
  }

  function when(ms: number): string {
    const d = new Date(ms);
    return d.toDateString() === new Date().toDateString()
      ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
      : d.toLocaleDateString([], { month: "short", day: "numeric" });
  }
</script>

<div class="flex flex-col gap-2 min-h-0">
  <div class="flex items-center gap-1.5">
    <div class="relative flex-1 min-w-0">
      <Search class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-txtsecondary pointer-events-none" />
      <input
        type="search"
        bind:value={query}
        placeholder="Search scores"
        aria-label="Search scores"
        class="w-full rounded border border-card-border bg-background pl-7 pr-2 py-1 text-xs focus:outline-none focus:border-primary"
      />
    </div>
    <label class="btn btn--sm inline-flex items-center gap-1.5 shrink-0 cursor-pointer" use:tip={"Add .abc files to the library"}>
      <Upload class="w-3.5 h-3.5" /> Upload
      <input type="file" accept=".abc,text/plain" multiple class="hidden" onchange={onFiles} />
    </label>
  </div>
  {#if uploadError}<span class="text-micro text-error leading-snug">{uploadError}</span>{/if}

  {#if !$scoreLibrary.length}
    <div class="flex flex-col items-center gap-2 py-6 text-center text-xs text-txtsecondary">
      <FileMusic class="w-8 h-8 opacity-40" strokeWidth={1.5} />
      <p class="leading-snug">No scores yet. Every score SheetSage2 reads from a song lands here, and so does any .abc file you upload.</p>
    </div>
  {:else if !shown.length}
    <p class="py-4 text-center text-xs text-txtsecondary">Nothing matches "{query}".</p>
  {:else}
    <ul class="flex flex-col gap-0.5" role="listbox" aria-label="Scores">
      {#each shown as s (s.id)}
        <li>
          <button
            class="w-full text-left rounded-md px-2.5 {dense ? 'py-1' : 'py-1.5'} flex flex-col gap-0.5 hover:bg-secondary {selected === s.id ? 'bg-secondary/80 ring-1 ring-primary/40' : ''}"
            role="option"
            aria-selected={selected === s.id}
            onclick={() => onselect(s)}
          >
            <span class="text-[0.8125rem] text-txtmain truncate">{s.title}</span>
            <span class="flex items-center gap-1.5 text-micro text-txtsecondary min-w-0">
              <span class="truncate">{s.kind === "extracted" && s.origin?.label ? s.origin.label : KIND[s.kind]}</span>
              <span class="ml-auto shrink-0 tabular-nums">{when(s.updatedAt)}</span>
            </span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>
