<script lang="ts">
  import { onMount } from "svelte";
  import { SlidersHorizontal, HardDrive, Cpu } from "lucide-svelte";
  import { getSettings, putSettings, putSlotCache, putBackends, pickFolder, pickBackend, resetSettings, type AppSettings } from "../stores/api";
  import { latestGpu, latestSys } from "../stores/perf";

  // Category side-nav — mirrors the playground settings modal's pattern.
  type SettingsCat = "general" | "kvcache" | "backends";
  let cat = $state<SettingsCat>("general");
  const cats: { id: SettingsCat; label: string; icon: typeof SlidersHorizontal }[] = [
    { id: "general", label: "Memory & Eviction", icon: SlidersHorizontal },
    { id: "kvcache", label: "KV Cache", icon: HardDrive },
    { id: "backends", label: "Backends", icon: Cpu },
  ];

  // --- Global settings (VRAM budget + idle eviction + slot KV) ---
  let settings = $state<AppSettings | null>(null);
  let settingsAvailable = $state(true); // false when server lacks -generate (501)
  let tVram = $state(0);
  let tHead = $state(0);
  let tRam = $state(0);
  let tTtl = $state(0); // idle-eviction seconds; 0 = never auto-unload
  let savingSettings = $state(false);
  let settingsErr = $state<string | null>(null);

  function syncSettingsForm(s: AppSettings): void {
    tVram = s.targetVramGB;
    tHead = s.vramOverheadGB;
    tRam = s.maxRamGB;
    tTtl = s.ttlSec;
    slotEnable = s.slotCache.enable;
    slotPath = s.slotCache.path;
    slotMinTokens = s.slotCache.minSaveTokens;
    slotMaxDiskGB = s.slotCache.maxDiskGB;
    slotMaxSessions = s.slotCache.maxSessions;
    beServer = s.backends.serverExe;
    beSd = s.backends.sdServerExe;
    beTts = s.backends.ttsServerExe;
  }

  // --- Backend executables (llama-server / sd-server / tts-server) ---
  // Point these at a Vulkan/ROCm build on AMD/Intel GPUs. Blank => the generate
  // file / sibling default.
  let beServer = $state("");
  let beSd = $state("");
  let beTts = $state("");
  let savingBackends = $state(false);
  let backendsErr = $state<string | null>(null);
  let backendsSaved = $state(false); // brief "Saved" flash after a successful write

  // Persist the current three paths. Called after a native pick and on manual
  // field blur (autosave — no explicit Save button). No-ops if nothing changed.
  async function saveBackendsNow(): Promise<void> {
    if (!settings) return;
    const next = { serverExe: beServer.trim(), sdServerExe: beSd.trim(), ttsServerExe: beTts.trim() };
    if (
      next.serverExe === settings.backends.serverExe &&
      next.sdServerExe === settings.backends.sdServerExe &&
      next.ttsServerExe === settings.backends.ttsServerExe
    )
      return;
    savingBackends = true;
    backendsErr = null;
    backendsSaved = false;
    try {
      await putBackends(next);
      await loadSettings();
      backendsSaved = true;
    } catch (e) {
      backendsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingBackends = false;
    }
  }

  // Open the host's native file dialog for one backend, then autosave.
  async function browseBackend(which: "server" | "sd" | "tts"): Promise<void> {
    backendsErr = null;
    try {
      const picked = await pickBackend();
      if (!picked) return; // cancelled / unsupported — keep the text field
      if (which === "server") beServer = picked;
      else if (which === "sd") beSd = picked;
      else beTts = picked;
      await saveBackendsNow();
    } catch (e) {
      backendsErr = e instanceof Error ? e.message : String(e);
    }
  }

  // --- Slot KV-cache persistence (global master switch + shared knobs) ---
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
      settingsAvailable = false; // 501 => server without -generate
      console.warn("settings unavailable", e);
    }
  }

  // Physical ceilings from live telemetry. 0 means "telemetry not in yet".
  const gpuMaxGb = $derived($latestGpu ? Math.floor($latestGpu.mem_total_mb / 1024) : 0);
  const sysMaxGb = $derived($latestSys ? Math.floor($latestSys.mem_total_mb / 1024) : 0);

  const settingsDirty = $derived(
    !!settings &&
      (tVram !== settings.targetVramGB ||
        tHead !== settings.vramOverheadGB ||
        tRam !== settings.maxRamGB ||
        Number(tTtl) !== settings.ttlSec),
  );

  const settingsOverCapacity = $derived(
    (gpuMaxGb > 0 && (tVram > gpuMaxGb || tHead > gpuMaxGb)) || (sysMaxGb > 0 && tRam > sysMaxGb),
  );

  function clampSettingsForm(): void {
    if (gpuMaxGb > 0) {
      if (tVram > gpuMaxGb) tVram = gpuMaxGb;
      if (tHead > gpuMaxGb) tHead = gpuMaxGb;
    }
    if (sysMaxGb > 0 && tRam > sysMaxGb) tRam = sysMaxGb;
    if (tTtl < 0) tTtl = 0;
  }

  async function saveSettings(): Promise<void> {
    clampSettingsForm();
    savingSettings = true;
    settingsErr = null;
    try {
      await putSettings({ targetVramGB: tVram, vramOverheadGB: tHead, maxRamGB: tRam, ttlSec: Number(tTtl) || 0 });
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

  // Friendly readout for the idle-eviction seconds field.
  const ttlHuman = $derived(
    Number(tTtl) <= 0 ? "never auto-unload" : Number(tTtl) % 60 === 0 ? `${Number(tTtl) / 60} min` : `${tTtl}s`,
  );

  onMount(loadSettings);
</script>

<div class="flex flex-1 min-h-0">
  {#snippet hint(text: string)}
    <span
      class="inline-flex items-center justify-center w-3.5 h-3.5 rounded-full border border-card-border text-txtsecondary text-[0.55rem] leading-none cursor-help align-middle"
      title={text}
      aria-label={text}>?</span>
  {/snippet}

  {#if !settingsAvailable}
    <div class="flex-1 p-5">
      <div class="card">
        <p class="font-mono text-xs text-txtsecondary">
          Settings editing requires the server to run with <span class="text-txtmain">-generate</span>.
        </p>
      </div>
    </div>
  {:else}
    <!-- Category side-nav -->
    <nav class="shrink-0 w-44 flex flex-col gap-0.5 py-3 border-r border-card-border bg-background/40">
      {#each cats as c (c.id)}
        {@const active = cat === c.id}
        <button
          onclick={() => (cat = c.id)}
          class="w-full flex items-center gap-2.5 px-3 py-2 border-l-2 text-left transition-colors {active
            ? 'border-primary text-txtmain bg-secondary/60'
            : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
        >
          <c.icon size={15} class="shrink-0" />
          <span class="text-[0.8125rem]">{c.label}</span>
        </button>
      {/each}
    </nav>

    <div class="flex-1 min-w-0 overflow-y-auto pretty-scroll p-4">
    {#if cat === "general"}
    <!-- Memory budget + idle eviction -->
    <div>
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

      <div class="grid grid-cols-2 md:grid-cols-4 gap-3 font-mono text-xs">
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
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Idle unload (s)
            {@render hint("Idle-eviction timeout baked into every model's ttl: a model with no requests for this many seconds is unloaded to free VRAM. 0 = never auto-unload. Saving regenerates the config and applies on each model's next load.")}
          </span>
          <input
            type="number" min="0" step="30" bind:value={tTtl} onblur={clampSettingsForm}
            onwheel={(e) => {
              if (document.activeElement !== e.currentTarget) return;
              e.preventDefault();
              tTtl = Math.max(0, Number(tTtl) + (e.deltaY < 0 ? 30 : -30));
            }}
            class="w-full rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-[0.6rem] text-txtsecondary">{ttlHuman} · default {settings?.defaults.ttlSec}</span>
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
    {:else if cat === "kvcache"}
    <!-- Slot KV-cache persistence -->
    <div>
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
            {@render hint("Folder for the .bin KV snapshots (also passed to llama-server as --slot-save-path). Defaults to a .cache folder next to the Quartermaster binary.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={slotPath} placeholder="(.cache next to Quartermaster)" disabled={!slotEnable}
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
    {:else}
    <!-- Backend executables -->
    <div>
      <div class="flex items-center gap-2 mb-3">
        <h6 class="!pb-0">Backend executables</h6>
        {@render hint("Paths to the inference server binaries Quartermaster spawns. On AMD/Intel GPUs point llama-server at a Vulkan (or ROCm/HIP) build — a CUDA build silently falls back to CPU. Leave blank to use the default (bare name on PATH, or a sibling of llama-server for sd/tts).")}
      </div>

      <div class="grid grid-cols-1 gap-3 font-mono text-xs">
        <div class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            llama-server (text/LLM)
            {@render hint("stable llama.cpp server for GGUF LLMs. Use a Vulkan or ROCm build on non-NVIDIA GPUs.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={beServer} placeholder="llama-server (on PATH)" onblur={saveBackendsNow}
              class="flex-1 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
            <button type="button" class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary" onclick={() => browseBackend("server")}>Browse…</button>
          </div>
        </div>
        <div class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            sd-server (image)
            {@render hint("stable-diffusion.cpp server for image models. Blank => a sibling 'sd-server' of the llama-server path.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={beSd} placeholder="(sibling of llama-server)" onblur={saveBackendsNow}
              class="flex-1 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
            <button type="button" class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary" onclick={() => browseBackend("sd")}>Browse…</button>
          </div>
        </div>
        <div class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            tts-server (speech)
            {@render hint("qwentts.cpp server for TTS models. Blank => a sibling 'tts-server' of the llama-server path.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={beTts} placeholder="(sibling of llama-server)" onblur={saveBackendsNow}
              class="flex-1 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
            <button type="button" class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary" onclick={() => browseBackend("tts")}>Browse…</button>
          </div>
        </div>
      </div>

      <div class="mt-3 flex items-center gap-3">
        <span class="font-mono text-[0.65rem] text-txtsecondary">
          {savingBackends ? "Saving…" : backendsSaved ? "Saved — config regenerated; new paths apply on each model's next load." : "Autosaves on pick or when you leave a field; regenerates the config and hot-reloads."}
        </span>
        {#if backendsErr}
          <span class="font-mono text-[0.65rem] text-error">{backendsErr}</span>
        {/if}
      </div>
    </div>
    {/if}
    </div>
  {/if}
</div>
