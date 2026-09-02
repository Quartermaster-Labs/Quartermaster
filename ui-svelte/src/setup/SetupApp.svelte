<script lang="ts">
  import { onMount } from "svelte";
  import { FolderOpen } from "lucide-svelte";
  import * as api from "./api";
  import type { Probe, ScanResult, Status } from "./api";
  import Select from "../components/Select.svelte";
  import * as native from "../lib/native";
  import WindowControls from "../components/WindowControls.svelte";

  // Laid out as a classic installer -- fixed header, one panel of content, a
  // footer rail of Back/Next -- rather than as a web page. The window has no
  // address bar, so the footer is the only navigation the user gets, and it has
  // to sit where an installer's does.
  const STEPS = ["Location", "Models", "Backends"] as const;

  let step = $state(0);
  let probe = $state<Probe | null>(null);
  let loadError = $state("");

  let dir = $state("");
  let modelsRoot = $state("");
  let variant = $state("");
  let picked = $state<Record<string, boolean>>({});
  // Shortcut options. The menu entry is on by default because that is the one
  // entry point a user expects to exist without having asked; a desktop icon
  // and a login start are both things you opt in to. Windows maps them to Inno
  // tasks, Linux to XDG desktop entries (cmd/quartermaster-setup/
  // shortcuts_linux.go); macOS has neither yet, so the block is hidden there.
  let startMenu = $state(true);
  let desktopIcon = $state(false);
  let autostart = $state(false);

  let scan = $state<ScanResult | null>(null);
  let scanning = $state(false);

  let status = $state<Status | null>(null);
  let running = $state(false);
  let launch = $state(true);

  const isWindows = $derived(probe?.os === "windows");
  const isLinux = $derived(probe?.os === "linux");
  const canShortcut = $derived(isWindows || isLinux);
  const done = $derived(status?.phase === "done");
  const failed = $derived(status?.phase === "error");
  const components = $derived(
    (probe?.components ?? []).filter((c) => picked[c.id]).map((c) => c.id),
  );

  onMount(async () => {
    try {
      const p = await api.probe();
      probe = p;
      dir = p.defaultDir;
      variant = p.variant;
      for (const c of p.components ?? []) picked[c.id] = c.selected;
      // Seeded, not scanned: the home directory almost never holds models, and
      // walking it on open would spend seconds of the user's first impression
      // discovering nothing.
      modelsRoot = p.homeDir ? p.homeDir.replace(/\\/g, "/") + "/models" : "";
    } catch (e) {
      loadError = String(e);
    }
  });

  // Scanning is debounced against typing, and each run tags itself so a slow
  // answer for a path the user has already edited away is dropped instead of
  // overwriting the newer one.
  let scanSeq = 0;
  $effect(() => {
    const path = modelsRoot.trim();
    scan = null;
    if (!path) return;
    const seq = ++scanSeq;
    scanning = true;
    const t = setTimeout(async () => {
      try {
        const r = await api.scan(path);
        if (seq === scanSeq) scan = r;
      } catch {
        /* a scan that fails just leaves the hint blank */
      } finally {
        if (seq === scanSeq) scanning = false;
      }
    }, 400);
    return () => clearTimeout(t);
  });

  async function startInstall() {
    running = true;
    try {
      status = await api.install({
        dir: dir.trim(),
        modelsRoot: modelsRoot.trim(),
        variant,
        components,
        startMenu,
        desktopIcon,
        autostart,
      });
      poll();
    } catch (e) {
      running = false;
      status = {
        phase: "error",
        step: "Install",
        detail: "",
        downloaded: 0,
        total: 0,
        warnings: null,
        error: String(e),
        installDir: dir,
      };
    }
  }

  // Polled rather than streamed: the whole run is a handful of phases over a
  // few minutes, and an SSE endpoint would be more moving parts than this
  // amount of information justifies.
  function poll() {
    const id = setInterval(async () => {
      try {
        const s = await api.status();
        status = s;
        if (s.phase === "done" || s.phase === "error") {
          clearInterval(id);
          running = false;
        }
      } catch {
        /* keep polling: the server outlives a transient fetch failure */
      }
    }, 500);
  }

  async function close() {
    try {
      await api.finish(launch && !failed);
    } catch {
      /* the window is closing either way */
    }
  }

  function pct(s: Status): number {
    if (!s.total) return 0;
    return Math.min(100, Math.round((s.downloaded / s.total) * 100));
  }

  const gb = (n: number) => n.toFixed(1) + " GB";

  // The unit has to follow the number. A backend archive is tens to a couple of
  // hundred megabytes, so formatting the download counters in GB rounded every
  // one of them to "0.0 GB of 0.0 GB" beside a bar that was visibly moving: the
  // progress was right and the label said nothing.
  const size = (n: number) => {
    if (n >= 1e9) return (n / 1e9).toFixed(1) + " GB";
    if (n >= 1e6) return (n / 1e6).toFixed(0) + " MB";
    return (n / 1e3).toFixed(0) + " KB";
  };
  const noteFor = (id: string) =>
    (probe?.variants ?? []).find((v) => v.id === id)?.note ?? "";

  // The variant list already carries a one-line note per option, so it maps
  // straight onto Select's two-line rows -- which is half the reason the native
  // <select> had to go: an <option> can only ever show the label, so the note
  // had to be repeated underneath and only for the option already chosen.
  const variantOptions = $derived(
    (probe?.variants ?? []).map((v) => ({
      value: v.id,
      label: v.label,
      detail: v.note || undefined,
    })),
  );

  // The dialog always hands back a backslash path. The two boxes are seeded in
  // different styles (the install dir from Windows, the models root normalised
  // to forward slashes), and Go accepts either, so each keeps the style it is
  // already showing rather than flipping under the user mid-edit.
  async function browse(title: string, current: string): Promise<string> {
    return native.pickFolder(title, current.trim().replace(/\//g, "\\"));
  }

  async function browseDir() {
    const p = await browse("Where should Quartermaster go?", dir);
    if (p) dir = p;
  }

  async function browseModels() {
    const p = await browse("Where are your models?", modelsRoot);
    if (p) modelsRoot = p.replace(/\\/g, "/");
  }
</script>

<div class="flex h-screen flex-col bg-background text-txtmain">
  <!-- The window has no caption of its own (cmd/quartermaster-setup strips it),
       so this header IS the title bar: the whole strip drags, double-click
       maximises, and the three buttons on the right are the real system verbs
       going through the native bridge. In the browser fallback there is no
       bridge, so the buttons are simply not rendered and the strip is an
       ordinary header again. -->
  <header
    class="titlebar relative flex items-baseline gap-3 border-b border-card-border px-8 py-5"
    onmousedown={native.isNative ? native.titleBarMouseDown : undefined}
    role="presentation"
  >
    <h1 class="text-lg font-semibold tracking-tight">Quartermaster</h1>
    <span class="text-label text-txtsecondary">First-time setup</span>

    <div class="absolute right-0 top-0">
      <WindowControls />
    </div>
  </header>

  {#if loadError}
    <main class="flex-1 overflow-y-auto px-8 py-6">
      <p class="text-error">Could not start setup: {loadError}</p>
    </main>
  {:else if !probe}
    <main class="flex flex-1 items-center justify-center">
      <p class="text-txtsecondary">Checking your hardware…</p>
    </main>
  {:else if status}
    <!-- Progress and result share one panel: the install is a single continuous
         thing, and swapping the whole window at the moment it finishes would
         throw away the warnings the run collected. -->
    <main class="flex-1 overflow-y-auto px-8 py-6">
      <h2 class="text-base font-semibold">
        {done ? "Ready to go" : failed ? "Setup did not finish" : status.step}
      </h2>

      {#if !done && !failed}
        <p class="mt-1 h-5 font-mono text-xs text-txtsecondary">{status.detail}</p>
        <div class="mt-4 h-2 w-full overflow-hidden rounded-full bg-secondary">
          <div
            class="h-full bg-primary transition-[width] duration-300"
            class:animate-pulse={!status.total}
            style="width: {status.total ? pct(status) : 100}%"
          ></div>
        </div>
        {#if status.total}
          <p class="mt-2 font-mono text-micro text-txtsecondary">
            {size(status.downloaded)} of {size(status.total)}
          </p>
        {/if}
      {/if}

      {#if failed}
        <p class="mt-3 text-sm text-error">{status.error}</p>
      {/if}

      {#if done}
        <p class="mt-2 text-sm text-txtsecondary">
          Quartermaster is installed in
          <span class="font-mono">{status.installDir}</span>.
        </p>
      {/if}

      {#if status.warnings?.length}
        <div class="well mt-4">
          <p class="text-label font-semibold text-warning">Finished with warnings</p>
          <ul class="mt-1 list-disc pl-5 text-xs text-txtsecondary">
            {#each status.warnings as w}<li>{w}</li>{/each}
          </ul>
        </div>
      {/if}
    </main>

    <footer
      class="flex items-center justify-between border-t border-card-border px-8 py-4"
    >
      <label class="flex items-center gap-2 text-sm" class:opacity-50={!done}>
        <input type="checkbox" bind:checked={launch} disabled={!done} />
        Start Quartermaster now
      </label>
      <button class="btn btn--primary" disabled={running} onclick={close}>
        {failed ? "Close" : "Finish"}
      </button>
    </footer>
  {:else}
    <nav class="flex gap-6 border-b border-card-border px-8 py-2">
      {#each STEPS as label, i}
        <span
          class="text-label uppercase tracking-wider"
          class:text-primary={i === step}
          class:font-semibold={i === step}
          class:text-txtsecondary={i !== step}>{i + 1}. {label}</span
        >
      {/each}
    </nav>

    <main class="flex-1 overflow-y-auto px-8 py-6">
      {#if step === 0}
        <h2 class="text-base font-semibold">Where should Quartermaster go?</h2>
        <div class="mt-4 flex gap-2">
          <input class="min-w-0 flex-1 font-mono text-sm" bind:value={dir} spellcheck="false" />
          {#if native.isNative}
            <button class="btn inline-flex shrink-0 items-center gap-1.5" onclick={browseDir}>
              <FolderOpen size={14} /> Browse
            </button>
          {/if}
        </div>

        {#if canShortcut}
          <!-- Re-running the wizard applies these as written: unticking one
               removes the shortcut it made, so this is the current state of the
               install, not an add-only list. True on both platforms: Inno's
               [InstallDelete] does it on Windows, writeOrRemove on Linux. -->
          <div class="mt-5 flex flex-col gap-2 text-sm">
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={startMenu} />
              {isLinux ? "Add an application menu entry" : "Add a Start Menu entry"}
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={desktopIcon} />
              Create a desktop shortcut
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={autostart} />
              Start Quartermaster when I log in
            </label>
          </div>
        {/if}
      {:else if step === 1}
        <h2 class="text-base font-semibold">Where are your models?</h2>
        <p class="mt-1 text-sm text-txtsecondary">
          Point this at a folder of GGUF files. Quartermaster reads it in place and
          writes nothing to it. You can leave it blank and set it later.
        </p>
        <div class="mt-4 flex gap-2">
          <input
            class="min-w-0 flex-1 font-mono text-sm"
            bind:value={modelsRoot}
            spellcheck="false"
            placeholder="e.g. D:/LLM/Models"
          />
          {#if native.isNative}
            <button class="btn inline-flex shrink-0 items-center gap-1.5" onclick={browseModels}>
              <FolderOpen size={14} /> Browse
            </button>
          {/if}
        </div>

        <p class="mt-2 h-5 text-xs text-txtsecondary">
          {#if scanning}
            Scanning…
          {:else if scan?.error}
            <span class="text-warning">{scan.error}</span>
          {:else if scan && !scan.exists}
            <span class="text-txtsecondary">
              That folder does not exist yet, so it will be created.
            </span>
          {:else if scan}
            <span class="text-success">
              Found {scan.count}
              {scan.count === 1 ? "model" : "models"} ({gb(scan.sizeGB)}).
            </span>
          {/if}
        </p>
      {:else}
        <h2 class="text-base font-semibold">Which engines should Quartermaster install?</h2>
        <p class="mt-1 text-sm text-txtsecondary">
          These are downloaded now. Everything here can be changed later under
          Settings → Backends.
        </p>

        <div class="mt-4 space-y-2">
          {#each probe.components ?? [] as c}
            <label class="card flex cursor-pointer items-start gap-3">
              <input type="checkbox" class="mt-0.5" bind:checked={picked[c.id]} />
              <span>
                <span class="block text-sm font-medium">{c.name}</span>
                <span class="block text-xs text-txtsecondary">{c.summary}</span>
              </span>
            </label>
          {/each}
        </div>

        <h3 class="mt-6 text-sm font-semibold">Compute backend</h3>
        <p class="mt-1 text-xs text-txtsecondary">
          {#if probe.gpus?.length}
            Detected: <span class="font-mono">{probe.gpus.join(", ")}</span>
          {:else}
            No GPU detected, so CPU builds will be used.
          {/if}
        </p>
        <Select
          class="mt-2 w-full"
          bind:value={variant}
          options={variantOptions}
          ariaLabel="Compute backend"
        />
        {#if noteFor(variant)}
          <p class="mt-2 text-xs text-txtsecondary">{noteFor(variant)}</p>
        {/if}
      {/if}
    </main>

    <footer
      class="flex items-center justify-end gap-2 border-t border-card-border px-8 py-4"
    >
      <button class="btn" disabled={step === 0} onclick={() => step--}>Back</button>
      {#if step < STEPS.length - 1}
        <button class="btn btn--primary" disabled={!dir.trim()} onclick={() => step++}>
          Next
        </button>
      {:else}
        <button class="btn btn--primary" onclick={startInstall}>Install</button>
      {/if}
    </footer>
  {/if}
</div>

<style>
  /* Dragging a frameless window is a mousedown that never becomes a click: the
     window manager takes the mouse mid-gesture. Without this the pointer sweeps
     a text selection across the header on the way, and the selection stays
     highlighted after the drag ends. */
  .titlebar {
    user-select: none;
    -webkit-user-select: none;
  }
</style>
