<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { onMount } from "svelte";
  import { SlidersHorizontal, HardDrive, Cpu, FolderOpen, Trash2, Star, Plus, Power, HelpCircle, Palette } from "lucide-svelte";
  import { getSettings, putSettings, putSlotCache, putBackends, putGuards, putAdvanced, resetAdvanced, pickFolder, pickBackend, resetSettings, getAutostart, putAutostart, fetchProcessSettings, putProcessSettings, type AppSettings, type BackendEntry, type AutostartStatus, type ProcessSettingsResponse } from "../stores/api";
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
    slotPreamble = s.slotCache.preambleCaches;
    backends = s.backendList.map((b) => ({ ...b }));
    syncAdvancedForm(s);
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
  // Preamble caches: the shared system+tools seed, minted per agent without the
  // user asking. Separate switch because it is the half that appears unprompted
  // and is exempt from the LRU caps above. Default on (a fresh config has no key).
  let slotPreamble = $state(true);
  let savingSlot = $state(false);
  let slotErr = $state<string | null>(null);

  const slotDirty = $derived(
    !!settings &&
      (slotEnable !== settings.slotCache.enable ||
        slotPath !== settings.slotCache.path ||
        Number(slotMinTokens) !== settings.slotCache.minSaveTokens ||
        Number(slotMaxDiskGB) !== settings.slotCache.maxDiskGB ||
        Number(slotMaxSessions) !== settings.slotCache.maxSessions ||
        slotPreamble !== settings.slotCache.preambleCaches),
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
        preambleCaches: slotPreamble,
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

  // --- OOM guard + GPU usage -------------------------------------------------
  // Saved through their own endpoint: the server carries every other section's
  // settings forward, so this write can't revert the memory or advanced fields.
  let gEvict = $state(true);
  let gReserve = $state(0);
  let gGrace = $state(30);
  let gMinGpu = $state(0.5);
  let gMulti = $state(true);
  let savingGuards = $state(false);
  let guardsErr = $state<string | null>(null);
  let guardsSaved = $state(false);
  let guardsFlashTimer: ReturnType<typeof setTimeout> | undefined;

  const guardsDirty = $derived(
    !!settings &&
      (gEvict !== settings.guards.oomGuardEvict ||
        Number(gReserve) !== settings.guards.oomGuardReserveGB ||
        Number(gGrace) !== settings.guards.oomGuardGraceSec ||
        Number(gMinGpu) !== settings.guards.minGpuFraction ||
        gMulti !== settings.guards.multiResident),
  );

  async function saveGuards(): Promise<void> {
    // Clamp to what the endpoint accepts rather than letting a mid-typing "0"
    // bounce back as a 400 the user has to read to understand.
    if (Number(gGrace) < 1) gGrace = 1;
    if (Number(gMinGpu) > 1) gMinGpu = 1;
    if (Number(gReserve) < 0) gReserve = 0;
    savingGuards = true;
    guardsErr = null;
    try {
      await putGuards({
        oomGuardEvict: gEvict,
        oomGuardReserveGB: Number(gReserve) || 0,
        oomGuardGraceSec: Number(gGrace) || 1,
        minGpuFraction: Number(gMinGpu) || 0,
        multiResident: gMulti,
      });
      await loadSettings();
      guardsSaved = true;
      clearTimeout(guardsFlashTimer);
      guardsFlashTimer = setTimeout(() => (guardsSaved = false), 2500);
    } catch (e) {
      guardsErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingGuards = false;
    }
  }

  let guardsAutosaveTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!guardsDirty || savingGuards) return;
    void gEvict; void gReserve; void gGrace; void gMinGpu; void gMulti;
    clearTimeout(guardsAutosaveTimer);
    guardsAutosaveTimer = setTimeout(saveGuards, 900);
    return () => clearTimeout(guardsAutosaveTimer);
  });

  // --- Advanced sizer knobs + fleet-wide model defaults ----------------------
  // ONE form object behind three UI cards (the Advanced disclosure here, KV quant
  // in the KV Cache tab, the LoRA folder in Backends) because they share one
  // endpoint: a save sends the whole advanced block. Editing in one card and
  // saving from another therefore flushes both, which is the intended behaviour -
  // it is a single settings object, not three.
  //
  // Unlike every other section, the sizer knobs do NOT autosave: a half-typed
  // context ladder that regenerates the config on a 900ms timer is exactly the
  // failure mode the warning above them is about. They get an explicit Apply.
  let advOpen = $state(false);
  let aCompute = $state(1);
  let aVisionOverhead = $state(1);
  let aVisionCtx = $state(8192);
  let aMoeCtx = $state(65536);
  let aDenseMin = $state(32768);
  let aLadder = $state(""); // comma-separated; parsed on save
  let aThreads = $state(0);
  let aHealth = $state(300);
  let aKv = $state("");
  let aLora = $state("");
  let savingAdv = $state(false);
  let advErr = $state<string | null>(null);
  let advSaved = $state(false);
  let advFlashTimer: ReturnType<typeof setTimeout> | undefined;

  const kvQuantOptions = [
    { value: "", label: "Auto", detail: "Per-model default chosen by the sizer" },
    { value: "f16", label: "f16", detail: "Full quality, largest KV" },
    { value: "bf16", label: "bf16", detail: "Same size as f16, wider range" },
    { value: "q8_0", label: "q8_0", detail: "Half the KV, near-lossless" },
    { value: "q5_1", label: "q5_1", detail: "" },
    { value: "q5_0", label: "q5_0", detail: "" },
    { value: "q4_1", label: "q4_1", detail: "" },
    { value: "q4_0", label: "q4_0", detail: "Smallest KV, visible quality loss" },
  ];

  function syncAdvancedForm(s: AppSettings): void {
    gEvict = s.guards.oomGuardEvict;
    gReserve = s.guards.oomGuardReserveGB;
    gGrace = s.guards.oomGuardGraceSec;
    gMinGpu = s.guards.minGpuFraction;
    gMulti = s.guards.multiResident;
    aCompute = s.advanced.computeBufFactor;
    aVisionOverhead = s.advanced.visionOverheadGB;
    aVisionCtx = s.advanced.visionCtx;
    aMoeCtx = s.advanced.moeCtxTarget;
    aDenseMin = s.advanced.denseMinCtx;
    aLadder = (s.advanced.denseCtxLadder ?? []).join(", ");
    aThreads = s.advanced.threads;
    aHealth = s.advanced.healthCheckTimeout;
    aKv = s.advanced.kvQuant;
    aLora = s.advanced.loraDir;
  }

  // A blank/garbage entry is dropped rather than sent as 0 - the endpoint
  // rejects a non-positive ladder step, and a silent 400 on an unrelated save
  // would be baffling.
  const parsedLadder = $derived(
    aLadder
      .split(/[,\s]+/)
      .map((v) => Number(v))
      .filter((n) => Number.isFinite(n) && n > 0),
  );

  async function saveAdvanced(): Promise<void> {
    savingAdv = true;
    advErr = null;
    try {
      await putAdvanced({
        computeBufFactor: Number(aCompute) || 0,
        visionOverheadGB: Number(aVisionOverhead) || 0,
        visionCtx: Number(aVisionCtx) || 0,
        moeCtxTarget: Number(aMoeCtx) || 0,
        denseMinCtx: Number(aDenseMin) || 0,
        denseCtxLadder: parsedLadder,
        threads: Number(aThreads) || 0,
        healthCheckTimeout: Number(aHealth) || 0,
        kvQuant: aKv,
        loraDir: aLora.trim(),
      });
      await loadSettings();
      advSaved = true;
      clearTimeout(advFlashTimer);
      advFlashTimer = setTimeout(() => (advSaved = false), 2500);
    } catch (e) {
      advErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingAdv = false;
    }
  }

  async function resetAdvancedToDefault(): Promise<void> {
    savingAdv = true;
    advErr = null;
    try {
      await resetAdvanced();
      await loadSettings(); // repopulates the form with the recomputed defaults
    } catch (e) {
      advErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingAdv = false;
    }
  }

  async function browseLoraDir(): Promise<void> {
    const p = await pickFolder();
    if (p) {
      aLora = p;
      await saveAdvanced();
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

  // --- Process-level settings: ports, remote access, updates, HF token ------
  //
  // The odd section out. Every other block on this page regenerates the config
  // and hot-reloads; these are read by the process at startup, so a save is
  // only a save. `proc.running` is what the process actually bound, and the
  // per-field diff below is how the page says "restart to apply" about the
  // fields that need it, and stays quiet about the ones that don't.
  //
  // No autosave here either: half-typed ports are not a thing to persist, and
  // the value only takes effect on a deliberate restart anyway.
  let proc = $state<ProcessSettingsResponse | null>(null);
  let pListen = $state("");
  let pPlayground = $state("");
  let pAdminAllow = $state("");
  let pAdminOpen = $state(false);
  let pWatch = $state(true);
  let pWatchInterval = $state(5);
  let pUpdate = $state(true);
  // The stored token is never returned, so the box starts blank and an empty
  // box means "leave it". Clearing is the explicit button.
  let pHfToken = $state("");
  let savingProc = $state(false);
  let procErr = $state("");
  let procSaved = $state(false);

  function syncProcForm(r: ProcessSettingsResponse) {
    proc = r;
    pListen = r.settings.listen;
    pPlayground = r.settings.playgroundListen;
    pAdminAllow = r.settings.adminAllow;
    pAdminOpen = r.settings.adminOpen;
    pWatch = r.settings.watchModels;
    pWatchInterval = r.settings.watchModelsIntervalSec || r.running.watchModelsIntervalSec || 5;
    pUpdate = r.settings.updateCheck;
    pHfToken = "";
  }

  async function loadProcSettings() {
    try {
      syncProcForm(await fetchProcessSettings());
    } catch (e) {
      procErr = e instanceof Error ? e.message : String(e);
    }
  }

  // A blank field means "unset - use the built-in default", and the built-in
  // default is what the process is running, so an empty box must NOT read as a
  // pending change against a non-empty running value.
  function pendingAddr(saved: string, running: string): boolean {
    return saved.trim() !== "" && saved.trim() !== running;
  }

  // Which saved values are not yet in force. Only the socket/policy ones can
  // be pending: watch-models and the update poll are re-read on each cycle.
  const restartNeeded = $derived.by(() => {
    if (!proc) return [] as string[];
    const s = proc.settings, r = proc.running;
    const out: string[] = [];
    if (pendingAddr(s.listen, r.listen)) out.push("API address");
    if (pendingAddr(s.playgroundListen, r.playgroundListen)) out.push("Playground address");
    if (s.adminAllow.trim() !== r.adminAllow.trim()) out.push("Dashboard access list");
    if (s.adminOpen !== r.adminOpen) out.push("Open dashboard");
    if (s.watchModels !== r.watchModels) out.push("Watch models folder");
    if (s.watchModelsIntervalSec > 0 && s.watchModelsIntervalSec !== r.watchModelsIntervalSec)
      out.push("Scan interval");
    if (s.updateCheck !== r.updateCheck) out.push("Automatic updates");
    return out;
  });

  const procDirty = $derived(
    !!proc &&
      (pListen !== proc.settings.listen ||
        pPlayground !== proc.settings.playgroundListen ||
        pAdminAllow !== proc.settings.adminAllow ||
        pAdminOpen !== proc.settings.adminOpen ||
        pWatch !== proc.settings.watchModels ||
        pWatchInterval !== (proc.settings.watchModelsIntervalSec || proc.running.watchModelsIntervalSec) ||
        pUpdate !== proc.settings.updateCheck ||
        pHfToken.trim() !== ""),
  );

  async function saveProc(opts: { clearToken?: boolean } = {}) {
    if (savingProc) return;
    savingProc = true;
    procErr = "";
    procSaved = false;
    try {
      await putProcessSettings({
        listen: pListen.trim(),
        playgroundListen: pPlayground.trim(),
        adminAllow: pAdminAllow.trim(),
        adminOpen: pAdminOpen,
        watchModels: pWatch,
        watchModelsIntervalSec: Math.max(0, Math.round(pWatchInterval || 0)),
        updateCheck: pUpdate,
        hfToken: pHfToken.trim(),
        hfTokenClear: !!opts.clearToken,
        hfTokenSet: false,
      });
      // Re-read rather than patching locally: the restart banner is driven by
      // the server's saved-vs-running diff, and the token field by a flag the
      // client cannot compute.
      syncProcForm(await fetchProcessSettings());
      procSaved = true;
      setTimeout(() => (procSaved = false), 2000);
    } catch (e) {
      procErr = e instanceof Error ? e.message : String(e);
    } finally {
      savingProc = false;
    }
  }

  onMount(() => {
    loadSettings();
    loadAutostart();
    loadProcSettings();
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

      <div class="grid grid-cols-2 md:grid-cols-3 gap-3 text-label">
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
            {@render hint("Safety margin subtracted from Target VRAM before any model is sized - it pads Quartermaster's own estimate of what a model costs, covering CUDA context, compute buffers and allocator fragmentation. Charged ALWAYS, even on an idle card, and baked into the config at generate time. Not the same as the OOM guard's Reserve, which is about other apps and is only charged while one is actually growing. Raise this if you hit OOM right after a load; it does not account for current desktop/game usage (auto-vram does that at startup).")}
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
      </div>

      <!-- Idle unload sits on its own row: it is the only EVICTION knob here,
           and squeezing it in as a fourth column of VRAM/RAM budgets read as
           though it were part of the sizing math. -->
      <div class="mt-3 grid grid-cols-2 md:grid-cols-3 gap-3 text-label">
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

    <!-- OOM guard: what happens AFTER a model is resident and something else
         (a game, a browser) starts eating the same VRAM. Separate from the
         memory budget above, which only sizes models at load time. -->
    <div class="mt-6">
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6>OOM guard</h6>
          {#if settings?.guardsOverridden}
            <span class="text-micro font-medium uppercase tracking-wide text-primary border border-primary/40 rounded px-1.5 py-0.5">custom</span>
          {:else}
            <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">default</span>
          {/if}
        </div>
        <span class="text-micro">
          {#if guardsErr}
            <span class="text-error">{guardsErr}</span>
          {:else if savingGuards}
            <span class="text-txtsecondary">Saving…</span>
          {:else if guardsSaved}
            <span class="text-primary">Saved!</span>
          {/if}
        </span>
      </div>

      <!-- Reserve is deliberately ABOVE the toggle and never disabled by it.
           OomGuardEvict gates the post-load watchdog only (vramguard.go, the
           shed loop); the reserve is subtracted in ceilingGB(), which the
           ADMISSION path calls on every spawn regardless. Greying it out with
           the toggle said it was inert when it was still in force. -->
      <label class="flex flex-col gap-1 text-label mb-3 max-w-xs">
        <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
          Reserve (GB)
          {@render hint("VRAM left free for OTHER apps that are still growing, and charged only while one actually is: the live ceiling is the budget minus foreign usage above its idle baseline minus this. On a quiet machine it costs nothing. The case it exists for is a game that has just claimed 8GB and is not done allocating - loading a model into the exact leftovers puts the next allocation, from either side, into shared memory. Distinct from Headroom above, which pads every model's own size estimate and is charged always.")}
        </span>
        <input
          type="number" min="0" step="0.25" bind:value={gReserve}
          class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
        />
        <span class="text-micro text-txtsecondary">0 = admit into the raw leftovers · default 1</span>
      </label>

      <label class="flex items-start gap-3 mb-3">
        <Toggle size="sm" checked={gEvict} onchange={(v: boolean) => (gEvict = v)} />
        <span class="text-label">
          <span class="text-txtmain flex items-center gap-1">
            Shed idle models when VRAM runs out
            {@render hint("A watchdog that runs after models are loaded. When something outside Quartermaster (a game, a browser, another app) grows into the VRAM the resident models need, and stays there past the grace period, the guard unloads IDLE models - newest-loaded first - until the set fits again. A model serving a request is never touched. Off means the loader keeps everything resident and lets the driver decide, which usually means a crash or a fallback to shared system memory. It does NOT affect the reserve above, which applies when a model is admitted.")}
          </span>
          <span class="text-micro text-txtsecondary">Off: nothing is unloaded once loaded, whatever else is on the card.</span>
        </span>
      </label>

      <label class="flex flex-col gap-1 text-label max-w-xs" class:opacity-50={!gEvict}>
        <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
          Grace (s)
          {@render hint("How long the resident set must stay over the live ceiling before anything is unloaded. This is the anti-thrash delay: a shader compile or a video decode spikes VRAM for a second or two, and a short grace would evict a model because of it. Minimum 1 - to disable shedding entirely, use the toggle above.")}
        </span>
        <input
          type="number" min="1" step="5" bind:value={gGrace} disabled={!gEvict}
          class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
        />
        <span class="text-micro text-txtsecondary">longer = fewer false evictions · default 30</span>
      </label>
    </div>

    <!-- GPU usage: the admission side - whether a model is allowed to load at
         all, and whether two may share the card. -->
    <div class="mt-6">
      <div class="flex items-center justify-between mb-3">
        <h6>GPU usage</h6>
      </div>

      <label class="flex flex-col gap-1 text-label mb-3 max-w-xs">
        <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
          Minimum GPU share
          {@render hint("The admission floor for the spawn-time sizer: the least of a model's layers that must end up on the GPU for the load to go ahead. At 0.5, a model that could only get 40% of itself onto the card - because something else took the VRAM - is refused rather than launched at a crawl over PCIe. Lower it to allow heavier CPU offload, or set 0 to always launch whatever fits.")}
        </span>
        <input
          type="number" min="0" max="1" step="0.05" bind:value={gMinGpu}
          class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
        />
        <span class="text-micro text-txtsecondary">0-1 · 0 disables the floor · default 0.5</span>
      </label>

      <label class="flex items-start gap-3">
        <Toggle size="sm" checked={gMulti} onchange={(v: boolean) => (gMulti = v)} />
        <span class="text-label">
          <span class="text-txtmain flex items-center gap-1">
            Allow several models on the GPU at once
            {@render hint("Lets more than one model stay resident, each sized against the VRAM actually left rather than against the whole card. Turn it off to have every model sized as if it were alone - simpler and safer for a single card, at the cost of a full unload/reload on every swap.")}
          </span>
          <span class="text-micro text-txtsecondary">Off: one model at a time, each sized against the full budget.</span>
        </span>
      </label>
    </div>

    <!-- Advanced sizer knobs. Collapsed, warned, and explicitly applied - see
         the note in the script block on why these alone do not autosave. -->
    <div class="mt-6">
      <button
        class="flex items-center gap-2 text-txtsecondary hover:text-txtmain"
        onclick={() => (advOpen = !advOpen)}
        aria-expanded={advOpen}
      >
        <h6 class="!m-0">Advanced</h6>
        <span class="text-micro">{advOpen ? "▾" : "▸"}</span>
        {#if settings?.advancedOverridden}
          <span class="text-micro font-medium uppercase tracking-wide text-primary border border-primary/40 rounded px-1.5 py-0.5">custom</span>
        {/if}
      </button>

      {#if advOpen}
        <p class="mt-2 mb-3 text-label text-warning">
          ⚠ These are the sizer's internal constants. Wrong values mis-size <em>every</em> model -
          silently, as failed loads or out-of-memory crashes long after the change. Do not touch them
          unless you know what you are doing; use Restore defaults to get back.
        </p>

        <div class="grid grid-cols-2 md:grid-cols-4 gap-3 text-label">
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Compute buffer ×
              {@render hint("Scales the modeled GPU compute buffer (logits + activations) before it is charged against the VRAM budget. Raise it if models with very large vocabularies spill; lower it to reclaim budget on models that never do.")}
            </span>
            <input type="number" min="0" step="0.05" bind:value={aCompute} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.computeBufFactor}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Vision overhead (GB)
              {@render hint("VRAM reserved for a vision model's CLIP/projector weights and image buffers, which are not part of the gguf the sizer measures.")}
            </span>
            <input type="number" min="0" step="0.25" bind:value={aVisionOverhead} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.visionOverheadGB}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Vision context
              {@render hint("Default context window (tokens) for the auto-generated vision variant of a model. Images eat context fast, but a large window here costs VRAM on every vision load.")}
            </span>
            <input type="number" min="0" step="1024" bind:value={aVisionCtx} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.visionCtx}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              MoE context target
              {@render hint("Context window the sizer aims for on mixture-of-experts models before it starts trading context away for GPU layers.")}
            </span>
            <input type="number" min="0" step="4096" bind:value={aMoeCtx} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.moeCtxTarget}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Dense min context
              {@render hint("Floor for dense models: the sizer will not walk the ladder below this, even if that means offloading more layers to the CPU.")}
            </span>
            <input type="number" min="0" step="4096" bind:value={aDenseMin} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.denseMinCtx}</span>
          </label>
          <label class="flex flex-col gap-1 col-span-2">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Dense context ladder
              {@render hint("Context ceiling for a dense model, largest first: the top rung caps how far the sizer may grow the window, and within that cap it takes the largest context whose KV cache fits the budget (rounded to 4096). Keep the top rung at or above the trained window of your largest-context model, or that model will be pinned below it. Comma-separated; entries that are not positive numbers are dropped.")}
            </span>
            <input type="text" bind:value={aLadder} spellcheck="false" class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {(settings?.advancedDefaults.denseCtxLadder ?? []).join(", ")}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Threads
              {@render hint("CPU threads passed to every backend (-t). Leave at the default unless you are pinning cores; oversubscribing slows generation rather than speeding it up.")}
            </span>
            <input type="number" min="0" step="1" bind:value={aThreads} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.threads}</span>
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
              Health timeout (s)
              {@render hint("How long a backend has to come up and answer its health check before the load is declared failed. Raise it for very large models on slow disks - a load that is merely slow otherwise looks like a crash.")}
            </span>
            <input type="number" min="0" step="30" bind:value={aHealth} class="w-full font-mono rounded border border-card-border bg-surface px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary" />
            <span class="text-micro text-txtsecondary">default {settings?.advancedDefaults.healthCheckTimeout}</span>
          </label>
        </div>

        <div class="mt-3 flex items-center justify-between gap-3">
          <span class="text-micro">
            {#if advErr}
              <span class="text-error">{advErr}</span>
            {:else if savingAdv}
              <span class="text-txtsecondary">Saving…</span>
            {:else if advSaved}
              <span class="text-primary">Saved!</span>
            {:else}
              <span class="text-txtsecondary">Applying regenerates the config and hot-reloads.</span>
            {/if}
          </span>
          <span class="flex items-center gap-2">
            <button
              class="btn btn--sm uppercase tracking-wide"
              onclick={resetAdvancedToDefault}
              disabled={savingAdv || !settings?.advancedOverridden}
              use:tip={"Restore every knob in this section to its computed default. Leaves the memory and guard settings alone."}
            >
              Restore defaults
            </button>
            <button class="btn btn--sm btn--primary uppercase tracking-wide" onclick={saveAdvanced} disabled={savingAdv}>
              Apply
            </button>
          </span>
        </div>
      {/if}
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

    <!-- Network & updates: the flags a packaged install has no command line to
         pass. Saved to the sidecar, applied at the next start. -->
    <div class="mt-6">
      <div class="flex items-center gap-2 mb-1">
        <h6>Network</h6>
        {@render hint("Which addresses Quartermaster binds. host:port, where an empty host (\":1250\") or 0.0.0.0 means every interface and 127.0.0.1 means this machine only. Leave a field blank to use the built-in default. These are bound once at launch, so a change takes effect on the next restart.")}
      </div>
      <p class="text-micro text-txtsecondary mb-3">
        Bound at launch; changes here apply after a restart.
      </p>

      <div class="rounded border border-card-border bg-surface p-3 grid gap-3 sm:grid-cols-2">
        <label class="grid gap-1">
          <span class="text-label text-txtmain flex items-center gap-1">
            API &amp; dashboard address
            {@render hint("The main listener: the OpenAI-compatible API, the dashboard, and this settings page. Currently bound to " + (proc?.running.listen || "?") + ".")}
          </span>
          <input
            type="text" spellcheck="false" placeholder={proc?.running.listen || "0.0.0.0:1250"}
            bind:value={pListen}
            class="w-full font-mono rounded border border-card-border bg-background px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">running: <span class="font-mono">{proc?.running.listen || "—"}</span></span>
        </label>

        <label class="grid gap-1">
          <span class="text-label text-txtmain flex items-center gap-1">
            Playground address
            {@render hint("The extra listener serving the standalone playground app (its own login and chat history). Leave it blank to use the built-in default - 0.0.0.0:8081 on a packaged install, and no playground listener at all on a dev build.")}
          </span>
          <input
            type="text" spellcheck="false" placeholder="0.0.0.0:8081"
            bind:value={pPlayground}
            class="w-full font-mono rounded border border-card-border bg-background px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">
            running: <span class="font-mono">{proc?.running.playgroundListen || "off"}</span> · blank uses the default
          </span>
        </label>
      </div>

      <div class="mt-3 rounded border border-card-border bg-surface p-3 grid gap-3">
        <label class="grid gap-1">
          <span class="text-label text-txtmain flex items-center gap-1">
            Extra hosts allowed on the dashboard
            {@render hint("When the API listens beyond this machine, the dashboard and admin endpoints stay restricted to localhost - the inference API is unaffected. List extra IPs or CIDR ranges here to let them in too, comma separated. A tailnet is the usual case: 100.64.0.0/10.")}
          </span>
          <input
            type="text" spellcheck="false" placeholder="e.g. 100.64.0.0/10, 192.168.1.50"
            bind:value={pAdminAllow}
            class="w-full font-mono rounded border border-card-border bg-background px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">Comma-separated IPs or CIDR ranges. Empty means this machine only.</span>
        </label>

        <label class="flex items-start gap-3 cursor-pointer">
          <Toggle size="sm" checked={pAdminOpen} onchange={(v: boolean) => (pAdminOpen = v)} />
          <span class="text-label min-w-0">
            <span class="text-txtmain">Serve the dashboard to every host</span>
            <span class="block mt-0.5 text-micro text-txtsecondary">
              The dashboard has <strong>no password</strong>. Turning this on hands config editing,
              the model browser and the API keys page to anyone who can reach the port. Prefer the
              allow-list above.
            </span>
          </span>
        </label>

        {#if pAdminOpen}
          <div class="rounded border border-warning/40 bg-warning/10 p-2 text-micro text-warning">
            ⚠ The unauthenticated dashboard will be reachable from every host that can reach the API address.
          </div>
        {/if}
      </div>
    </div>

    <div class="mt-6">
      <div class="flex items-center gap-2 mb-3">
        <h6>Models folder</h6>
        {@render hint("Whether Quartermaster notices GGUFs appearing in or leaving the models folder on its own, without a restart or a manual rescan.")}
      </div>

      <div class="rounded border border-card-border bg-surface p-3 grid gap-3">
        <label class="flex items-start gap-3 cursor-pointer">
          <Toggle size="sm" checked={pWatch} onchange={(v: boolean) => (pWatch = v)} />
          <span class="text-label min-w-0">
            <span class="text-txtmain">Watch the models folder</span>
            <span class="block mt-0.5 text-micro text-txtsecondary">
              Re-scan for added or removed GGUFs and reload the catalog automatically. Turn it off if
              the models live on a slow or sleeping drive that a poll keeps waking.
            </span>
          </span>
        </label>

        <label class="grid gap-1 max-w-[12rem]" class:opacity-40={!pWatch}>
          <span class="text-label text-txtmain">Scan interval (s)</span>
          <input
            type="number" min="1" step="1" disabled={!pWatch} bind:value={pWatchInterval}
            class="w-full font-mono rounded border border-card-border bg-background px-2 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <span class="text-micro text-txtsecondary">default 5</span>
        </label>
      </div>
    </div>

    <!-- Its own section, NOT filed under the models folder above: this polls for
         new builds of Quartermaster, and an app-update toggle sitting under a
         heading about models reads as "check for model updates". -->
    <div class="mt-6">
      <div class="flex items-center gap-2 mb-3">
        <h6>Quartermaster updates</h6>
        {@render hint("Whether the app looks for newer builds of ITSELF. Nothing to do with your models - it polls the Quartermaster releases page on GitHub. Release builds only; a dev build never checks.")}
      </div>

      <div class="rounded border border-card-border bg-surface p-3">
        <label class="flex items-start gap-3 cursor-pointer">
          <Toggle size="sm" checked={pUpdate} onchange={(v: boolean) => (pUpdate = v)} />
          <span class="text-label min-w-0">
            <span class="text-txtmain">Check for new Quartermaster versions</span>
            <span class="block mt-0.5 text-micro text-txtsecondary">
              Poll the releases page in the background so the sidebar can offer an upgrade of the app
              itself. Installing one is always a deliberate click; see Software update at the top of
              this tab. Never active in a dev build.
            </span>
          </span>
        </label>
      </div>
    </div>

    <div class="mt-6">
      <div class="flex items-center gap-2 mb-3">
        <h6>Hugging Face token</h6>
        {@render hint("An access token from huggingface.co/settings/tokens. It authenticates the model browser's searches and downloads, which gets you gated repositories (Llama, Gemma and friends) and a much higher rate limit. Read access is enough.")}
      </div>

      <div class="rounded border border-card-border bg-surface p-3 grid gap-2">
        <div class="flex items-end gap-2">
          <label class="grid gap-1 flex-1 min-w-0">
            <span class="text-label text-txtmain">Token</span>
            <input
              type="password" spellcheck="false" autocomplete="off"
              placeholder={proc?.settings.hfTokenSet ? "•••••••• stored, type to replace" : "hf_…"}
              bind:value={pHfToken}
              class="w-full font-mono rounded border border-card-border bg-background px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
          </label>
          {#if proc?.settings.hfTokenSet}
            <button
              class="btn btn--sm uppercase tracking-wide hover:border-error hover:text-error"
              disabled={savingProc}
              onclick={() => saveProc({ clearToken: true })}
            >
              Clear
            </button>
          {/if}
        </div>
        <p class="text-micro text-txtsecondary">
          Stored in the overrides file next to your config, and never shown again after saving.
          Applies to the model browser from the next restart.
        </p>
        {#if proc?.envToken}
          <div class="rounded border border-warning/40 bg-warning/10 p-2 text-micro text-warning">
            ⚠ HF_TOKEN is set in this process's environment, and an environment token always wins, so
            anything saved here will be ignored until it is unset.
          </div>
        {/if}
      </div>
    </div>

    <div class="mt-4 flex items-center gap-3">
      <button
        class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
        disabled={savingProc || !procDirty}
        onclick={() => saveProc()}
      >
        {savingProc ? "Saving…" : "Save"}
      </button>
      {#if procErr}
        <span class="text-micro text-error">{procErr}</span>
      {:else if procSaved}
        <span class="text-micro text-primary">Saved!</span>
      {:else if procDirty}
        <span class="text-micro text-txtsecondary">Unsaved changes</span>
      {/if}
    </div>

    {#if restartNeeded.length > 0}
      <div class="mt-3 rounded border border-warning/40 bg-warning/10 p-2 text-micro text-warning">
        ⚠ Saved, but not in force yet: {restartNeeded.join(", ")}. Restart Quartermaster to apply.
      </div>
    {/if}
    {:else if cat === "kvcache"}
    <!-- Fleet-wide KV type. Lives here rather than in the advanced block because
         it is a quality/VRAM trade-off a user can reason about, not a sizer
         constant - but it shares the advanced save (see the script block). -->
    <div class="mb-6">
      <div class="flex items-baseline gap-2 mb-1">
        <h6>KV cache type</h6>
        {@render hint("The precision every model's KV cache is stored at (-ctk/-ctv). Auto lets the sizer pick per model. Quantising the cache roughly halves (q8_0) or quarters (q4_0) the VRAM a context window costs, which buys a longer window at some quality cost - q8_0 is close to lossless, q4_0 is visibly not. A per-model setting in the model config editor still wins over this.")}
      </div>
      <div class="flex items-end gap-3 max-w-md">
        <div class="flex-1">
          <Select
            value={aKv}
            options={kvQuantOptions}
            onchange={(v: string) => {
              aKv = v;
              void saveAdvanced();
            }}
          />
        </div>
        <span class="text-micro pb-1">
          {#if advErr}
            <span class="text-error">{advErr}</span>
          {:else if savingAdv}
            <span class="text-txtsecondary">Saving…</span>
          {:else if advSaved}
            <span class="text-primary">Saved!</span>
          {/if}
        </span>
      </div>
      <p class="mt-1 text-micro text-txtsecondary">Applies on each model's next load.</p>
    </div>

    <!-- Slot KV-cache persistence -->
    <div>
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <h6 >KV-cache disk save</h6>
          <span class="status bg-warning/10 text-warning text-micro !px-1.5 !py-0.5">Experimental</span>
          {@render hint("Persist a long conversation's KV cache to disk so it survives being evicted from the live slot, and is restored instead of reprocessed. Master switch: each model opts in via its config editor (\"Save KV cache to disk\").\n\nExperimental: exact save/restore of a conversation works on both standard transformers and hybrid/recurrent models (e.g. Qwen3.5/3.6 GatedDeltaNet) - a restored session continues by appending, reusing ~100% of its tokens. What hybrids can't do is partial-prefix seeding (preamble mint / best-seed reuse): a recurrent state can be continued forward but never rewound or trimmed, so those paths are skipped automatically on recurrent models.")}
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
              <FolderOpen size={13} class="inline-block -mt-px mr-1" />Browse…
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
        <label class="col-span-2 flex items-center gap-2 pt-1">
          <Toggle size="sm" bind:checked={slotPreamble} disabled={!slotEnable} />
          <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">
            Preamble caches
            {@render hint("The OTHER half of the feature: one shared system+tools KV per agent (not per chat), prefilled once and reused as the seed of every cold load that sends the same preamble. Minted unprompted on an agent's first request, hundreds of MB each, and NOT counted against the disk / session caps above (only the newest 3 per model are kept), which is why it switches off separately.\n\nOff here means no model mints or restores one; a single model can also be excluded from its config editor. Conversation snapshots are unaffected either way.")}
          </span>
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

      <div class="mt-6">
        <div class="flex items-baseline gap-2 mb-1">
          <h6>LoRA folder</h6>
          {@render hint("Where the image backend looks for LoRA files (--lora-model-dir), for every image model. Leave blank to use each model's own folder, which is the default. A per-model LoRA folder in the model config editor still wins over this.")}
        </div>
        <div class="flex items-center gap-2 max-w-2xl">
          <input
            type="text" bind:value={aLora} spellcheck="false" placeholder="each model's own folder"
            onblur={saveAdvanced}
            class="flex-1 font-mono text-label rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <button class="btn btn--sm" onclick={browseLoraDir} use:tip={"Choose a folder"} aria-label="Browse for LoRA folder">
            <FolderOpen size={14} />
          </button>
        </div>
      </div>
    </div>
    {/if}
    </div>
</div>
