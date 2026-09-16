<script lang="ts">
  /**
   * The Browse page's detail pane for one audio.cpp family.
   *
   * audio.cpp models are listed in the ordinary TTS and Transcribe tabs, but
   * they are not hub repos and cannot be shown like one: ~70 families are
   * published into a handful of shared GGUF repos, so there is no repo page to
   * read, no per-repo file table, and the file names alone do not say which
   * family a name belongs to or which files belong together. The server reads
   * the catalog out of the INSTALLED backend (model_specs/, shipped beside the
   * binary) and this renders one family's packages.
   *
   * The download is the ordinary one: a package's files go to startHubDownload,
   * so resume, pause and the Downloads rail all work unchanged.
   */
  import { Download, Check, AlertTriangle, RefreshCw } from "lucide-svelte";
  import { startHubDownload, type AudioCppFamily, type AudioCppPackage } from "../lib/hubApi";
  import { hubJobs, refreshHubJobs, isRunningJob } from "../stores/hubJobs";
  import { tip } from "../lib/tooltip";

  let { family }: { family: AudioCppFamily } = $props();

  let err = $state<string | null>(null);

  // Keyed by FILE path, not by repo: one repo holds every family, so "is this
  // repo busy" would mark all 200-odd packages in the catalog as downloading.
  const inFlight = $derived(new Set($hubJobs.filter(isRunningJob).flatMap((j) => j.files.map((f) => f.path))));

  function pkgState(p: AudioCppPackage): "local" | "downloading" | "ready" {
    if (p.files.some((f) => inFlight.has(f))) return "downloading";
    if (p.local) return "local";
    return "ready";
  }

  async function download(p: AudioCppPackage): Promise<void> {
    err = null;
    try {
      // The whole file set as one job: a package's sidecar (an f5_tts vocab.txt)
      // is not an alternative to the gguf, it is part of it.
      await startHubDownload(p.repo, p.files, `${family.displayName} ${p.displayName}`);
      await refreshHubJobs();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<!-- Header, in the same shape as the repo header beside it: name, what it is,
     then the list of things you can take. -->
<div class="p-4 border-b border-card-border">
  <div class="flex items-center gap-2 flex-wrap">
    <span class="text-sm text-txtmain">{family.displayName}</span>
    <span class="font-mono text-[0.6rem] uppercase tracking-wide px-1.5 rounded-full border border-card-border text-txtsecondary">audio.cpp</span>
    {#if family.task}
      <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{family.task}</span>
    {/if}
    {#if family.status && family.status !== "supported"}
      <!-- Upstream's own confidence in the family, passed through verbatim:
           "experimental" there means the same here. -->
      <span class="font-mono text-[0.6rem] uppercase tracking-wide text-warning">{family.status}</span>
    {/if}
  </div>
  <div class="mt-0.5 font-mono text-[0.65rem] text-txtsecondary">{family.family}</div>
  {#if family.description}
    <p class="mt-2 text-xs text-txtsecondary">{family.description}</p>
  {/if}
  {#if family.languages?.length}
    <p class="mt-1 text-[0.65rem] text-txtsecondary">{family.languages.join(", ")}</p>
  {/if}
  {#if !family.supported}
    <!-- Listed rather than hidden: "the engine can run this, the app cannot
         serve it yet" is information, and the two causes need different fixes. -->
    <p class="mt-2 inline-flex items-start gap-1 text-[0.7rem] text-warning">
      <AlertTriangle class="w-3 h-3 shrink-0 mt-0.5" />
      {family.reason}
    </p>
  {/if}
</div>

{#if err}
  <div class="m-4 card text-xs text-danger">{err}</div>
{/if}

<div class="divide-y divide-card-border-inner">
  {#each family.packages as p (p.id)}
    {@const st = pkgState(p)}
    <div class="px-4 py-2.5 flex items-center gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <span class="text-xs text-txtmain truncate">{p.displayName}</span>
          {#if p.default}
            <span class="font-mono text-[0.6rem] uppercase tracking-wide text-primary">recommended</span>
          {/if}
        </div>
        <div class="font-mono text-[0.65rem] text-txtsecondary truncate" use:tip={p.files.join("\n")}>
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
        <button class="btn h-7 shrink-0 inline-flex items-center gap-1" onclick={() => download(p)}>
          <Download class="w-3.5 h-3.5" /> Download
        </button>
      {/if}
    </div>
  {/each}
</div>
