<script lang="ts">
  import { onMount } from "svelte";
  import { push } from "svelte-spa-router";
  import {
    models,
    inFlightRequests,
    metrics,
    loadModel,
    unloadSingleModel,
    loadCounts,
    getSettings,
    putSettings,
    putSlotCache,
    pickFolder,
    resetSettings,
    type AppSettings,
  } from "../stores/api";
  import { latestGpu, latestSys } from "../stores/perf";
  import { vramBreakdown } from "../stores/vram";
  import VramGauge from "../components/VramGauge.svelte";
  import { prettifyModelName } from "../lib/modelUtils";
  import type { Model } from "../lib/types";

  const liveModels = $derived(
    $models.filter((m) => m.state === "ready" || m.state === "starting" || m.state === "stopping"),
  );
  // Loadable = listed, currently stopped. Most-loaded models float to the top
  // (tie-break alphabetical) so frequent picks stay one click away. Cap the
  // list; full catalog lives on the Models page.
  const loadable = $derived(
    $models
      .filter((m) => !m.unlisted && m.state === "stopped")
      .sort((a, b) => ($loadCounts[b.id] ?? 0) - ($loadCounts[a.id] ?? 0) || a.id.localeCompare(b.id))
      .slice(0, 12),
  );

  const reqCount = $derived($metrics.length);
  const lastReq = $derived($metrics[0]);

  let busy = $state<Record<string, boolean>>({});
  async function load(m: Model): Promise<void> {
    busy = { ...busy, [m.id]: true };
    try {
      await loadModel(m.id);
    } finally {
      busy = { ...busy, [m.id]: false };
    }
  }
  async function unload(m: Model): Promise<void> {
    busy = { ...busy, [m.id]: true };
    try {
      await unloadSingleModel(m.id);
    } finally {
      busy = { ...busy, [m.id]: false };
    }
  }

  // --- GPU memory budget editor (settings.targetVramGB / headroom / RAM cap) ---
  let settings = $state<AppSettings | null>(null);
  let settingsAvailable = $state(true); // false when server lacks -generate (501)
  let tVram = $state(0);
  let tHead = $state(0);
  let tRam = $state(0);
  let savingSettings = $state(false);
  let settingsErr = $state<string | null>(null);

  function syncSettingsForm(s: AppSettings): void {
    tVram = s.targetVramGB;
    tHead = s.vramOverheadGB;
    tRam = s.maxRamGB;
    slotEnable = s.slotCache.enable;
    slotPath = s.slotCache.path;
    slotMinTokens = s.slotCache.minSaveTokens;
    slotMaxDiskGB = s.slotCache.maxDiskGB;
    slotMaxSessions = s.slotCache.maxSessions;
  }

  // --- Slot KV-cache persistence (settings.slotCache; global master switch +
  // shared knobs). Per-model opt-in lives in each model's config editor. ---
  let slotEnable = $state(false);
  let slotPath = $state("");
  let slotMinTokens = $state(0); // 0 => server default (30000)
  let slotMaxDiskGB = $state(0); // 0 => server default (10)
  let slotMaxSessions = $state(0); // 0 => server default (20)
  let savingSlot = $state(false);
  let slotErr = $state<string | null>(null);

  const slotDirty = $derived(
    !!settings &&
      (slotEnable !== settings.slotCache.enable ||
        slotPath !== settings.slotCache.path ||
        Number(slotMinTokens) !== settings.slotCache.minSaveTokens ||
        Number(slotMaxDiskGB) !== settings.slotCache.maxDiskGB ||
        Number(slotMaxSessions) !== settings.slotCache.maxSessions),
  );

  async function browseSlotDir(): Promise<void> {
    try {
      const picked = await pickFolder();
      if (picked) slotPath = picked;
    } catch (e) {
      slotErr = e instanceof Error ? e.message : String(e);
    }
  }

  async function saveSlotCache(): Promise<void> {
    savingSlot = true;
    slotErr = null;
    try {
      await putSlotCache({
        enable: slotEnable,
        path: slotPath.trim(),
        minSaveTokens: Number(slotMinTokens) || 0,
        maxDiskGB: Number(slotMaxDiskGB) || 0,
        maxSessions: Number(slotMaxSessions) || 0,
      });
      await loadSettings();
    } catch (e) {
      slotErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingSlot = false;
    }
  }

  async function loadSettings(): Promise<void> {
    try {
      const s = await getSettings();
      settings = s;
      syncSettingsForm(s);
    } catch (e) {
      // 501 => server started without -generate; hide the editor.
      settingsAvailable = false;
      console.warn("settings unavailable", e);
    }
  }

  // Physical ceilings from live telemetry — you can't budget more than the
  // hardware has. Rounded down to whole GB; 0 means "telemetry not in yet".
  const gpuMaxGb = $derived($latestGpu ? Math.floor($latestGpu.mem_total_mb / 1024) : 0);
  const sysMaxGb = $derived($latestSys ? Math.floor($latestSys.mem_total_mb / 1024) : 0);

  const settingsDirty = $derived(
    !!settings &&
      (tVram !== settings.targetVramGB || tHead !== settings.vramOverheadGB || tRam !== settings.maxRamGB),
  );

  // True when any field is above what the hardware physically has.
  const settingsOverCapacity = $derived(
    (gpuMaxGb > 0 && (tVram > gpuMaxGb || tHead > gpuMaxGb)) || (sysMaxGb > 0 && tRam > sysMaxGb),
  );

  function clampSettingsForm(): void {
    if (gpuMaxGb > 0) {
      if (tVram > gpuMaxGb) tVram = gpuMaxGb;
      if (tHead > gpuMaxGb) tHead = gpuMaxGb;
    }
    if (sysMaxGb > 0 && tRam > sysMaxGb) tRam = sysMaxGb;
  }

  async function saveSettings(): Promise<void> {
    clampSettingsForm();
    savingSettings = true;
    settingsErr = null;
    try {
      await putSettings({ targetVramGB: tVram, vramOverheadGB: tHead, maxRamGB: tRam });
      await loadSettings();
    } catch (e) {
      settingsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingSettings = false;
    }
  }

  async function resetSettingsToDefault(): Promise<void> {
    savingSettings = true;
    settingsErr = null;
    try {
      await resetSettings();
      await loadSettings();
    } catch (e) {
      settingsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingSettings = false;
    }
  }

  onMount(loadSettings);

  function stateBadge(state: string): string {
    if (state === "ready") return "status status--ready";
    if (state === "starting") return "status status--starting";
    if (state === "stopping") return "status status--stopping";
    return "status status--stopped";
  }
</script>

<div class="max-w-5xl mx-auto">
  {#snippet hint(text: string)}
    <span
      class="inline-flex items-center justify-center w-3.5 h-3.5 rounded-full border border-card-border text-txtsecondary text-[0.55rem] leading-none cursor-help align-middle"
      title={text}
      aria-label={text}>?</span>
  {/snippet}

  <h2 class="mb-4">Dashboard</h2>

  <div class="grid gap-4 md:grid-cols-2">
    <!-- VRAM -->
    <div class="card">
      <div class="flex items-center justify-between mb-3">
        <h6 class="!pb-0">GPU Memory</h6>
        <span class="font-mono text-xs text-txtsecondary truncate max-w-[12rem]" title={$latestGpu?.name}>
          {$latestGpu?.name ?? "—"}
        </span>
      </div>
      {#if $latestGpu}
        <VramGauge
          usedMb={$latestGpu.mem_used_mb}
          totalMb={$latestGpu.mem_total_mb}
          segments={$vramBreakdown?.segments}
          height="0.75rem"
        />
        <div class="mt-3 grid grid-cols-2 gap-2 font-mono text-xs">
          <div>
            <div class="text-txtsecondary uppercase tracking-wide">Util</div>
            <div class="text-txtmain tabular-nums">{$latestGpu.gpu_util_pct.toFixed(1)}%</div>
          </div>
          <div>
            <div class="text-txtsecondary uppercase tracking-wide">Temp</div>
            <div class="text-txtmain tabular-nums">{$latestGpu.temp_c}°C</div>
          </div>
        </div>
      {:else}
        <p class="font-mono text-xs text-txtsecondary">No GPU telemetry.</p>
      {/if}
    </div>

    <!-- Activity summary -->
    <div class="card">
      <h6 class="!pb-0 mb-3">Activity</h6>
      <div class="grid grid-cols-2 gap-3 font-mono">
        <div>
          <div class="text-2xl font-bold text-txtmain tabular-nums">{$inFlightRequests}</div>
          <div class="text-xs text-txtsecondary uppercase tracking-wide">In-flight</div>
        </div>
        <div>
          <div class="text-2xl font-bold text-txtmain tabular-nums">{reqCount}</div>
          <div class="text-xs text-txtsecondary uppercase tracking-wide">Requests seen</div>
        </div>
      </div>
      {#if lastReq}
        <div class="mt-3 pt-3 border-t border-card-border-inner font-mono text-xs text-txtsecondary">
          Last: <span class="text-txtmain">{lastReq.model}</span> · {lastReq.resp_status_code} ·
          {lastReq.duration_ms}ms
        </div>
      {/if}
      {#if $latestSys}
        <div class="mt-2 font-mono text-xs text-txtsecondary">
          System RAM <span class="text-txtmain tabular-nums"
            >{($latestSys.mem_used_mb / 1024).toFixed(1)}/{($latestSys.mem_total_mb / 1024).toFixed(1)} GB</span
          >
        </div>
      {/if}
    </div>
  </div>

  <!-- GPU memory budget -->
  {#if settingsAvailable}
    <div class="card mt-4">
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6 class="!pb-0">Memory budget</h6>
          {#if settings?.overridden}
            <span class="font-mono text-[0.6rem] uppercase tracking-wide text-primary border border-primary/40 rounded px-1.5 py-0.5">custom</span>
          {:else}
            <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">default</span>
          {/if}
          {#if settings?.autoVram}
            <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary" title="Live free-VRAM is sampled at startup; saving a target disables this.">auto-vram</span>
          {/if}
        </div>
        <button
          class="btn btn--sm uppercase tracking-wide"
          onclick={resetSettingsToDefault}
          disabled={savingSettings || !settings?.overridden}
          title="Revert to the generate file's values"
        >
          Reset
        </button>
      </div>

      <div class="grid grid-cols-3 gap-3 font-mono text-xs">
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Target VRAM (GB)
            {@render hint("Total GPU memory the loader may fill. The sizer chooses GPU layers / CPU-MoE offload so the model + KV cache fit under this. Higher = more on GPU = faster, until you risk OOM.")}
          </span>
          <input
            type="number" min="1" step="0.5" max={gpuMaxGb || undefined} bind:value={tVram} onblur={clampSettingsForm}
            onwheel={(e) => {
              if (document.activeElement !== e.currentTarget) return;
              e.preventDefault();
              tVram = Math.max(1, Math.round((tVram + (e.deltaY < 0 ? 0.5 : -0.5)) * 100) / 100);
              clampSettingsForm();
            }}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-[0.6rem] text-txtsecondary">default {settings?.defaults.targetVramGB}{gpuMaxGb ? ` · max ${gpuMaxGb}` : ""}</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Headroom (GB)
            {@render hint("Safety margin subtracted from Target VRAM before sizing — covers CUDA context, compute buffers and fragmentation, NOT current desktop/game usage (auto-vram already accounts for that at startup). Raise it if you hit OOM right after load.")}
          </span>
          <input
            type="number" min="0" step="0.25" max={gpuMaxGb || undefined} bind:value={tHead} onblur={clampSettingsForm}
            onwheel={(e) => {
              if (document.activeElement !== e.currentTarget) return;
              e.preventDefault();
              tHead = Math.max(0, Math.round((tHead + (e.deltaY < 0 ? 0.25 : -0.25)) * 100) / 100);
              clampSettingsForm();
            }}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-[0.6rem] text-txtsecondary">default {settings?.defaults.vramOverheadGB}</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Max RAM (GB)
            {@render hint("Ceiling on system RAM for CPU-offloaded MoE experts + any KV kept in RAM. A model whose plan needs more than this gets sized down (fewer experts offloaded).")}
          </span>
          <input
            type="number" min="1" step="1" max={sysMaxGb || undefined} bind:value={tRam} onblur={clampSettingsForm}
            onwheel={(e) => {
              if (document.activeElement !== e.currentTarget) return;
              e.preventDefault();
              tRam = Math.max(1, tRam + (e.deltaY < 0 ? 1 : -1));
              clampSettingsForm();
            }}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-[0.6rem] text-txtsecondary">default {settings?.defaults.maxRamGB}{sysMaxGb ? ` · max ${sysMaxGb}` : ""}</span>
        </label>
      </div>

      {#if settingsOverCapacity}
        <p class="mt-2 font-mono text-[0.65rem] text-warning">⚠ Value exceeds installed hardware — will be clamped on save.</p>
      {/if}

      <div class="mt-3 flex items-center gap-3">
        <button
          class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
          onclick={saveSettings}
          disabled={savingSettings || !settingsDirty}
        >
          {savingSettings ? "Saving…" : "Save & reload"}
        </button>
        <span class="font-mono text-[0.65rem] text-txtsecondary">Saving regenerates the config and hot-reloads.</span>
        {#if settingsErr}
          <span class="font-mono text-[0.65rem] text-error">{settingsErr}</span>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Slot KV-cache persistence -->
  {#if settingsAvailable}
    <div class="card mt-4">
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6 class="!pb-0">KV-cache disk save</h6>
          <span class="status bg-warning/10 text-warning text-[0.6rem] !px-1.5 !py-0.5">Experimental</span>
          {@render hint("Persist a long conversation's KV cache to disk so it survives being evicted from the live slot, and is restored instead of reprocessed. Master switch: each model opts in via its config editor (\"Save KV cache to disk\").\n\nExperimental: reliable for standard transformer models. Hybrid/recurrent models (e.g. Qwen3.5/3.6 GatedDeltaNet) don't yet restore across a process swap — pending upstream llama.cpp (#20819); they still get warm same-process reuse.")}
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={slotEnable} />
          <span class="text-txtsecondary uppercase tracking-wide font-mono text-[0.65rem]">Enable</span>
        </label>
      </div>

      <div class="grid grid-cols-2 gap-3 font-mono text-xs">
        <label class="flex flex-col gap-1 col-span-2">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Directory
            {@render hint("Folder for the .bin KV snapshots (also passed to llama-server as --slot-save-path). Defaults to a .cache folder next to the quartermaster binary.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={slotPath} placeholder="(.cache next to quartermaster)" disabled={!slotEnable}
              class="flex-1 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
            />
            <button
              type="button"
              class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary disabled:opacity-50"
              onclick={browseSlotDir}
              disabled={!slotEnable}
            >
              Browse…
            </button>
          </div>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Min context to save
            {@render hint("Only persist a conversation whose live KV is at least this many tokens — cheap chats aren't worth the disk write.")}
          </span>
          <input
            type="number" min="0" step="1000" bind:value={slotMinTokens} disabled={!slotEnable}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-[0.6rem] text-txtsecondary">tokens · default 30000</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Max disk size
            {@render hint("Total budget for saved snapshots. Oldest are evicted (LRU) once this or the session cap is exceeded.")}
          </span>
          <input
            type="number" min="0" step="1" bind:value={slotMaxDiskGB} disabled={!slotEnable}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-[0.6rem] text-txtsecondary">GB · default 10</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Max sessions
            {@render hint("Cap on the number of saved conversations. Oldest evicted (LRU) past this.")}
          </span>
          <input
            type="number" min="0" step="1" bind:value={slotMaxSessions} disabled={!slotEnable}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-[0.6rem] text-txtsecondary">files · default 20</span>
        </label>
      </div>

      <div class="mt-3 flex items-center gap-3">
        <button
          class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
          onclick={saveSlotCache}
          disabled={savingSlot || !slotDirty}
        >
          {savingSlot ? "Saving…" : "Save & reload"}
        </button>
        <span class="font-mono text-[0.65rem] text-txtsecondary">Saving regenerates the config and hot-reloads.</span>
        {#if slotErr}
          <span class="font-mono text-[0.65rem] text-error">{slotErr}</span>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Loaded -->
  <div class="card mt-4">
    <div class="flex items-center justify-between mb-3">
      <h6 class="!pb-0">Loaded</h6>
      <button class="btn btn--sm uppercase tracking-wide" onclick={() => push("/models")}>All models →</button>
    </div>
    {#if liveModels.length === 0}
      <p class="font-mono text-xs text-txtsecondary">Nothing on the GPU. Load a model below.</p>
    {:else}
      <ul class="divide-y divide-card-border-inner">
        {#each liveModels as m (m.id)}
          <li class="flex items-center gap-3 py-2">
            <span class={stateBadge(m.state)}>{m.state}</span>
            <span class="font-mono text-sm text-txtmain truncate" title={m.id}>{m.id}</span>
            <button
              class="ml-auto btn btn--sm uppercase tracking-wide hover:border-error hover:text-error"
              onclick={() => unload(m)}
              disabled={busy[m.id]}>Unload</button
            >
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <!-- Quick load -->
  {#if loadable.length > 0}
    <div class="card mt-4">
      <h6 class="!pb-0 mb-3">Quick load</h6>
      <div class="flex flex-wrap gap-2">
        {#each loadable as m (m.id)}
          <button
            class="flex items-center gap-2 rounded-md border border-card-border bg-surface px-3 py-1.5 font-mono text-sm text-txtmain shadow-sm transition-colors hover:border-primary hover:text-primary hover:bg-secondary/40 disabled:opacity-60 max-w-[18rem]"
            onclick={() => load(m)}
            disabled={busy[m.id]}
            title={m.id}
          >
            <span class="w-1.5 h-1.5 rounded-full bg-txtsecondary shrink-0"></span>
            <span class="truncate">{busy[m.id] ? "Loading…" : prettifyModelName(m.name || m.id)}</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
</div>
