<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { onMount } from "svelte";
  import { SlidersHorizontal, HardDrive, Cpu, FolderOpen, Trash2, Star, Plus, Power, HelpCircle, Palette } from "lucide-svelte";
  import { getSettings, putSettings, putSlotCache, putBackends, pickFolder, pickBackend, resetSettings, getAutostart, putAutostart, type AppSettings, type BackendEntry, type AutostartStatus } from "../stores/api";
  import { BACKEND_CLASSES, backendClass, type BackendClassDef } from "../lib/backends";
  import ManagedBackends from "../components/ManagedBackends.svelte";
  import SoftwareUpdate from "../components/SoftwareUpdate.svelte";
  import Select from "../components/Select.svelte";
  import Toggle from "../components/Toggle.svelte";
  import UIScaleControl from "../components/UIScaleControl.svelte";
  import { themeMode, type ThemeMode } from "../stores/theme";
  import { latestGpu, latestSys } from "../stores/perf";

  // Category side-nav — mirrors the playground settings modal's pattern.
  type SettingsCat = "appearance" | "general" | "kvcache" | "backends" | "system";
  let cat = $state<SettingsCat>("general");
  const cats: { id: SettingsCat; label: string; icon: typeof SlidersHorizontal }[] = [
    { id: "appearance", label: "Appearance", icon: Palette },
    { id: "general", label: "Memory & Eviction", icon: SlidersHorizontal },
    { id: "kvcache", label: "KV Cache", icon: HardDrive },
    { id: "backends", label: "Backends", icon: Cpu },
    { id: "system", label: "System", icon: Power },
  ];

  // Appearance is client-only (localStorage), so it works even when the server
  // was started without -generate and every other category is unavailable.
  const themeOptions = [
    { value: "system", label: "System", detail: "Follow the OS light/dark setting" },
    { value: "light", label: "Light", detail: "" },
    { value: "dark", label: "Dark", detail: "" },
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
    backends = s.backendList.map((b) => ({ ...b }));
  }

  // --- Backend registry (llama-server / sd-server / tts-server / vllm / …) ---
  // A list of backends the loader can spawn, grouped by the model class each
  // serves (see lib/backends.ts — llama.cpp and vLLM are both "llm" engines).
  // Point a llama/sd/tts row at a Vulkan/ROCm build on AMD/Intel GPUs. The ★
  // entry of a class is the auto-pick; per-model overrides live in the model
  // config editor.
  let backends = $state<BackendEntry[]>([]);

  // Rows bucketed by class, in BACKEND_CLASSES order. Carries each row's index
  // in `backends` so the mutators stay index-based (ids are client-only).
  const backendGroups = $derived(
    BACKEND_CLASSES.map((cls) => ({
      cls,
      rows: backends.map((be, i) => ({ be, i })).filter(({ be }) => backendClass(be.kind) === cls.id),
    })),
  );
  let savingBackends = $state(false);
  let backendsErr = $state<string | null>(null);
  let backendsSaved = $state(false); // brief "Saved" flash after a successful write

  // ponytail: cheap id, only needs to be stable within this list. crypto.randomUUID
  // is on every browser we target.
  function newBackendId(): string {
    return crypto.randomUUID();
  }

  // Add an empty row to one class, seeded with that class's first engine. Not
  // saved until it has a path (saveBackendsNow drops pathless rows).
  function addBackend(cls: BackendClassDef): void {
    backends = [...backends, { id: newBackendId(), kind: cls.engines[0].kind, name: "", path: "", default: false }];
    backendsSaved = false;
  }

  // Mark row i the auto-pick for its class, clearing the flag on its classmates.
  async function setDefaultBackend(i: number): Promise<void> {
    const cls = backendClass(backends[i].kind);
    backends = backends.map((b, j) => (backendClass(b.kind) === cls ? { ...b, default: j === i } : b));
    await saveBackendsNow();
  }

  async function removeBackend(i: number): Promise<void> {
    backends = backends.filter((_, j) => j !== i);
    await saveBackendsNow();
  }

  // Persist the whole registry. Called on add/remove, native pick, and field
  // blur (autosave — no explicit Save button). No-ops if nothing changed.
  async function saveBackendsNow(): Promise<void> {
    if (!settings) return;
    // A pathless row can't be launched — the server drops it (UpsertSidecarBackendList).
    // Persist only complete rows; keep pathless ones as in-progress editor rows so
    // blurring the name field (before the path is typed) doesn't wipe the new row.
    const next = backends.filter((b) => b.path.trim()).map((b) => ({ ...b, name: b.name.trim(), path: b.path.trim() }));
    if (JSON.stringify(next) === JSON.stringify(settings.backendList)) return;
    savingBackends = true;
    backendsErr = null;
    backendsSaved = false;
    try {
      const inProgress = backends.filter((b) => !b.path.trim());
      await putBackends(next);
      await loadSettings();
      if (inProgress.length) backends = [...backends, ...inProgress];
      backendsSaved = true;
    } catch (e) {
      backendsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingBackends = false;
    }
  }

  // Open the host's native file dialog for one backend row, then autosave.
  async function browseBackend(i: number): Promise<void> {
    backendsErr = null;
    try {
      const picked = await pickBackend();
      if (!picked) return; // cancelled / unsupported — keep the text field
      backends[i].path = picked;
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

  let slotSaved = $state(false); // brief "Saved" flash after a successful write
  let slotFlashTimer: ReturnType<typeof setTimeout> | undefined;

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
      slotSaved = true;
      clearTimeout(slotFlashTimer);
      slotFlashTimer = setTimeout(() => (slotSaved = false), 2500);
    } catch (e) {
      slotErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingSlot = false;
    }
  }

  // Autosave, same debounce as the memory knobs (each write regenerates + reloads).
  let slotAutosaveTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!slotDirty || savingSlot) return;
    clearTimeout(slotAutosaveTimer);
    slotAutosaveTimer = setTimeout(saveSlotCache, 900);
    return () => clearTimeout(slotAutosaveTimer);
  });

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

  let settingsSaved = $state(false); // brief "Saved" flash after a successful write
  let savedFlashTimer: ReturnType<typeof setTimeout> | undefined;

  async function saveSettings(): Promise<void> {
    clampSettingsForm();
    savingSettings = true;
    settingsErr = null;
    try {
      await putSettings({ targetVramGB: tVram, vramOverheadGB: tHead, maxRamGB: tRam, ttlSec: Number(tTtl) || 0 });
      await loadSettings();
      settingsSaved = true;
      clearTimeout(savedFlashTimer);
      savedFlashTimer = setTimeout(() => (settingsSaved = false), 2500);
    } catch (e) {
      settingsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingSettings = false;
    }
  }

  // Autosave: debounce edits to the memory/eviction fields, no Save button. The
  // write regenerates the config + hot-reloads, so it must not fire per keystroke.
  let autosaveTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!settingsDirty || savingSettings) return;
    // touch the fields so the effect re-runs on each edit
    void tVram; void tHead; void tRam; void tTtl;
    clearTimeout(autosaveTimer);
    autosaveTimer = setTimeout(saveSettings, 900);
    return () => clearTimeout(autosaveTimer);
  });

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

  // --- Start with the system (Windows Run key, shared across installs) ---
  let autostart = $state<AutostartStatus | null>(null);
  let autostartBusy = $state(false);
  let autostartErr = $state<string | null>(null);

  async function loadAutostart(): Promise<void> {
    try {
      autostart = await getAutostart();
    } catch (e) {
      console.warn("autostart unavailable", e);
    }
  }

  async function toggleAutostart(enabled: boolean, takeover = false): Promise<void> {
    autostartBusy = true;
    autostartErr = null;
    try {
      const st = await putAutostart(enabled, takeover);
      // A 409 returns the unchanged status (enabled by a foreign exe), which
      // the markup renders as the take-over prompt.
      autostart = st;
    } catch (e) {
      autostartErr = e instanceof Error ? e.message : String(e);
    } finally {
      autostartBusy = false;
    }
  }

  // Friendly readout for the idle-eviction seconds field.
  const ttlHuman = $derived(
    Number(tTtl) <= 0 ? "never auto-unload" : Number(tTtl) % 60 === 0 ? `${Number(tTtl) / 60} min` : `${tTtl}s`,
  );

  onMount(() => {
    loadSettings();
    loadAutostart();
  });
</script>

<div class="flex flex-1 min-h-0">
  {#snippet hint(text: string)}
    <!-- lucide glyph rather than a hand-drawn "?" bubble: the old one needed a
         sub-11px font to fit its circle, which no longer exists in the scale. -->
    <span
      class="inline-flex shrink-0 align-middle text-txtsecondary cursor-help hover:text-txtmain"
      use:tip={text}
      aria-label={text}><HelpCircle size={12} /></span>
  {/snippet}

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
    {#if cat === "appearance"}
    <!-- Client-only: theme + interface size both live in localStorage, so this
         panel works with or without -generate. -->
    <div>
      <div class="flex items-center gap-2 mb-3">
        <h6 >Appearance</h6>
        {@render hint("How the dashboard is drawn. Both settings are stored in this browser (or app window) only - they are not part of the server config.")}
      </div>

      <div class="rounded border border-card-border bg-surface p-3 flex flex-col gap-3">
        <div class="flex items-center justify-between gap-4">
          <span class="min-w-0">
            <span class="block text-sm text-txtmain">Theme</span>
            <span class="block mt-0.5 text-micro text-txtsecondary">
              Light, dark, or whatever the operating system is set to.
            </span>
          </span>
          <Select
            value={$themeMode}
            onchange={(v) => themeMode.set(v as ThemeMode)}
            options={themeOptions}
            ariaLabel="Theme"
            class="w-36 shrink-0"
          />
        </div>

        <div class="flex items-center justify-between gap-4 border-t border-card-border pt-3">
          <span class="min-w-0">
            <span class="block text-sm text-txtmain">Interface size</span>
            <span class="block mt-0.5 text-micro text-txtsecondary">
              Scales the whole UI. Ctrl+Plus / Ctrl+Minus / Ctrl+0 do the same thing anywhere.
            </span>
          </span>
          <UIScaleControl />
        </div>
      </div>
    </div>

    {:else if !settingsAvailable}
    <div class="card">
      <p class="text-label text-txtsecondary">
        Settings editing requires the server to run with <span class="text-txtmain">-generate</span>.
      </p>
    </div>

    {:else if cat === "general"}
    <!-- Memory budget + idle eviction -->
    <div>
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6 >Memory budget</h6>
          {#if settings?.overridden}
            <span class="text-micro font-medium uppercase tracking-wide text-primary border border-primary/40 rounded px-1.5 py-0.5">custom</span>
          {:else}
            <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">default</span>
          {/if}
          {#if settings?.autoVram}
            <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary" use:tip={"Live free-VRAM is sampled at startup; saving a target disables this."}>auto-vram</span>
          {/if}
        </div>
        <button
          class="btn btn--sm uppercase tracking-wide"
          onclick={resetSettingsToDefault}
          disabled={savingSettings || !settings?.overridden}
          use:tip={"Revert to the generate file's values"}
        >
          Reset
        </button>
      </div>

      <div class="grid grid-cols-2 md:grid-cols-4 gap-3 text-label">
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
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">default {settings?.defaults.targetVramGB}{gpuMaxGb ? ` · max ${gpuMaxGb}` : ""}</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Headroom (GB)
            {@render hint("Safety margin subtracted from Target VRAM before sizing - covers CUDA context, compute buffers and fragmentation, NOT current desktop/game usage (auto-vram already accounts for that at startup). Raise it if you hit OOM right after load.")}
          </span>
          <input
            type="number" min="0" step="0.25" max={gpuMaxGb || undefined} bind:value={tHead} onblur={clampSettingsForm}
            onwheel={(e) => {
              if (document.activeElement !== e.currentTarget) return;
              e.preventDefault();
              tHead = Math.max(0, Math.round((tHead + (e.deltaY < 0 ? 0.25 : -0.25)) * 100) / 100);
              clampSettingsForm();
            }}
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">default {settings?.defaults.vramOverheadGB}</span>
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
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">default {settings?.defaults.maxRamGB}{sysMaxGb ? ` · max ${sysMaxGb}` : ""}</span>
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
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">{ttlHuman} · default {settings?.defaults.ttlSec}</span>
        </label>
      </div>

      {#if settingsOverCapacity}
        <p class="mt-2 text-micro text-warning">⚠ Value exceeds installed hardware - will be clamped on save.</p>
      {/if}

      <div class="mt-3 flex items-center justify-between gap-3">
        <span class="text-micro text-txtsecondary">Changes save automatically, regenerate the config and hot-reload.</span>
        <span class="text-micro">
          {#if settingsErr}
            <span class="text-error">{settingsErr}</span>
          {:else if savingSettings}
            <span class="text-txtsecondary">Saving…</span>
          {:else if settingsSaved}
            <span class="text-primary">Saved!</span>
          {/if}
        </span>
      </div>
    </div>

    {:else if cat === "system"}
    <!-- The app itself first: which build is running, and how it gets newer. -->
    <SoftwareUpdate />

    <!-- Then how it starts (Windows only) -->
    {#if autostart?.supported}
      <div>
        <div class="flex items-center gap-2 mb-3">
          <h6 >Startup</h6>
          {@render hint("Launch Quartermaster in the system tray when you log in to Windows. All Quartermaster installs on this machine share ONE startup entry, so only one can start with the system - if another install owns it, take it over from here.")}
        </div>

        <div class="rounded border border-card-border bg-surface p-3">
          <label class="flex items-start justify-between gap-4 cursor-pointer">
            <span class="min-w-0">
              <span class="block text-sm text-txtmain">Start with system</span>
              <span class="block mt-0.5 text-micro text-txtsecondary">
                Launch minimized to the tray when you sign in to Windows.
              </span>
            </span>
            <Toggle
              class="mt-0.5"
              disabled={autostartBusy}
              checked={autostart.enabled && autostart.ownedByUs}
              onchange={(on) => toggleAutostart(on)}
            />
          </label>

          {#if autostart.enabled && !autostart.ownedByUs}
            <div class="mt-3 rounded border border-warning/40 bg-warning/10 p-2 text-micro">
              <div class="flex items-center justify-between gap-2">
                <span class="text-warning">⚠ Owned by another Quartermaster install</span>
                <button
                  class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
                  disabled={autostartBusy}
                  onclick={() => toggleAutostart(true, true)}
                >
                  {autostartBusy ? "Working…" : "Take over"}
                </button>
              </div>
              <p class="mt-1.5 truncate font-mono text-txtsecondary" use:tip={autostart.ownerExe}>
                {autostart.ownerExe}
              </p>
            </div>
          {/if}

          {#if autostartErr}
            <p class="mt-2 text-micro text-error">{autostartErr}</p>
          {/if}
        </div>
      </div>
    {:else}
      <p class="text-label text-txtsecondary">Starting with the system is a Windows-only option.</p>
    {/if}
    {:else if cat === "kvcache"}
    <!-- Slot KV-cache persistence -->
    <div>
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6 >KV-cache disk save</h6>
          <span class="status bg-warning/10 text-warning text-micro !px-1.5 !py-0.5">Experimental</span>
          {@render hint("Persist a long conversation's KV cache to disk so it survives being evicted from the live slot, and is restored instead of reprocessed. Master switch: each model opts in via its config editor (\"Save KV cache to disk\").\n\nExperimental: exact save/restore of a conversation works on both standard transformers and hybrid/recurrent models (e.g. Qwen3.5/3.6 GatedDeltaNet) - a restored session continues by appending, reusing ~100% of its tokens. What hybrids can't do is partial-prefix seeding (preamble mint / best-seed reuse): a recurrent state can be continued forward but never rewound or trimmed, so those paths are skipped automatically on recurrent models (slotCache.recurrentSeeds re-enables them for testing).")}
        </div>
        <label class="flex items-center gap-2 text-sm">
          <Toggle size="sm" bind:checked={slotEnable} />
          <span class="text-txtsecondary text-micro font-medium uppercase tracking-wide">Enable</span>
        </label>
      </div>

      <div class="grid grid-cols-2 gap-3 text-label">
        <label class="flex flex-col gap-1 col-span-2">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Directory
            {@render hint("Folder for the .bin KV snapshots (also passed to llama-server as --slot-save-path). Defaults to a .cache folder next to the Quartermaster binary.")}
          </span>
          <div class="flex gap-2">
            <input
              type="text" bind:value={slotPath} placeholder="(.cache next to Quartermaster)" disabled={!slotEnable}
              class="flex-1 font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
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
            {@render hint("Only persist a conversation whose live KV is at least this many tokens - cheap chats aren't worth the disk write.")}
          </span>
          <input
            type="number" min="0" step="1000" bind:value={slotMinTokens} disabled={!slotEnable}
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-micro text-txtsecondary">tokens · default 30000</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Max disk size
            {@render hint("Total budget for saved snapshots. Oldest are evicted (LRU) once this or the session cap is exceeded.")}
          </span>
          <input
            type="number" min="0" step="1" bind:value={slotMaxDiskGB} disabled={!slotEnable}
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-micro text-txtsecondary">GB · default 10</span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Max sessions
            {@render hint("Cap on the number of saved conversations. Oldest evicted (LRU) past this.")}
          </span>
          <input
            type="number" min="0" step="1" bind:value={slotMaxSessions} disabled={!slotEnable}
            class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
          />
          <span class="text-micro text-txtsecondary">files · default 20</span>
        </label>
      </div>

      <div class="mt-3 flex items-center justify-between gap-3">
        <span class="text-micro text-txtsecondary">Changes save automatically, regenerate the config and hot-reload.</span>
        <span class="text-micro">
          {#if slotErr}
            <span class="text-error">{slotErr}</span>
          {:else if savingSlot}
            <span class="text-txtsecondary">Saving…</span>
          {:else if slotSaved}
            <span class="text-primary">Saved!</span>
          {/if}
        </span>
      </div>
    </div>
    {:else}
    <!-- Managed installs first, then the hand-entered registry they write into -->
    <div>
      <ManagedBackends onchanged={loadSettings} />

      <div class="flex items-baseline gap-2 mb-1">
        <h6 >Backends</h6>
        {@render hint("Inference server binaries Quartermaster can spawn, grouped by the kind of model they serve. On AMD/Intel GPUs point a row at a Vulkan (or ROCm/HIP) build - a CUDA build silently falls back to CPU. The ★ entry of a group is the auto-pick; a model can be pinned to any other entry of its group from its config editor.")}
      </div>
      <p class="text-[0.7rem] text-txtsecondary mb-4">
        Blank groups fall back to the built-in defaults (llama-server on PATH, sd/tts as siblings).
      </p>

      <div class="flex flex-col gap-3">
        {#each backendGroups as g (g.cls.id)}
          <section class="rounded-md border border-card-border bg-surface/40">
            <header class="flex items-center gap-2 px-3 py-2 border-b border-card-border">
              <span class="text-[0.8125rem] text-txtmain">{g.cls.label}</span>
              <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary border border-card-border rounded px-1.5 py-0.5">{g.cls.id}</span>
              <span class="text-[0.7rem] text-txtsecondary truncate">{g.cls.blurb}</span>
              <button
                type="button"
                class="btn btn--sm ml-auto shrink-0 inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary"
                onclick={() => addBackend(g.cls)}
              ><Plus size={12} /> Add</button>
            </header>

            {#if g.rows.length === 0}
              <p class="px-3 py-2.5 text-[0.7rem] text-txtsecondary">None registered.</p>
            {:else}
              <div class="flex flex-col divide-y divide-card-border">
                {#each g.rows as { be, i } (be.id)}
                  <div class="flex gap-2 items-center px-3 py-2 font-mono text-xs">
                    <button
                      type="button"
                      class="shrink-0 p-1 rounded transition-colors {be.default ? 'text-primary' : 'text-txtsecondary hover:text-txtmain'}"
                      use:tip={be.default ? `Default ${g.cls.label.toLowerCase()} backend` : "Make the default for this group"}
                      aria-pressed={be.default}
                      onclick={() => setDefaultBackend(i)}
                    ><Star size={14} fill={be.default ? "currentColor" : "none"} /></button>

                    {#if g.cls.engines.length > 1}
                      <Select
                        bind:value={be.kind}
                        onchange={saveBackendsNow}
                        options={g.cls.engines.map((e) => ({ value: e.kind, label: e.label, detail: e.hint }))}
                        tooltip="Engine - decides which arg set is generated"
                        ariaLabel="Engine"
                        class="w-28 shrink-0"
                      />
                    {:else}
                      <span class="w-28 shrink-0 px-2 py-1 text-txtsecondary truncate" use:tip={g.cls.engines[0]?.hint ?? ""}>{g.cls.engines[0]?.label ?? be.kind}</span>
                    {/if}

                    <input
                      type="text" bind:value={be.name} placeholder="label (optional)" onblur={saveBackendsNow}
                      class="w-32 shrink-0 rounded border border-card-border bg-surface px-2 py-1 text-txtmain placeholder:text-txtsecondary/60 focus:outline-none focus:ring-2 focus:ring-primary"
                    />
                    <input
                      type="text" bind:value={be.path} placeholder="path to executable" onblur={saveBackendsNow}
                      readonly={be.managed}
                      use:tip={be.managed ? `Installed build ${be.version} (${be.variant}) - change it from Install a backend above` : ""}
                      class="flex-1 min-w-0 rounded border border-card-border bg-surface px-2 py-1 text-txtmain placeholder:text-txtsecondary/60 focus:outline-none focus:ring-2 focus:ring-primary {be.managed ? 'opacity-60' : ''}"
                    />
                    {#if be.managed}
                      <span class="shrink-0 text-micro font-medium uppercase tracking-wide text-primary border border-primary rounded px-1.5 py-0.5">installed</span>
                    {:else}
                      <button
                        type="button" use:tip={"Browse…"} aria-label="Browse for executable"
                        class="shrink-0 p-1.5 rounded border border-transparent text-txtsecondary hover:text-primary hover:border-primary transition-colors"
                        onclick={() => browseBackend(i)}
                      ><FolderOpen size={14} /></button>
                    {/if}
                    <button
                      type="button" use:tip={"Remove backend"} aria-label="Remove backend"
                      class="shrink-0 p-1.5 rounded border border-transparent text-txtsecondary hover:text-error hover:border-error transition-colors"
                      onclick={() => removeBackend(i)}
                    ><Trash2 size={14} /></button>
                  </div>
                {/each}
              </div>
            {/if}
          </section>
        {/each}
      </div>

      <div class="mt-3 flex items-center gap-3">
        <span class="text-micro text-txtsecondary">
          {savingBackends ? "Saving…" : backendsSaved ? "Saved - config regenerated; new paths apply on each model's next load." : "Autosaves on change; regenerates the config and hot-reloads."}
        </span>
        {#if backendsErr}
          <span class="text-micro text-error">{backendsErr}</span>
        {/if}
      </div>
    </div>
    {/if}
    </div>
</div>
