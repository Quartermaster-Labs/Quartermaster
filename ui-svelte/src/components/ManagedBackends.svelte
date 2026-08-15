<script lang="ts">
  // Managed backend installs — download an inference-server build straight from
  // its upstream GitHub release, keep several versions side by side, switch
  // between them, and remove them.
  //
  // This sits ABOVE the manual backend registry in Settings, it does not replace
  // it: three of Quartermaster's backends (tts-server, sam3-server, the
  // ROCm-patched sd-server) have no downloadable upstream release, so a
  // hand-entered path stays a first-class way to register a backend. A managed
  // install just writes the same registry row for you and keeps it updated.
  import { onMount, onDestroy } from "svelte";
  import { Download, RefreshCw, Trash2, Check, ExternalLink, Star, Plus, Pencil } from "lucide-svelte";
  import {
    getBackendCatalog,
    getBackendReleases,
    BackendApiError,
    installBackend,
    getBackendJobs,
    activateBackend,
    uninstallBackend,
    makeBackendDefault,
    getBackendSources,
    deleteBackendSource,
    resolveBackendAsset,
    type BackendCatalog,
    type ManagedComponent,
    type BackendJob,
    type BackendRelease,
    type BackendSource,
    type BackendResolved,
  } from "../stores/api";
  import { backendClass } from "../lib/backends";
  import TrackRepoModal from "./TrackRepoModal.svelte";

  // Called after an install/activate/uninstall so the parent can re-read the
  // registry it renders below us.
  let { onchanged }: { onchanged?: () => void } = $props();

  let catalog = $state<BackendCatalog | null>(null);
  let jobs = $state<BackendJob[]>([]);
  let err = $state<string | null>(null);
  let available = $state(true); // false when the server has no manager (501)

  // Per-component UI state, keyed by component id.
  let variantSel = $state<Record<string, string>>({});
  let versionSel = $state<Record<string, string>>({}); // "" => newest stable
  let releases = $state<Record<string, BackendRelease[]>>({});
  let loadingRel = $state<Record<string, boolean>>({});
  let busy = $state<Record<string, boolean>>({}); // activate/uninstall in flight

  // Tracked repos: the editable side of a custom component. The catalog renders
  // them as ordinary cards; this is only what the edit form needs back.
  let sources = $state<BackendSource[]>([]);
  let editing = $state<BackendSource | null>(null);
  let adding = $state(false);
  // What each custom variant would download right now. A derived pattern is not
  // something a user can judge, but a file name is — so the card shows the
  // resolved asset instead of the rule behind it.
  let resolved = $state<Record<string, BackendResolved>>({});

  const activeJob = (id: string): BackendJob | undefined =>
    jobs.find((j) => j.component === id && j.phase !== "done" && j.phase !== "error");
  const lastJob = (id: string): BackendJob | undefined => jobs.find((j) => j.component === id);

  async function refresh(): Promise<void> {
    try {
      const c = await getBackendCatalog();
      catalog = c;
      jobs = c.jobs;
      for (const comp of c.components) {
        // Preselect the flavour that matches this host's GPU until the user picks.
        if (!variantSel[comp.id]) variantSel[comp.id] = comp.active?.variant || comp.suggested;
        if (versionSel[comp.id] === undefined) versionSel[comp.id] = "";
      }
      // Check for updates automatically, but only for things already installed —
      // that's the question the user actually has on opening this tab, and the
      // server caches release listings for 10 minutes so reopening is free.
      for (const comp of c.components) {
        if (comp.installed.length) void loadReleases(comp);
      }
      try {
        sources = await getBackendSources();
      } catch {
        // No -generate control file: the built-in catalog still works, there is
        // just nowhere to persist a tracked repo. Leave the list empty.
        sources = [];
      }
      err = null;
    } catch (e) {
      // Only a 501 means this server genuinely has no manager, which is the one
      // case where hiding the section is right. Anything else is a fault the
      // user needs to see — silently blanking the tab looks like the feature was
      // never built.
      if (e instanceof BackendApiError && e.status === 501) {
        available = false;
        return;
      }
      err = e instanceof Error ? e.message : String(e);
      console.warn("backend catalog failed", e);
    }
  }

  // Poll only while something is installing — the rest of the time the catalog
  // is static, so an idle settings tab makes no requests.
  let timer: ReturnType<typeof setInterval> | undefined;
  function ensurePolling(): void {
    if (timer) return;
    timer = setInterval(async () => {
      try {
        const next = await getBackendJobs();
        const wasRunning = jobs.some((j) => j.phase !== "done" && j.phase !== "error");
        jobs = next;
        const running = next.some((j) => j.phase !== "done" && j.phase !== "error");
        if (!running) {
          clearInterval(timer);
          timer = undefined;
          if (wasRunning) {
            await refresh();
            onchanged?.();
          }
        }
      } catch {
        // A transient poll failure is not worth surfacing; the next tick retries.
      }
    }, 1200);
  }

  onMount(refresh);
  onDestroy(() => clearInterval(timer));

  // Lazily list upstream releases the first time a component's version picker is
  // used, and on demand for "check for updates".
  async function loadReleases(comp: ManagedComponent, force = false): Promise<void> {
    if (loadingRel[comp.id]) return;
    if (releases[comp.id] && !force) return;
    loadingRel[comp.id] = true;
    err = null;
    try {
      releases[comp.id] = await getBackendReleases(comp.id, force);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      loadingRel[comp.id] = false;
    }
  }

  async function install(comp: ManagedComponent, version?: string): Promise<void> {
    err = null;
    try {
      await installBackend(comp.id, variantSel[comp.id] ?? "", version ?? versionSel[comp.id] ?? "");
      jobs = await getBackendJobs();
      ensurePolling();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }

  async function activate(comp: ManagedComponent, version: string, variant: string): Promise<void> {
    busy[comp.id] = true;
    err = null;
    try {
      await activateBackend(comp.id, version, variant);
      await refresh();
      onchanged?.();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy[comp.id] = false;
    }
  }

  async function makeDefault(comp: ManagedComponent): Promise<void> {
    busy[comp.id] = true;
    err = null;
    try {
      await makeBackendDefault(comp.id);
      await refresh();
      onchanged?.();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy[comp.id] = false;
    }
  }

  async function remove(comp: ManagedComponent, version: string, variant: string): Promise<void> {
    busy[comp.id] = true;
    err = null;
    try {
      await uninstallBackend(comp.id, version, variant);
      await refresh();
      onchanged?.();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy[comp.id] = false;
    }
  }

  // --- tracked repos ---

  const sourceFor = (id: string): BackendSource | undefined => sources.find((s) => s.id === id);

  async function stopTracking(comp: ManagedComponent): Promise<void> {
    busy[comp.id] = true;
    err = null;
    try {
      await deleteBackendSource(comp.id);
      await refresh();
      onchanged?.();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy[comp.id] = false;
    }
  }

  // Ask the server what the current selection would actually download. Keyed by
  // component+variant so switching the flavour picker re-previews.
  async function previewAsset(comp: ManagedComponent, variant: string, version: string): Promise<void> {
    const key = `${comp.id}/${variant}/${version}`;
    if (resolved[key]) return;
    try {
      resolved[key] = await resolveBackendAsset(comp.id, variant, version);
    } catch {
      // Offline or rate-limited: the card just doesn't show a preview line.
    }
  }

  function fmtBytes(n: number): string {
    if (!n) return "";
    const mb = n / (1024 * 1024);
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
    // Round to a whole MB only once there is a whole MB to round to, so a small
    // helper binary reads "0.4 MB" instead of "0 MB".
    return mb >= 10 ? `${Math.round(mb)} MB` : `${mb.toFixed(1)} MB`;
  }

  function progress(j: BackendJob): number {
    if (j.phase !== "downloading" || !j.total) return 0;
    return Math.min(100, Math.round((j.downloaded / j.total) * 100));
  }

  function phaseLabel(j: BackendJob): string {
    switch (j.phase) {
      case "resolving":
        return "Finding the release…";
      case "downloading":
        return j.total
          ? `Downloading ${fmtBytes(j.downloaded)} of ${fmtBytes(j.total)}`
          : `Downloading ${fmtBytes(j.downloaded)}`;
      case "extracting":
        return "Unpacking…";
      case "registering":
        return "Registering…";
      case "done":
        return `Installed ${j.version}`;
      case "error":
        return j.error ?? "Install failed";
    }
  }

  // Latest tag we know about; drives the "update available" hint.
  function newerAvailable(comp: ManagedComponent): string | null {
    const rels = releases[comp.id];
    if (!rels?.length || !comp.active) return null;
    const newest = rels.find((r) => !r.prerelease) ?? rels[0];
    return newest && newest.tag !== comp.active.version ? newest.tag : null;
  }

  // The single install button names exactly what it will do with the current
  // picker selection, so there is no need for a second "latest" button.
  function installLabel(comp: ManagedComponent, upd: string | null): string {
    const pinned = versionSel[comp.id];
    if (pinned) return `Install ${pinned}`;
    if (upd) return `Update to ${upd}`;
    return comp.installed.length ? "Reinstall latest" : "Install latest";
  }

  // Group the catalog by what a backend is FOR, not by project name — by the
  // registry *class*, not the kind, so llama.cpp and vLLM share one tab. That is
  // the honest grouping: a class is exactly the set of engines competing for one
  // ★ default. Unknown kinds land in "Other" rather than vanishing, so adding a
  // catalog entry needs no change here.
  const GROUP_LABELS: Record<string, string> = {
    llm: "Text",
    image: "Image",
    upscale: "Upscale",
    tts: "Speech",
    asr: "Transcription",
    segment: "Segmentation",
    tools: "Tools", // helper binaries (yt-dlp) — never registered as a backend
    custom: "Other",
  };
  const groupOf = (kind: string): string => (kind === "" ? "tools" : backendClass(kind));
  const groupLabel = (id: string): string => GROUP_LABELS[id] ?? id;

  type Group = { id: string; label: string; comps: ManagedComponent[] };

  let groups = $derived.by<Group[]>(() => {
    const by = new Map<string, ManagedComponent[]>();
    for (const c of catalog?.components ?? []) {
      const id = groupOf(c.kind);
      const list = by.get(id);
      if (list) list.push(c);
      else by.set(id, [c]);
    }
    return [...by].map(([id, comps]) => ({ id, label: groupLabel(id), comps }));
  });

  let tab = $state<string | null>(null);
  // Keep the selection valid across refreshes without fighting the user: only
  // fall back to the first tab when the current one no longer exists.
  let activeTab = $derived(groups.some((g) => g.id === tab) ? tab! : (groups[0]?.id ?? ""));
  let shown = $derived(groups.find((g) => g.id === activeTab)?.comps ?? []);

  // A dot on the tab so an update in a group you are not looking at is still
  // visible. Only installed components are checked for updates, so this is only
  // ever true for a group the user actually uses.
  const groupHasUpdate = (g: Group): boolean => g.comps.some((c) => newerAvailable(c) !== null);
</script>

{#if available && catalog}
  <div class="mb-6">
    <div class="flex items-baseline gap-2 mb-1">
      <h6 class="!pb-0">Install a backend</h6>
      <span class="font-mono text-[0.65rem] text-txtsecondary truncate">{catalog.root}</span>
      <!-- Anything that publishes release assets can be installed the same way
           as the built-ins: pick a build from a real release, and the matching
           rule is worked out from it. -->
      <button
        type="button"
        class="btn btn--sm ml-auto shrink-0 inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary"
        title="Install builds from a GitHub repo that isn't in the list"
        onclick={() => {
          editing = null;
          adding = true;
        }}><Plus size={12} /> Track a repo</button
      >
    </div>
    <p class="text-[0.7rem] text-txtsecondary mb-3">
      Downloads the build from its upstream release and registers it below. Every version you install is kept, so you
      can switch back at any time.
      {#if catalog.gpus.length}
        <span class="text-txtmain">Detected: {catalog.gpus.join(", ")}.</span>
      {/if}
    </p>

    {#if groups.length > 1}
      <div class="flex flex-wrap items-center gap-1 mb-3 border-b border-card-border">
        {#each groups as g (g.id)}
          <button
            type="button"
            class="relative -mb-px px-3 py-1.5 text-[0.7rem] uppercase tracking-wide border-b-2 transition-colors {g.id ===
            activeTab
              ? 'border-primary text-primary'
              : 'border-transparent text-txtsecondary hover:text-txtmain'}"
            onclick={() => (tab = g.id)}
          >
            {g.label}
            <span class="font-mono text-[0.6rem] text-txtsecondary">{g.comps.length}</span>
            {#if groupHasUpdate(g)}
              <span class="absolute top-1 right-1 h-1.5 w-1.5 rounded-full bg-primary" title="Update available"></span>
            {/if}
          </button>
        {/each}
      </div>
    {/if}

    <div class="flex flex-col gap-3">
      {#each shown as comp (comp.id)}
        {@const job = activeJob(comp.id)}
        {@const last = lastJob(comp.id)}
        {@const upd = newerAvailable(comp)}
        <section class="rounded-md border border-card-border bg-surface/40">
          <header class="flex items-center gap-2 px-3 py-2 border-b border-card-border">
            <span class="text-[0.8125rem] text-txtmain">{comp.name}</span>
            {#if comp.active}
              <span class="font-mono text-[0.6rem] rounded px-1.5 py-0.5 border border-primary text-primary">
                {comp.active.version} · {comp.active.variant}
              </span>
            {:else if comp.installed.length}
              <span class="font-mono text-[0.6rem] rounded px-1.5 py-0.5 border border-card-border text-txtsecondary">
                installed, not active
              </span>
            {:else if comp.manual}
              <span class="font-mono text-[0.6rem] rounded px-1.5 py-0.5 border border-card-border text-txtsecondary">
                manual setup
              </span>
            {/if}
            {#if upd}
              <span class="font-mono text-[0.6rem] text-primary">update: {upd}</span>
            {:else if comp.active && releases[comp.id]}
              <span class="font-mono text-[0.6rem] text-txtsecondary">up to date</span>
            {/if}
            {#if comp.custom}
              <span
                class="font-mono text-[0.6rem] rounded px-1.5 py-0.5 border border-card-border text-txtsecondary"
                title="A repo you added"
              >tracked</span>
              <button
                type="button"
                class="ml-auto shrink-0 p-1 rounded text-txtsecondary hover:text-primary"
                title="Edit this tracked repo"
                aria-label="Edit this tracked repo"
                onclick={() => {
                  editing = sourceFor(comp.id) ?? null;
                  adding = !!editing;
                }}><Pencil size={13} /></button
              >
              <button
                type="button"
                class="shrink-0 p-1 rounded text-txtsecondary hover:text-error disabled:opacity-40"
                title={comp.installed.length
                  ? "Remove its installed builds before you can stop tracking it"
                  : "Stop tracking this repo"}
                aria-label="Stop tracking this repo"
                disabled={!!busy[comp.id] || comp.installed.length > 0}
                onclick={() => stopTracking(comp)}><Trash2 size={13} /></button
              >
            {/if}
            <a
              href={`https://github.com/${comp.repo}`}
              target="_blank"
              rel="noreferrer"
              class="shrink-0 text-txtsecondary hover:text-primary {comp.custom ? '' : 'ml-auto'}"
              title={comp.repo}
              aria-label={`Open ${comp.repo} on GitHub`}
            ><ExternalLink size={13} /></a>
          </header>

          <div class="px-3 py-2.5 flex flex-col gap-2">
            <p class="text-[0.7rem] text-txtsecondary">{comp.blurb}</p>

            {#if comp.manual}
              <!-- An engine we can drive but not install: upstream ships no
                   self-contained executable (vLLM publishes Python wheels). Say
                   how to get it instead of rendering an install button that
                   could only ever fail. -->
              <p class="text-[0.7rem] text-txtsecondary">{comp.setup}</p>
              <span class="font-mono text-[0.6rem] text-txtsecondary">Add its path in the backend list below.</span>
            {:else if !comp.supported}
              <p class="text-[0.7rem] text-txtsecondary">No build is published for {catalog.os}.</p>
            {:else}
              <!-- One control row: the two pickers already default to the
                   detected GPU flavour and the newest release, so the single
                   button below them is the one-click path AND the pin-a-specific-
                   build path. A separate "install latest" button was the same
                   action twice. -->
              <div class="flex flex-wrap items-center gap-2 font-mono text-xs">
                {#if comp.variants.length > 1}
                  <select
                    bind:value={variantSel[comp.id]}
                    title="Build flavour - auto-selected from your GPU"
                    class="w-40 shrink-0 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
                  >
                    {#each comp.variants.filter((v) => v.available) as v (v.id)}
                      <option value={v.id} title={v.note ?? ""}>
                        {v.label}{v.id === comp.suggested ? " (detected)" : ""}
                      </option>
                    {/each}
                  </select>
                {/if}

                <select
                  bind:value={versionSel[comp.id]}
                  onfocus={() => loadReleases(comp)}
                  title="Version - leave on latest unless you need a specific build"
                  class="w-44 shrink-0 rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
                >
                  <option value="">Latest release</option>
                  {#each releases[comp.id] ?? [] as r (r.tag)}
                    <option value={r.tag}>{r.tag}{r.prerelease ? " (pre-release)" : ""}</option>
                  {/each}
                </select>

                <button
                  type="button"
                  class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide border-primary text-primary hover:bg-primary/10 disabled:opacity-50"
                  disabled={!!job}
                  title="Download the selected flavour and version, then switch to it"
                  onclick={() => install(comp)}
                ><Download size={12} /> {installLabel(comp, upd)}</button>

                <button
                  type="button"
                  class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary"
                  title="Re-check upstream for new releases"
                  disabled={!!loadingRel[comp.id]}
                  onclick={() => loadReleases(comp, true)}
                ><RefreshCw size={12} class={loadingRel[comp.id] ? "animate-spin" : ""} /> Check</button>
              </div>

              {#if comp.custom}
                <!-- The rule that decides which file gets downloaded was derived
                     from an example, never written by hand, so showing it would
                     be showing a regex nobody chose. Show the file it currently
                     picks instead — that is the thing a user can check. -->
                {@const key = `${comp.id}/${variantSel[comp.id] ?? ""}/${versionSel[comp.id] ?? ""}`}
                {@const res = resolved[key]}
                <div class="font-mono text-[0.65rem] text-txtsecondary">
                  {#if res?.asset}
                    Downloads <span class="text-txtmain">{res.asset}</span> from {res.tag}
                  {:else if res?.closest}
                    Nothing matches in {res.tag} — closest is <span class="text-txtmain">{res.closest}</span>. Edit this
                    repo and re-pick the build.
                  {:else if res?.error}
                    {res.error}
                  {:else}
                    <button type="button" class="underline hover:text-primary" onclick={() => previewAsset(comp, variantSel[comp.id] ?? "", versionSel[comp.id] ?? "")}
                      >Show what this would download</button
                    >
                  {/if}
                </div>
              {/if}

              {#if job}
                <div class="flex flex-col gap-1">
                  <div class="h-1.5 rounded bg-card-border overflow-hidden">
                    <div class="h-full bg-primary transition-[width]" style={`width:${progress(job)}%`}></div>
                  </div>
                  <span class="font-mono text-[0.65rem] text-txtsecondary">{phaseLabel(job)}</span>
                </div>
              {:else if last?.phase === "error"}
                <span class="font-mono text-[0.65rem] text-error">{last.error}</span>
              {/if}

              <!-- Installing does not take ★ from a backend the user registered
                   earlier, so say plainly when a managed build is on disk but
                   not the thing that actually gets launched. -->
              {#if comp.installed.length && comp.kind !== "" && !comp.isDefault}
                <div class="flex flex-wrap items-center gap-2 rounded border border-card-border bg-surface/60 px-2 py-1.5">
                  <span class="text-[0.7rem] text-txtsecondary">
                    {#if comp.defaultOwner}
                      Installed, but not in use - <span class="text-txtmain font-mono">{comp.defaultOwner}</span> is the
                      default for this group.
                    {:else}
                      Installed, but not in use - no default is set for this group.
                    {/if}
                  </span>
                  <button
                    type="button"
                    class="btn btn--sm ml-auto inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary"
                    title="Make this the ★ auto-pick for its group. Models pinned to another backend keep their pin."
                    disabled={!!busy[comp.id]}
                    onclick={() => makeDefault(comp)}
                  ><Star size={12} /> Use by default</button>
                </div>
              {/if}

              {#if comp.installed.length}
                <div class="flex flex-col divide-y divide-card-border border-t border-card-border -mx-3 mt-1">
                  <!-- A helper binary's "in use" is derived (newest install wins),
                       not a registry pointer, so removing it strands nothing —
                       only a real active build is protected from deletion. -->
                  {#each comp.installed as b (b.version + b.variant)}
                    <div class="flex items-center gap-2 px-3 py-1.5 font-mono text-[0.7rem]">
                      <span class="text-txtmain">{b.version}</span>
                      <span class="text-txtsecondary">{b.variant}</span>
                      <span class="text-txtsecondary">{fmtBytes(b.sizeBytes)}</span>
                      {#if b.active}
                        <span class="ml-auto inline-flex items-center gap-1 text-primary"><Check size={12} /> in use</span>
                      {:else if comp.kind !== ""}
                        <button
                          type="button"
                          class="ml-auto btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
                          title="Use this build"
                          disabled={!!busy[comp.id]}
                          onclick={() => activate(comp, b.version, b.variant)}
                        >Use</button>
                      {:else}
                        <!-- A helper binary has no registry row to point at a
                             build, so there is nothing to activate: the newest
                             install is simply the one that gets used. -->
                        <span class="ml-auto text-txtsecondary">superseded</span>
                      {/if}
                      <button
                        type="button"
                        title={b.active && comp.kind !== ""
                          ? "Switch to another version before removing this one"
                          : "Remove this build"}
                        aria-label="Remove this build"
                        class="shrink-0 p-1 rounded border border-transparent text-txtsecondary hover:text-error hover:border-error transition-colors disabled:opacity-40"
                        disabled={(b.active && comp.kind !== "") || !!busy[comp.id]}
                        onclick={() => remove(comp, b.version, b.variant)}
                      ><Trash2 size={13} /></button>
                    </div>
                  {/each}
                </div>
              {/if}
            {/if}
          </div>
        </section>
      {/each}
    </div>

    {#if err}
      <p class="mt-2 font-mono text-[0.65rem] text-error">{err}</p>
    {/if}
  </div>

  {#if adding}
    <TrackRepoModal
      os={catalog.os}
      source={editing}
      onclose={() => {
        adding = false;
        editing = null;
      }}
      onsaved={async () => {
        // A saved source changes the catalog, and its patterns changed with it.
        resolved = {};
        await refresh();
        onchanged?.();
      }}
    />
  {/if}
{:else if available && err}
  <!-- The catalog never loaded. Say so instead of rendering nothing, which is
       indistinguishable from the feature not existing. -->
  <div class="mb-6">
    <h6>Install a backend</h6>
    <p class="mt-1 font-mono text-[0.65rem] text-error">{err}</p>
  </div>
{/if}
