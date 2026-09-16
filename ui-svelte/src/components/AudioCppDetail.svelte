<script lang="ts">
  /**
   * The Browse page's detail pane for one audio.cpp family.
   *
   * Deliberately the SAME pane as a repo page, down to the file table: an
   * audio.cpp family is an ordinary Hugging Face repo with a curated file list,
   * and a second visual language for it would make the user learn which engine
   * a model belongs to before they could read its page. What it cannot mirror
   * is the repo-wide half (README, downloads, likes): ~70 families share a
   * handful of GGUF repos, so those describe the repo and not this model. The
   * catalog itself is served from the model_specs/ the INSTALLED backend ships
   * (GET /api/hub/audiocpp).
   *
   * The download is the ordinary one too: a package's files go to
   * startHubDownload, so resume, pause and the Downloads rail work unchanged.
   */
  import { Download, Check, AlertTriangle, ExternalLink, Lock, RefreshCw } from "lucide-svelte";
  import HubAvatar from "./HubAvatar.svelte";
  import { startHubDownload, humanBytes, type AudioCppFamily, type AudioCppPackage } from "../lib/hubApi";
  import { hubJobs, refreshHubJobs, isRunningJob } from "../stores/hubJobs";
  import { tip } from "../lib/tooltip";

  let { family }: { family: AudioCppFamily } = $props();

  let err = $state<string | null>(null);

  const author = $derived((family.packages[0]?.repo ?? "").split("/")[0] ?? "");

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

<!-- Repo header, same shape as the hub's own -->
<div class="p-4 border-b border-card-border flex items-start gap-3">
  <HubAvatar {author} source="hf" size="w-11 h-11" />
  <div class="min-w-0 flex-1">
    <div class="flex items-center gap-2 flex-wrap">
      <h3 class="font-mono text-sm text-txtmain truncate">{family.displayName}</h3>
      {#if family.packages[0]?.repo}
        <a
          class="text-txtsecondary hover:text-primary transition-colors"
          href={`https://huggingface.co/${family.packages[0].repo}`}
          target="_blank"
          rel="noreferrer"
          use:tip={"Open on Hugging Face"}
        >
          <ExternalLink class="w-3.5 h-3.5" />
        </a>
      {/if}
      {#if family.packages.some((p) => p.gated)}
        <span class="status status--starting"><Lock class="w-3 h-3" /> gated: accept the license first</span>
      {/if}
      {#if family.status && family.status !== "supported"}
        <!-- Upstream's own confidence in the family, passed through verbatim:
             "experimental" there means the same here. -->
        <span class="font-mono text-[0.6rem] uppercase tracking-wide text-warning">{family.status}</span>
      {/if}
    </div>
    <div class="text-[0.7rem] text-txtsecondary">{author}</div>
    <div class="mt-1.5 flex items-center gap-3 text-[0.65rem] text-txtsecondary tabular-nums">
      <span class="font-mono px-1.5 py-px rounded bg-secondary/70 text-txtmain">{family.task || family.category || "audio"}</span>
      <span>{family.family}</span>
      {#if family.languages?.length}
        <span class="truncate">{family.languages.join(", ")}</span>
      {/if}
    </div>
    {#if family.description}
      <p class="mt-2 text-xs text-txtsecondary">{family.description}</p>
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
</div>

<!-- File picker: the same data-table the repo page uses, minus the Estimate
     column. The planner behind it is LLM-shaped (layers, KV, expert share); a
     TTS or ASR gguf has none of that, so a verdict there would be a confident
     wrong number. -->
<div class="p-4 border-b border-card-border">
  {#if err}
    <div class="mb-3 text-xs text-danger">{err}</div>
  {/if}
  <table class="data-table -mx-2 w-[calc(100%+1rem)] text-xs">
    <thead>
      <tr class="rule text-txtsecondary text-left">
        <th class="text-micro font-medium uppercase tracking-wide pb-1.5 pl-2 pr-3">Build</th>
        <th class="text-micro font-medium uppercase tracking-wide pb-1.5 px-3 whitespace-nowrap">Size</th>
        <th class="pr-2"></th>
      </tr>
    </thead>
    <tbody>
      {#each family.packages as p (p.id)}
        {@const st = pkgState(p)}
        <tr class="rule hover:bg-secondary/30 transition-colors">
          <td class="py-2.5 pl-2 pr-3 align-top font-mono text-txtmain">
            <span class="break-all">{p.displayName}</span>
            {#if p.default}
              <span class="ml-1 rounded bg-secondary px-1 py-0.5 text-micro font-medium uppercase tracking-wide text-txtsecondary" use:tip={"Upstream's recommended build for this model"}>
                recommended
              </span>
            {/if}
            {#if p.files.length > 1}
              <!-- Not "parts" like a sharded gguf: these are a model and the
                   sidecars it cannot load without, downloaded as one job. -->
              <span class="text-[0.65rem] text-txtsecondary" use:tip={p.files.join("\n")}>· {p.files.length} files</span>
            {/if}
            {#if p.local}
              <span
                class="ml-1 inline-flex items-center gap-0.5 rounded bg-success/15 px-1 py-0.5 text-micro font-medium uppercase tracking-wide text-success"
                use:tip={"Already in your models folder, nothing to download"}
              >
                <Check class="w-2.5 h-2.5" /> downloaded
              </span>
            {/if}
          </td>
          <td class="py-2.5 px-3 align-top tabular-nums text-txtsecondary whitespace-nowrap">
            {p.sizeBytes ? humanBytes(p.sizeBytes) : "—"}
          </td>
          <td class="py-2.5 pl-3 pr-2 align-top text-right whitespace-nowrap">
            {#if st === "downloading"}
              <RefreshCw class="w-3.5 h-3.5 animate-spin text-txtsecondary inline-block" />
            {:else}
              <button
                class="icon-btn"
                disabled={st === "local"}
                use:tip={st === "local" ? "Already in your models folder" : `Download ${p.displayName}`}
                aria-label={`Download ${p.displayName}`}
                onclick={() => download(p)}
              >
                <Download class="w-3.5 h-3.5" />
              </button>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
