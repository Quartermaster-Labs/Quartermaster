<script lang="ts">
  /**
   * The Browse page's audio.cpp tab.
   *
   * Not a hub search. audio.cpp publishes ~70 families into a handful of shared
   * GGUF repos, so searching the hub for them returns hundreds of loose file
   * names with nothing saying which family a name belongs to or which files
   * belong together. The server reads the catalog out of the INSTALLED backend
   * (model_specs/, shipped beside the binary) and this renders it. The download
   * itself is the ordinary one: a package's files go to startHubDownload, so
   * resume, pause and the Downloads rail all work unchanged.
   */
  import { onMount } from "svelte";
  import { Download, Check, AlertTriangle, RefreshCw } from "lucide-svelte";
  import { getAudioCppCatalog, startHubDownload, HubApiError, type AudioCppFamily, type AudioCppPackage } from "../lib/hubApi";
  import { hubJobs, refreshHubJobs, isRunningJob } from "../stores/hubJobs";
  import { tip } from "../lib/tooltip";

  let families = $state<AudioCppFamily[]>([]);
  let installed = $state(false);
  let loading = $state(true);
  let err = $state<string | null>(null);
  let query = $state("");
  let task = $state<"all" | "tts" | "asr" | "other">("all");
  let open = $state<Record<string, boolean>>({});

  const TASKS: { id: typeof task; label: string }[] = [
    { id: "all", label: "All" },
    { id: "tts", label: "Speech" },
    { id: "asr", label: "Transcription" },
    // Families audio.cpp runs and we do not serve yet. Listed rather than
    // hidden: "the engine can do this, the app cannot yet" is information, and
    // a catalog silently three quarters as long looks broken.
    { id: "other", label: "Not wired up" },
  ];

  async function load(): Promise<void> {
    loading = true;
    try {
      const c = await getAudioCppCatalog();
      families = c.families;
      installed = c.installed;
      err = c.error ?? null;
    } catch (e) {
      // 501 means the build has no model browser at all; the page above already
      // says so, so this stays quiet rather than stacking a second banner.
      err = e instanceof HubApiError && e.status === 501 ? null : e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  onMount(load);

  // Every repo path this catalog is currently pulling. Keyed by path, not by
  // repo: one repo holds every family, so "is this repo busy" would mark all 200
  // packages as downloading.
  const inFlight = $derived(new Set($hubJobs.filter(isRunningJob).flatMap((j) => j.files.map((f) => f.path))));

  const shown = $derived(
    families.filter((f) => {
      if (task === "other" ? f.supported : task !== "all" && f.task !== task) return false;
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return (
        f.displayName.toLowerCase().includes(q) ||
        f.family.toLowerCase().includes(q) ||
        (f.description ?? "").toLowerCase().includes(q) ||
        (f.languages ?? []).some((l) => l.toLowerCase() === q)
      );
    })
  );

  function pkgState(p: AudioCppPackage): "local" | "downloading" | "ready" {
    if (p.files.some((f) => inFlight.has(f))) return "downloading";
    if (p.local) return "local";
    return "ready";
  }

  async function download(f: AudioCppFamily, p: AudioCppPackage): Promise<void> {
    err = null;
    try {
      // The whole file set as one job: a package's sidecar (an f5_tts vocab.txt)
      // is not an alternative to the gguf, it is part of it.
      await startHubDownload(p.repo, p.files, `${f.displayName} ${p.displayName}`);
      await refreshHubJobs();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }

  // Re-read the catalog when the last of our jobs lands: "downloaded" is judged
  // off disk, so without this the row that just finished keeps its button.
  let running = $derived($hubJobs.filter(isRunningJob).length);
  let wasRunning = $state(0);
  $effect(() => {
    const now = running;
    if (wasRunning > 0 && now === 0) void load();
    wasRunning = now;
  });
</script>

<div class="flex flex-col h-full min-h-0">
  <div class="shrink-0 flex flex-wrap items-stretch gap-2 px-3 py-1.5 bg-surface">
    <div class="relative w-[22rem] max-w-full h-7 shrink-0">
      <input
        class="w-full h-full px-3 py-0 rounded-full border border-card-border bg-background text-xs text-txtmain placeholder:text-txtsecondary focus:outline-none focus:border-primary transition-colors"
        placeholder="Filter audio.cpp families…"
        bind:value={query}
      />
    </div>
    <div class="seg h-7 shrink-0">
      {#each TASKS as t (t.id)}
        <button type="button" aria-pressed={task === t.id} onclick={() => (task = t.id)}>{t.label}</button>
      {/each}
    </div>
    <span class="ml-auto self-center font-mono text-[0.65rem] text-txtsecondary tabular-nums">
      {shown.length} of {families.length} families
    </span>
  </div>

  <div class="flex-1 min-h-0 overflow-y-auto px-3 pb-3 flex flex-col gap-2">
    {#if err}
      <div class="card text-sm text-danger">{err}</div>
    {:else if loading}
      <div class="card text-sm text-txtsecondary">Reading the installed audio.cpp catalog…</div>
    {:else if !installed}
      <!-- The catalog lives inside the backend, so there is nothing to show
           until it is installed. Point at the fix rather than showing an empty
           list that looks like a broken page. -->
      <div class="card text-sm text-txtsecondary">
        audio.cpp is not installed. Install it from the <strong class="text-txtmain">Backends</strong> tab, then reopen this tab: the model
        catalog ships inside the backend, so it arrives with the binary.
      </div>
    {:else if shown.length === 0}
      <div class="card text-sm text-txtsecondary">No family matches that filter.</div>
    {:else}
      {#each shown as f (f.family)}
        <div class="card p-0 overflow-hidden">
          <button
            class="w-full text-left px-3 py-2 flex items-start gap-3 hover:bg-surface transition-colors"
            onclick={() => (open = { ...open, [f.family]: !open[f.family] })}
          >
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="text-sm text-txtmain truncate">{f.displayName}</span>
                <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{f.family}</span>
                {#if f.task}
                  <span class="font-mono text-[0.6rem] uppercase tracking-wide px-1.5 rounded-full border border-card-border text-txtsecondary">
                    {f.task}
                  </span>
                {/if}
                {#if f.status && f.status !== "supported"}
                  <!-- Upstream's own confidence in the family, passed through
                       verbatim: "experimental" there means the same here. -->
                  <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{f.status}</span>
                {/if}
                {#if f.packages.some((p) => p.local)}
                  <span class="inline-flex items-center gap-1 font-mono text-[0.6rem] uppercase tracking-wide text-primary">
                    <Check class="w-3 h-3" /> downloaded
                  </span>
                {/if}
              </div>
              {#if f.description}
                <p class="mt-0.5 text-xs text-txtsecondary line-clamp-2">{f.description}</p>
              {/if}
              {#if !f.supported}
                <p class="mt-1 inline-flex items-center gap-1 text-[0.7rem] text-txtsecondary">
                  <AlertTriangle class="w-3 h-3 shrink-0" />
                  {f.reason}
                </p>
              {/if}
            </div>
            <span class="font-mono text-[0.65rem] text-txtsecondary tabular-nums shrink-0 mt-0.5">
              {f.packages.length}
              {f.packages.length === 1 ? "build" : "builds"}
            </span>
          </button>

          {#if open[f.family]}
            <div class="border-t border-card-border divide-y divide-card-border">
              {#each f.packages as p (p.id)}
                {@const st = pkgState(p)}
                <div class="px-3 py-2 flex items-center gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="text-xs text-txtmain truncate">{p.displayName}</div>
                    <div class="font-mono text-[0.6rem] text-txtsecondary truncate" use:tip={p.files.join("\n")}>
                      {p.repo}{p.files.length > 1 ? ` · ${p.files.length} files` : ""}
                    </div>
                  </div>
                  {#if st === "local"}
                    <span class="inline-flex items-center gap-1 font-mono text-[0.65rem] uppercase text-primary shrink-0">
                      <Check class="w-3.5 h-3.5" /> on disk
                    </span>
                  {:else if st === "downloading"}
                    <span class="inline-flex items-center gap-1 font-mono text-[0.65rem] uppercase text-txtsecondary shrink-0">
                      <RefreshCw class="w-3.5 h-3.5 animate-spin" /> downloading
                    </span>
                  {:else}
                    <button class="btn h-7 shrink-0 inline-flex items-center gap-1" onclick={() => download(f, p)}>
                      <Download class="w-3.5 h-3.5" /> Download
                    </button>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/each}
    {/if}
  </div>
</div>
