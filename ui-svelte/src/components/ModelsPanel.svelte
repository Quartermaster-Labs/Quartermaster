<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { push } from "svelte-spa-router";
  import { get } from "svelte/store";
  import { FolderOpen, Layers, MoreVertical, X } from "lucide-svelte";
  import { models, loadModel, getSettings, pickModelsFolder, pickLoraFolder, getModelDeletePlan, deleteModelFiles } from "../stores/api";
  import { askConfirm, notify } from "../lib/confirm";
  import { persistentStore } from "../stores/persistent";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { isNative } from "../lib/native";
  import { openTab } from "../stores/appTabs";
  import { modelCategory, MODEL_CATEGORIES, playgroundTarget, type ModelCategory } from "../lib/modelUtils";
  import { nextSort, fmtGB, type SortDir, type SortKey, type StateFilter } from "../lib/modelTable";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import ModelsTable from "./ModelsTable.svelte";

  // The route's category is the INITIAL tab only — switching tabs from here is
  // page-local state, so it doesn't push a history entry per click.
  let { category = "llm" as ModelCategory }: { category?: ModelCategory } = $props();
  // Seeded from the prop's own default, then reconciled by the effect below on
  // its first run — reading `category` here would only capture its initial value.
  let tab = $state<ModelCategory>("llm");
  let lastRouteCategory = $state<ModelCategory>("llm");
  $effect(() => {
    // Follow the route only when it actually changed (a deep link / sidebar
    // click), never over the user's own tab pick.
    if (category !== lastRouteCategory) {
      lastRouteCategory = category;
      tab = category;
    }
  });

  let pendingLoads = $state<Record<string, boolean>>({});
  const loadControllers = new Map<string, AbortController>();

  // Per-model config editor (cogwheel).
  let configModelId = $state<string | null>(null);
  let configOpenFor = $state("");
  let configOpen = $state(false);
  function openConfig(family: string, openFor = ""): void {
    configModelId = family;
    configOpenFor = openFor || family;
    configOpen = true;
  }

  const showUnlistedStore = persistentStore<boolean>("showUnlisted", true);
  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "name");
  const sortKeyStore = persistentStore<SortKey>("modelsSortKey", "name");
  const sortDirStore = persistentStore<SortDir>("modelsSortDir", "asc");
  // Favourites are keyed by ROW (the base model), not by model id: pinning is a
  // statement about the model, and it must survive switching quant or variant.
  const favoritesStore = persistentStore<string[]>("modelsFavorites", []);
  function toggleFavorite(key: string): void {
    favoritesStore.update((f) => (f.includes(key) ? f.filter((k) => k !== key) : [...f, key]));
  }

  // Search lives in the table's Model header (the column it filters); the table
  // owns the expand/collapse, this is just the value.
  let search = $state("");
  let stateFilter = $state<StateFilter>("all");

  // Ascending → descending → off, so the catalog's own order is reachable
  // without a reset button. A different column always starts over at ascending.
  function onSort(key: SortKey): void {
    const next = nextSort(get(sortKeyStore), get(sortDirStore), key);
    sortKeyStore.set(next.key);
    sortDirStore.set(next.dir);
  }

  // Per-category scan folder (folder icon in the toolbar), and beside it the
  // per-category LoRA folder. Both are model-LOCATION questions and both are
  // per-category, so they belong together here rather than one here and one
  // buried in Settings -> Backends, which is a page about executables.
  let folderPath = $state("");
  // Whether THIS tab owns a folder, as opposed to inheriting the shared root.
  // folderPath alone cannot say: it is filled with the effective path either way.
  let folderOwn = $state(false);
  let loraPath = $state("");
  let picking = $state(false);
  // The three categories with a LoRA concept. The folder means two different
  // things across them, which is why the tooltip below is derived rather than
  // fixed:
  //   image/video - sd-server's --lora-model-dir, a whole directory handed to
  //     the backend, which lists it and lets each REQUEST pick from it.
  //   llm - a base path only. llama.cpp has no directory flag; each adapter is
  //     named at launch, per model, in the config editor. Setting this folder
  //     emits nothing on its own - it is what those per-model entries resolve
  //     bare filenames against, and what the editor's picker lists.
  // Every other tab's backend has no LoRA concept at all, so the control would
  // be a setting that reaches nothing.
  const LORA_TABS: ModelCategory[] = ["llm", "image", "video"];
  const hasLora = $derived(LORA_TABS.includes(tab));
  const loraTip = $derived(
    tab === "llm"
      ? `LoRA folder: ${loraPath || "the fleet-wide default"} - adapters here can be attached to a model in its config editor`
      : `LoRA folder: ${loraPath || "the fleet-wide default"} - click to choose`,
  );
  async function refreshFolder(): Promise<void> {
    try {
      const s = await getSettings();
      folderPath = s.categoryRoots?.[tab] || s.modelsRoot || "";
      folderOwn = Boolean(s.categoryRoots?.[tab]);
      loraPath = s.loraDirs?.[tab] || "";
    } catch {
      folderPath = "";
      folderOwn = false;
      loraPath = "";
    }
  }
  $effect(() => {
    tab; // re-run when the tab changes
    refreshFolder();
  });
  // clear=true skips the dialog and drops back to the shared models folder.
  // Re-picking can only ever REPLACE a path, so without this an inherited
  // default is unreachable once a folder has been chosen once.
  async function pickFolder(clear = false): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const path = await pickModelsFolder(tab, clear);
      // null => user cancelled; regen+reload already ran. A clear returns "",
      // so re-read rather than assign: the effective path becomes the shared root.
      if (path !== null) await refreshFolder();
    } catch (e) {
      console.error(e);
    } finally {
      picking = false;
    }
  }

  // clear=true skips the dialog and drops back to the fleet-wide LoRA folder.
  // Re-picking can only ever REPLACE a path, so without this an inherited
  // default is unreachable once a folder has been chosen once.
  async function pickLora(clear = false): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const path = await pickLoraFolder(tab, clear);
      if (path !== null) loraPath = path; // null => user cancelled
    } catch (e) {
      console.error(e);
    } finally {
      picking = false;
    }
  }

  // ---- Toolbar overflow --------------------------------------------------
  // The table-wide controls used to wrap onto a second row the moment the window
  // narrowed, which read as a broken toolbar. Instead they fold into a "..." menu.
  //
  // The test is "do the tabs and the FULL control set fit on one line", not
  // "is the bar overflowing": measuring the live bar would flip back the instant
  // folding freed the space, and then overflow again - a permanent flicker.
  // `ctrlFull` is only ever sampled while expanded, so the threshold is a fixed
  // number and the decision has no feedback loop.
  let barEl = $state<HTMLElement | undefined>();
  let tabsEl = $state<HTMLElement | undefined>();
  let ctrlEl = $state<HTMLElement | undefined>();
  let compact = $state(false);
  let menuOpen = $state(false);
  let ctrlFull = 0;

  // Tailwind v4 needs an @reference to resolve utilities inside a component
  // <style>, so shared row styling lives in a constant instead of a class.
  const MENU_ROW =
    "w-full flex items-center gap-2 px-3 py-1.5 text-left text-txtsecondary transition-colors " +
    "hover:bg-secondary hover:text-txtmain hover:cursor-pointer " +
    "disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-txtsecondary";

  function measureBar(): void {
    if (!barEl || !tabsEl || !ctrlEl) return;
    if (!compact) ctrlFull = ctrlEl.offsetWidth;
    if (!ctrlFull) return;
    let tabsW = 0;
    for (const c of tabsEl.children) tabsW += (c as HTMLElement).offsetWidth;
    tabsW += 4 * Math.max(0, tabsEl.children.length - 1); // gap-x-1
    const next = tabsW + ctrlFull + 32 > barEl.clientWidth; // 32 = px-3 gutters + breathing room
    if (next !== compact) {
      compact = next;
      if (!next) menuOpen = false;
    }
  }

  $effect(() => {
    if (!barEl) return;
    const ro = new ResizeObserver(measureBar);
    ro.observe(barEl);
    measureBar();
    return () => ro.disconnect();
  });
  // Tab labels carry counts, so the tabs' natural width changes as models appear.
  $effect(() => {
    $models;
    queueMicrotask(measureBar);
  });
  // The CONTROL set is not fixed either: the LoRA buttons exist only on the
  // image/video tabs, and either clear button only once a folder is set. ctrlFull
  // is sampled inside measureBar, which resize and $models alone would not
  // re-run here, so switching to a tab with more controls would overflow into
  // the second row this whole mechanism exists to prevent.
  $effect(() => {
    hasLora;
    folderOwn;
    loraPath;
    queueMicrotask(measureBar);
  });

  $effect(() => {
    if (!menuOpen) return;
    const onDown = (e: PointerEvent): void => {
      if (e.target instanceof Node && ctrlEl?.contains(e.target)) return;
      menuOpen = false;
    };
    window.addEventListener("pointerdown", onDown, true);
    return () => window.removeEventListener("pointerdown", onDown, true);
  });

  // See playgroundTarget in modelUtils: category -> tab + button label, or null
  // for models with no interactive UI (embed/segment/3D/rerankers).
  function playable(m: Model): boolean {
    return playgroundTarget(m) !== null;
  }

  function playgroundTab(m: Model): string {
    return playgroundTarget(m)?.tab ?? "chat";
  }

  function playLabel(m: Model): string {
    return playgroundTarget(m)?.label ?? "Chat";
  }

  // Playground is a separate app on its own port — open it with the model + tab
  // as launch params (different origin, so stores can't be shared directly).
  function chatWith(m: Model): void {
    const port = get(playgroundPort);
    if (!port) {
      push("/test"); // no playground port configured — show the stub
      return;
    }
    const u = `${window.location.protocol}//${window.location.hostname}:${port}/ui/?model=${encodeURIComponent(m.id)}&tab=${playgroundTab(m)}`;
    // In the app window this is a tab, not a trip to the system browser --
    // window.open is routed to openExternal there (lib/native.ts).
    if (isNative) {
      openTab(u);
      return;
    }
    window.open(u, "_blank", "noopener");
  }

  // Kick off the load (non-blocking) AND jump to the playground.
  function chatAndLoad(m: Model): void {
    handleLoadModel(m.id);
    chatWith(m);
  }

  // Trash button: ask the server what the delete would remove (the same
  // function the DELETE runs), spell it out, and only then remove.
  const baseName = (p: string) => p.split(/[\\/]/).pop() ?? p;
  const gb = (bytes: number) => `${fmtGB(bytes / 2 ** 30)} GB`;
  const fileList = (files: { path: string }[]) => files.map((f) => `• ${baseName(f.path)}`).join("\n");
  async function deleteModel(m: Model): Promise<void> {
    let plan;
    try {
      plan = await getModelDeletePlan(m.id);
    } catch (e) {
      await notify("Cannot delete this model", e instanceof Error ? e.message : String(e));
      return;
    }
    const parts = [`Deletes from disk (${gb(plan.bytes)}):\n${fileList(plan.files)}`];
    parts.push(`Removes from the catalog: ${plan.removes.join(", ")}`);
    if (plan.running?.length) parts.push(`Unloads first: ${plan.running.join(", ")}`);
    if (plan.usedBy?.length) parts.push(`Also named by ${plan.usedBy.join(", ")} (e.g. as a draft model), which will lose it.`);
    if (plan.kept?.length) parts.push(`Kept in the folder:\n${fileList(plan.kept)}`);
    parts.push("This cannot be undone.");
    const ok = await askConfirm({
      title: `Delete ${baseName(plan.files[0]?.path ?? m.id)}?`,
      body: parts.join("\n\n"),
      confirmLabel: `Delete ${gb(plan.bytes)}`,
      danger: true,
    });
    if (!ok) return;
    try {
      await deleteModelFiles(m.id);
    } catch (e) {
      await notify("Delete failed", e instanceof Error ? e.message : String(e));
    }
  }

  async function handleLoadModel(modelId: string): Promise<void> {
    if (pendingLoads[modelId]) return;
    const controller = new AbortController();
    loadControllers.set(modelId, controller);
    pendingLoads[modelId] = true;
    try {
      await loadModel(modelId, controller.signal);
    } catch (e) {
      console.error(e);
    } finally {
      loadControllers.delete(modelId);
      delete pendingLoads[modelId];
    }
  }

  function cancelLoad(modelId: string): void {
    loadControllers.get(modelId)?.abort();
  }

  let local = $derived($models.filter((m) => !m.peerID));
  let inTab = $derived(local.filter((m) => modelCategory(m) === tab));
  let peers = $derived($models.filter((m) => m.peerID));
  // Tab counts follow the unlisted toggle so the badge matches what the table
  // actually shows.
  function tabCount(id: ModelCategory): number {
    return local.filter((m) => modelCategory(m) === id && ($showUnlistedStore || !m.unlisted)).length;
  }

  let peersByPeerId = $derived(
    peers.reduce(
      (acc, m) => {
        const k = m.peerID || "unknown";
        (acc[k] ??= []).push(m);
        return acc;
      },
      {} as Record<string, Model[]>,
    ),
  );
</script>

<!-- Full-bleed page: the panel IS the page background, so the toolbar rule and
     the table both span edge to edge and only their contents are inset. -->
<div class="flex flex-col h-full">
  <!-- One toolbar: category tabs left, table-wide controls right. min-h-10 is the
       app's row unit - a sidebar item and the status rail are both h-10, so every
       page's first row lands on the same line as the chrome beside it. min-, not
       fixed: the tabs may wrap, and a hard height would clip the second line.
       items-stretch, so each tab fills that height and its underline sits on the
       row's bottom edge rather than at the bottom of its own text box.
       The tabs wrap rather than scroll — a scrollbar under them hides categories behind a drag —
       and the controls fold into a "..." menu instead of taking a row of their own. -->
  <div bind:this={barEl} class="flex items-stretch gap-x-1 px-3 min-h-10 border-b border-card-border shrink-0">
    <div bind:this={tabsEl} class="flex flex-wrap items-stretch gap-x-1 gap-y-1 min-w-0">
    {#each MODEL_CATEGORIES as c (c.id)}
      <button
        class="inline-flex items-center px-3 -mb-px border-b-2 font-mono text-xs uppercase tracking-wide transition-colors {tab === c.id
          ? 'border-primary text-txtmain'
          : 'border-transparent text-txtsecondary hover:text-txtmain'}"
        onclick={() => (tab = c.id)}
      >
        {c.label}
        <span class="ml-1.5 tabular-nums text-[0.65rem] text-txtsecondary">{tabCount(c.id)}</span>
      </button>
    {/each}
    </div>

    <!-- self-stretch + items-center, not a bottom pad: the row is as tall as the
         tab buttons, so stretching to it and centring inside is what actually
         puts the controls on the row's midline. A pad only guesses at it. -->
    <div bind:this={ctrlEl} class="ml-auto flex items-center gap-2 shrink-0 self-stretch">
      <div class="flex items-center rounded border border-card-border overflow-hidden">
        {#each [{ id: "all", label: "All" }, { id: "loaded", label: "Loaded" }, { id: "idle", label: "Idle" }] as f (f.id)}
          <button
            class="px-2.5 py-1 font-mono text-[0.65rem] uppercase tracking-wide transition-colors {stateFilter === f.id
              ? 'bg-primary text-white'
              : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/60'}"
            onclick={() => (stateFilter = f.id as StateFilter)}
          >
            {f.label}
          </button>
        {/each}
      </div>
      {#if compact}
        <!-- Same three controls, folded. Anchored to the button rather than the
             toolbar so it stays put when the tabs wrap to a second line. -->
        <!-- Bare glyph, not a .btn: it sits among framed controls but is the
             affordance for the frames that were folded away, so a second box
             around it read as one more control rather than as their handle.
             self-stretch gives its hit area the full toolbar row. -->
        <div class="relative self-stretch flex">
          <button
            class="flex items-center justify-center px-1.5 cursor-pointer text-txtsecondary transition-colors hover:text-txtmain"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label="Table options"
            onclick={() => (menuOpen = !menuOpen)}
            use:tip={"Table options"}
          >
            <MoreVertical class="w-4 h-4" />
          </button>
          {#if menuOpen}
            <div
              class="absolute right-0 top-full mt-1 z-30 w-56 rounded-md border border-card-border bg-surface shadow-xl py-1 text-sm"
              role="menu"
              tabindex="-1"
            >
              <button class={MENU_ROW} role="menuitem" disabled={picking} onclick={() => { menuOpen = false; pickFolder(); }}>
                <FolderOpen class="w-3.5 h-3.5 shrink-0" />
                <span class="flex flex-col items-start min-w-0">
                  <span>Models folder</span>
                  {#if folderPath}<span class="max-w-full truncate font-mono text-[0.6rem] text-txtsecondary">{folderPath}</span>{/if}
                </span>
              </button>
              {#if folderOwn}
                <button class={MENU_ROW} role="menuitem" disabled={picking} onclick={() => { menuOpen = false; pickFolder(true); }}>
                  <span class="w-3.5 shrink-0"></span>
                  <span>Use the shared models folder</span>
                </button>
              {/if}
              {#if hasLora}
                <button class={MENU_ROW} role="menuitem" disabled={picking} onclick={() => { menuOpen = false; pickLora(); }}>
                  <Layers class="w-3.5 h-3.5 shrink-0" />
                  <span class="flex flex-col items-start min-w-0">
                    <span>LoRA folder</span>
                    <span class="max-w-full truncate font-mono text-[0.6rem] text-txtsecondary">{loraPath || "default"}</span>
                  </span>
                </button>
                {#if loraPath}
                  <button class={MENU_ROW} role="menuitem" disabled={picking} onclick={() => { menuOpen = false; pickLora(true); }}>
                    <span class="w-3.5 shrink-0"></span>
                    <span>Use the default LoRA folder</span>
                  </button>
                {/if}
              {/if}
              <button
                class={MENU_ROW}
                role="menuitem"
                onclick={() => showIdorNameStore.update((p) => (p === "name" ? "id" : "name"))}
              >
                <span class="w-3.5 shrink-0"></span>
                <span>Show {$showIdorNameStore === "id" ? "names" : "ids"}</span>
              </button>
              <button class={MENU_ROW} role="menuitem" onclick={() => showUnlistedStore.update((p) => !p)}>
                <span class="w-3.5 shrink-0"></span>
                <span>{$showUnlistedStore ? "Hide unlisted" : "Show unlisted"}</span>
              </button>
            </div>
          {/if}
        </div>
      {:else}
        <button
          class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
          onclick={() => pickFolder()}
          disabled={picking}
          aria-label="Set models folder"
          use:tip={`Models folder${folderPath ? ": " + folderPath : ""} - click to choose`}
        >
          <FolderOpen class="w-3.5 h-3.5" />
        </button>
        {#if folderOwn}
          <button
            class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
            onclick={() => pickFolder(true)}
            disabled={picking}
            aria-label="Use the shared models folder"
            use:tip={"Drop this category's models folder and use the shared one"}
          >
            <X class="w-3.5 h-3.5" />
          </button>
        {/if}
        {#if hasLora}
          <button
            class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
            onclick={() => pickLora()}
            disabled={picking}
            aria-label="Set LoRA folder"
            use:tip={loraTip}
          >
            <Layers class="w-3.5 h-3.5" />
          </button>
          {#if loraPath}
            <button
              class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
              onclick={() => pickLora(true)}
              disabled={picking}
              aria-label="Use the default LoRA folder"
              use:tip={"Drop this category's LoRA folder and use the default"}
            >
              <X class="w-3.5 h-3.5" />
            </button>
          {/if}
        {/if}
        <button
          class="btn btn--sm uppercase tracking-wide"
          onclick={() => showIdorNameStore.update((p) => (p === "name" ? "id" : "name"))}
          use:tip={"Toggle id / name display"}
        >
          {$showIdorNameStore === "id" ? "ID" : "Name"}
        </button>
        <button class="btn btn--sm uppercase tracking-wide" onclick={() => showUnlistedStore.update((p) => !p)} use:tip={"Show or hide unlisted models"}>
          {$showUnlistedStore ? "Hide unlisted" : "Show unlisted"}
        </button>
      {/if}
    </div>
  </div>

  <ModelsTable
    models={inTab}
    bind:search
    {stateFilter}
    showUnlisted={$showUnlistedStore}
    display={$showIdorNameStore}
    sortKey={$sortKeyStore}
    sortDir={$sortDirStore}
    pending={pendingLoads}
    favorites={$favoritesStore}
    onFavorite={toggleFavorite}
    onLoad={handleLoadModel}
    onCancel={cancelLoad}
    onPlay={chatAndLoad}
    canPlay={playable}
    {playLabel}
    onConfig={openConfig}
    onDelete={deleteModel}
    {onSort}
  />

  <!-- Peer models: read-only, no local actions. -->
  {#if Object.keys(peersByPeerId).length > 0}
    <div class="shrink-0">
      <h3 class="mb-2">Peer models</h3>
      {#each Object.entries(peersByPeerId).sort(([a], [b]) => a.localeCompare(b)) as [peerId, peerModels] (peerId)}
        <div class="mb-2">
          <div class="font-mono text-xs uppercase tracking-wide text-txtsecondary mb-1">{peerId}</div>
          <div class="flex flex-wrap gap-2">
            {#each peerModels as m (m.id)}
              <span class="font-mono text-xs border border-card-border rounded px-2 py-1 text-txtmain {m.unlisted ? 'opacity-70' : ''}">{m.id}</span>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<ModelConfigModal modelId={configModelId} openForId={configOpenFor} open={configOpen} onclose={() => (configOpen = false)} />
