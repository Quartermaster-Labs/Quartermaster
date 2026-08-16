<script lang="ts">
  import { tip } from "../lib/tooltip";
  // Downloads, as a browser does it: one icon on the status rail with a live
  // count, opening a small panel of what is running and what recently landed.
  //
  // It is a menu rather than a page because monitoring a download is a glance,
  // not a destination — and a pull outlives whatever page started it, so the
  // control has to be reachable from all of them. Job state lives in
  // stores/hubJobs.ts; this component only draws it.
  import { onMount } from "svelte";
  import { link } from "svelte-spa-router";
  import { ArrowDownToLine, X, AlertTriangle, Check, Ban, Folder, Pause, Play } from "lucide-svelte";
  import HubAvatar from "./HubAvatar.svelte";
  import { cancelHubDownload, pauseHubDownload, resumeHubDownload, revealFolder, humanBytes, type HubJob } from "../lib/hubApi";
  import { hubJobs, hubRates, hubActiveCount, refreshHubJobs, isUnfinishedJob } from "../stores/hubJobs";

  let open = $state(false);
  let err = $state<string | null>(null);
  // Job id whose Cancel is awaiting confirmation. One at a time: opening a
  // second confirm replaces the first, so there is never a second armed button
  // sitting off-screen in a scrolled list.
  let confirming = $state<string | null>(null);
  // Ids with a verb in flight, so a double-click can't send pause twice.
  let busy = $state<Record<string, boolean>>({});

  // One check on load: a pull started before this reload (or from another tab)
  // has to show up in the count without anyone opening the panel first. The
  // store schedules its own polling from there and stops when nothing runs.
  onMount(refreshHubJobs);

  // Running AND paused: a paused pull is outstanding work, not history.
  const active = $derived($hubJobs.filter(isUnfinishedJob));
  // Newest first, and capped: this is a recent-activity list, not an archive.
  const history = $derived(
    $hubJobs
      .filter((j) => !isUnfinishedJob(j))
      .slice()
      .sort((a, b) => (b.finished ?? b.started).localeCompare(a.finished ?? a.started))
      .slice(0, 8)
  );

  function toggle(): void {
    open = !open;
    confirming = null;
    // A panel opened by hand should show the truth now, not at the next tick —
    // and while nothing is running the store is not polling at all.
    if (open) refreshHubJobs();
  }

  async function act(j: HubJob, fn: (id: string) => Promise<unknown>): Promise<void> {
    if (busy[j.id]) return;
    err = null;
    busy = { ...busy, [j.id]: true };
    try {
      await fn(j.id);
      // The list is polled, but a verb the user just pressed has to land now —
      // and after a pause the store stops polling entirely, so nothing else
      // would fetch the row that proves it worked.
      await refreshHubJobs();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      const { [j.id]: _, ...rest } = busy;
      busy = rest;
    }
  }

  function pause(j: HubJob): void {
    void act(j, pauseHubDownload);
  }

  function resume(j: HubJob): void {
    void act(j, resumeHubDownload);
  }

  // Cancel DISCARDS the bytes (partials plus anything this job finished — half a
  // sharded GGUF is not a model), which is the whole difference from pause and
  // why it is confirmed rather than one click.
  function cancel(j: HubJob): void {
    confirming = null;
    void act(j, cancelHubDownload);
  }

  function pct(j: HubJob): number {
    return j.total ? Math.min(100, Math.round((j.downloaded / j.total) * 100)) : 0;
  }

  // Blank rather than wrong while the first samples land: "2s left" on a 40 GB
  // file is worse than no number. The rate is smoothed in the store.
  function eta(j: HubJob): string {
    const rate = $hubRates[j.id] ?? 0;
    if (!rate || !j.total || j.phase !== "downloading") return "";
    const secs = Math.round((j.total - j.downloaded) / rate);
    if (secs < 60) return `${secs}s left`;
    if (secs < 3600) return `${Math.round(secs / 60)}m left`;
    return `${Math.floor(secs / 3600)}h ${Math.round((secs % 3600) / 60)}m left`;
  }

  function rateLabel(j: HubJob): string {
    const rate = $hubRates[j.id] ?? 0;
    return rate && j.phase === "downloading" ? `${humanBytes(rate)}/s` : "";
  }

  function hubURL(id: string): string {
    return `https://huggingface.co/${id}`;
  }

  // A path printed under a finished download is a string to read and retype;
  // the server opens it in the file manager instead (loopback-gated, and only
  // for paths inside the models root — see internal/server/revealfolder.go).
  // No argument = the models root itself.
  async function reveal(dir = ""): Promise<void> {
    err = null;
    try {
      await revealFolder(dir);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<svelte:window
  onkeydown={(e) => {
    if (e.key !== "Escape") return;
    // Escape backs out of the armed confirmation first, then the panel — the
    // destructive state is the one you most want an escape hatch from.
    if (confirming) confirming = null;
    else open = false;
  }}
/>

<div class="shrink-0">
  <!-- Same read as the rail's "Unload all" button beside it: main-text coloured,
       outlined on hover. The border is always there but transparent until
       hovered, or the row would shift by a pixel as it appears. -->
  <button
    class="relative flex items-center gap-1 px-1.5 py-1 rounded-md border border-transparent text-txtmain transition-colors {open
      ? 'bg-secondary/60 border-btn-border'
      : 'hover:border-card-border hover:bg-secondary/40'}"
    onclick={toggle}
    use:tip={"Downloads"}
    aria-label="Downloads"
    aria-expanded={open}
  >
    <ArrowDownToLine class="w-4 h-4 {$hubActiveCount ? 'text-primary' : ''}" />
    {#if $hubActiveCount}
      <span class="text-[0.6rem] tabular-nums text-primary">{$hubActiveCount}</span>
    {/if}
  </button>
</div>

{#if open}
  <!-- Click-away backdrop. Transparent and full-screen rather than a document
       listener: it also swallows the click that closes the panel, so it can't
       land on whatever was underneath. -->
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div
    class="fixed inset-0 z-40"
    onclick={() => {
      open = false;
      confirming = null;
    }}
  ></div>

  <!-- Fixed, not absolute: the status rail is an overflow-x-auto strip, which
       would clip a panel positioned inside it. -->
  <div class="fixed right-3 top-11 z-50 w-[26rem] max-w-[calc(100vw-1.5rem)] rounded-md border border-card-border bg-surface shadow-xl font-mono text-xs overflow-hidden">
    <div class="flex items-center gap-2 px-3 py-2 border-b border-card-border">
      <span class="uppercase tracking-[0.15em] text-[0.6rem] text-txtsecondary">Downloads</span>
      <a href="/browse" use:link class="ml-auto text-[0.6rem] text-primary hover:underline" onclick={() => (open = false)}>Browse models</a>
    </div>

    {#if err}
      <div class="px-3 py-2 text-[0.65rem] text-error border-b border-card-border">{err}</div>
    {/if}

    <div class="max-h-[24rem] overflow-y-auto pretty-scroll">
      {#if !active.length && !history.length}
        <div class="px-3 py-4 text-[0.65rem] text-txtsecondary">
          Nothing downloaded this session. Pick a quant on the
          <a href="/browse" use:link class="text-primary underline" onclick={() => (open = false)}>Browse</a> page.
        </div>
      {/if}

      {#each active as j (j.id)}
        <div class="px-3 py-2.5 border-b border-card-border-inner flex flex-col gap-1.5">
          <div class="flex items-center gap-2">
            <HubAvatar author={j.repo.split("/")[0] ?? ""} source={j.source} size="w-6 h-6" />
            <div class="min-w-0 flex-1">
              <div class="text-txtmain truncate">{j.repo}</div>
              <div class="text-[0.6rem] text-txtsecondary tabular-nums truncate">
                {j.label ? j.label + " · " : ""}{j.phase}
                {#if j.files.length > 1}· {j.files.filter((f) => f.done >= f.size && f.size).length}/{j.files.length} files{/if}
              </div>
            </div>
            <span class="text-txtmain tabular-nums shrink-0">{pct(j)}%</span>
            {#if j.phase === "paused"}
              <button class="icon-btn shrink-0" use:tip={"Resume from where it stopped"} disabled={busy[j.id]} onclick={() => resume(j)}>
                <Play class="w-3.5 h-3.5" />
              </button>
            {:else}
              <button class="icon-btn shrink-0" use:tip={"Pause — every byte is kept"} disabled={busy[j.id]} onclick={() => pause(j)}>
                <Pause class="w-3.5 h-3.5" />
              </button>
            {/if}
            <button
              class="icon-btn shrink-0 {confirming === j.id ? 'text-error' : ''}"
              use:tip={"Cancel and delete what has downloaded"}
              disabled={busy[j.id]}
              onclick={() => (confirming = confirming === j.id ? null : j.id)}
            >
              <X class="w-3.5 h-3.5" />
            </button>
          </div>
          <div class="h-1 rounded-full bg-secondary/60 overflow-hidden">
            <div
              class="h-full rounded-full transition-[width] duration-500 {j.phase === 'paused' ? 'bg-txtsecondary' : 'bg-primary'}"
              style="width:{pct(j)}%"
            ></div>
          </div>
          <div class="flex items-center gap-2 text-[0.6rem] text-txtsecondary tabular-nums">
            <span>{humanBytes(j.downloaded)} / {humanBytes(j.total)}</span>
            {#if rateLabel(j)}<span>{rateLabel(j)}</span>{/if}
            {#if eta(j)}<span class="ml-auto">{eta(j)}</span>{/if}
          </div>
          {#if confirming === j.id}
            <!-- Inline rather than a window.confirm(): it names the amount that
                 is about to be thrown away, which is the fact the decision turns
                 on, and it can't be suppressed by the browser's "block dialogs". -->
            <div class="flex items-center gap-2 rounded border border-error/40 bg-error/10 px-2 py-1.5 text-[0.6rem]">
              <span class="text-txtmain">Delete {humanBytes(j.downloaded)} already downloaded?</span>
              <button class="ml-auto btn btn--sm" onclick={() => (confirming = null)}>Keep</button>
              <button class="btn btn--sm btn--danger" onclick={() => cancel(j)}>Delete</button>
            </div>
          {/if}
        </div>
      {/each}

      {#each history as j (j.id)}
        <div class="px-3 py-2 border-b border-card-border-inner last:border-b-0 flex items-start gap-2">
          <HubAvatar author={j.repo.split("/")[0] ?? ""} source={j.source} size="w-6 h-6" />
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-1.5">
              {#if j.phase === "done"}
                <Check class="w-3 h-3 text-success shrink-0" />
              {:else if j.phase === "error"}
                <AlertTriangle class="w-3 h-3 text-error shrink-0" />
              {:else}
                <Ban class="w-3 h-3 text-txtsecondary shrink-0" />
              {/if}
              <span class="text-txtmain truncate">{j.repo}</span>
            </div>
            <div class="text-[0.6rem] text-txtsecondary tabular-nums">
              {j.label ? j.label + " · " : ""}{j.phase} · {humanBytes(j.downloaded)}
            </div>
            {#if j.error}
              <div class="text-[0.6rem] text-error">
                {j.error}
                {#if j.gated}
                  <a class="underline ml-1" href={hubURL(j.repo)} target="_blank" rel="noreferrer">Accept the license</a>
                {/if}
              </div>
            {/if}
            {#if j.phase === "done" && j.dir}
              <button
                class="flex w-full items-center gap-1 text-left text-[0.6rem] text-txtsecondary hover:text-primary hover:underline transition-colors"
                use:tip={"Open this folder"}
                onclick={() => reveal(j.dir)}
              >
                <Folder class="w-3 h-3 shrink-0" /><span class="truncate">{j.dir}</span>
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <div class="px-3 py-1.5 border-t border-card-border text-[0.6rem]">
      <button class="inline-flex items-center gap-1 text-primary hover:underline" onclick={() => reveal()}>
        <Folder class="w-3 h-3" /> Open models folder
      </button>
    </div>
  </div>
{/if}
