<script lang="ts">
  import { onMount } from "svelte";
  import * as api from "./api";
  import type { Probe, ScanResult, Status } from "./api";

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
  let autostart = $state(false);

  let scan = $state<ScanResult | null>(null);
  let scanning = $state(false);

  let status = $state<Status | null>(null);
  let running = $state(false);
  let launch = $state(true);

  const isWindows = $derived(probe?.os === "windows");
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
  const noteFor = (id: string) =>
    (probe?.variants ?? []).find((v) => v.id === id)?.note ?? "";
</script>

<div class="flex h-screen flex-col bg-background text-txtmain">
  <header class="flex items-baseline gap-3 border-b border-card-border px-8 py-5">
    <h1 class="text-lg font-semibold tracking-tight">quartermaster</h1>
    <span class="text-label text-txtsecondary">First-time setup</span>
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
            {gb(status.downloaded / 1e9)} of {gb(status.total / 1e9)}
          </p>
        {/if}
      {/if}

      {#if failed}
        <p class="mt-3 text-sm text-error">{status.error}</p>
      {/if}

      {#if done}
        <p class="mt-2 text-sm text-txtsecondary">
          quartermaster is installed in
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
        Start quartermaster now
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
        <h2 class="text-base font-semibold">Where should quartermaster go?</h2>
        <p class="mt-1 text-sm text-txtsecondary">
          This holds the application itself — a few hundred megabytes. Models live
          somewhere else and are never copied here.
        </p>
        <input class="mt-4 w-full font-mono text-sm" bind:value={dir} spellcheck="false" />

        {#if isWindows}
          <label class="mt-5 flex items-center gap-2 text-sm">
            <input type="checkbox" bind:checked={autostart} />
            Start quartermaster when I log in
          </label>
        {/if}
      {:else if step === 1}
        <h2 class="text-base font-semibold">Where are your models?</h2>
        <p class="mt-1 text-sm text-txtsecondary">
          Point this at a folder of GGUF files. quartermaster reads it in place and
          writes nothing to it. You can leave it blank and set it later.
        </p>
        <input
          class="mt-4 w-full font-mono text-sm"
          bind:value={modelsRoot}
          spellcheck="false"
          placeholder="e.g. D:/LLM/Models"
        />

        <p class="mt-2 h-5 text-xs text-txtsecondary">
          {#if scanning}
            Scanning…
          {:else if scan?.error}
            <span class="text-warning">{scan.error}</span>
          {:else if scan && !scan.exists}
            <span class="text-warning">That folder does not exist yet.</span>
          {:else if scan}
            <span class="text-success">
              Found {scan.count}
              {scan.count === 1 ? "model" : "models"} ({gb(scan.sizeGB)}).
            </span>
          {/if}
        </p>
      {:else}
        <h2 class="text-base font-semibold">Which engines should I install?</h2>
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
            No GPU detected — CPU builds will be used.
          {/if}
        </p>
        <select class="mt-2 w-full text-sm" bind:value={variant}>
          {#each probe.variants ?? [] as v}
            <option value={v.id}>{v.label}</option>
          {/each}
        </select>
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
