<script lang="ts">
  // Track a custom backend repo — the add/edit form behind "Track a repo" in
  // Settings → Backends.
  //
  // The one rule that shapes this whole component: the user never writes a
  // pattern. They paste a repo, we show a real release's assets, they tick the
  // file they want, and the server derives the matching rule from that example
  // (internal/backends/derive.go). Everything here is therefore a picker over
  // data fetched from GitHub, never a text field the user has to get right —
  // which is also why the form can't be filled in offline, and says so.
  import { onMount, untrack } from "svelte";
  import { X, Search, Plus, Trash2 } from "lucide-svelte";
  import {
    getBackendSourceAssets,
    saveBackendSource,
    type BackendSource,
    type BackendSourceAssets,
    type BackendSourceVariant,
  } from "../stores/api";
  import { BACKEND_CLASSES } from "../lib/backends";
  import Select, { type SelectOption } from "./Select.svelte";

  let {
    os,
    source = null,
    onclose,
    onsaved,
  }: {
    os: string;
    source?: BackendSource | null; // editing an existing tracked repo
    onclose: () => void;
    onsaved: () => void;
  } = $props();

  // The executable a build of each kind is expected to contain. Only a starting
  // point — a fork can rename its binary, so the field stays editable.
  const DEFAULT_EXE: Record<string, string> = {
    llama: "llama-server",
    vllm: "vllm",
    sd: "sd-server",
    tts: "tts-server",
    ttscpp: "tts-cli",
    asr: "parakeet-server",
    sam: "sam3_server",
    upscale: "realesrgan-ncnn-vulkan",
    custom: "server",
  };
  const exeFor = (kind: string): string => DEFAULT_EXE[kind] ?? "server";
  const withExt = (name: string): string => (os === "windows" && !name.endsWith(".exe") ? `${name}.exe` : name);

  // The form is seeded from the prop exactly once and owns its fields from then
  // on — the modal is mounted fresh per open, so there is no later `source` to
  // track. untrack() states that rather than leaving ten "only captures the
  // initial value" warnings for a reader to re-derive.
  const seed = untrack(() => source);

  let repo = $state(seed?.repo ?? "");
  let name = $state(seed?.name ?? "");
  let blurb = $state(seed?.blurb ?? "");
  let kind = $state(seed?.kind ?? "llama");
  let exe = $state(seed?.exe ?? withExt(exeFor("llama")));
  let exeTouched = $state(!!seed); // stop auto-filling once the user edits it
  let bare = $state(seed?.bare ?? false);

  let assets = $state<BackendSourceAssets | null>(null);
  let tag = $state(seed?.tag ?? "");
  let picked = $state<BackendSourceVariant[]>(seed?.variants ? structuredClone($state.snapshot(seed.variants)) : []);
  let showAll = $state(false);
  let loading = $state(false);
  let saving = $state(false);
  let err = $state<string | null>(null);

  // An asset list is the only way to pick anything, so load it up front when
  // editing an existing source.
  onMount(() => {
    if (source?.repo) void find();
  });

  function onKindChange(): void {
    if (!exeTouched) exe = withExt(exeFor(kind));
  }

  // Every engine of every class, flattened, with the class name as the group
  // heading — the <Select> equivalent of the <optgroup> this used to be.
  const kindOptions: SelectOption[] = BACKEND_CLASSES.flatMap((cls) =>
    cls.engines.map((eng) => ({ value: eng.kind, label: eng.label, detail: eng.hint, group: cls.label })),
  );

  async function find(nextTag = tag, refresh = false): Promise<void> {
    if (!repo.trim()) return;
    loading = true;
    err = null;
    try {
      assets = await getBackendSourceAssets(repo.trim(), nextTag, refresh);
      tag = assets.tag;
      if (!name.trim()) {
        const tail = assets.repo.split("/").pop() ?? assets.repo;
        name = tail;
      }
    } catch (e) {
      assets = null;
      err = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  const isPicked = (asset: string): boolean => picked.some((v) => v.asset === asset);
  const isExtra = (asset: string): boolean => picked.some((v) => (v.extras ?? []).includes(asset));

  function toggle(asset: string): void {
    if (isPicked(asset)) {
      picked = picked.filter((v) => v.asset !== asset);
      return;
    }
    // Label the build from its own file name; the server does the same when this
    // is left blank, and the user can rename it.
    picked = [...picked, { label: suggestLabel(asset), asset, extras: [] }];
  }

  // Mirror of backends.SuggestLabel — kept client-side only so a freshly ticked
  // row is named immediately instead of after a round trip.
  function suggestLabel(asset: string): string {
    let base = asset;
    for (const ext of [".tar.gz", ".tgz", ".zip", ".exe", ".7z"]) {
      if (base.toLowerCase().endsWith(ext)) {
        base = base.slice(0, -ext.length);
        break;
      }
    }
    const skip = /^(v?\d+|b\d+|r\d+|\d{8}|[0-9a-f]{7,40}|rc\d*|alpha\d*|beta\d*|pre\d*|bin|x64|amd64)$/i;
    const parts = base.split(/[-_.+]+/).filter((p) => p && !skip.test(p));
    if (!parts.length) return "default";
    return parts.slice(-4).join(" ");
  }

  // `picked` is deeply reactive, so mutating a row in place is enough — no
  // self-assignment needed to nudge it.
  function addExtra(v: BackendSourceVariant, asset: string): void {
    if (!asset) return;
    v.extras = [...(v.extras ?? []), asset];
  }

  function dropExtra(v: BackendSourceVariant, asset: string): void {
    v.extras = (v.extras ?? []).filter((e) => e !== asset);
  }

  // Assets not already spoken for, offered as companion files. This is the
  // separately-shipped-runtime case (llama.cpp's cudart zips): unpacked into the
  // same folder as the build they belong to.
  const freeAssets = $derived((assets?.assets ?? []).filter((a) => !isPicked(a.name) && !isExtra(a.name)));

  const visibleAssets = $derived((assets?.assets ?? []).filter((a) => showAll || a.recommended || isPicked(a.name)));

  const canSave = $derived(!!repo.trim() && !!kind && (bare || !!exe.trim()) && picked.length > 0 && !saving);

  async function save(): Promise<void> {
    saving = true;
    err = null;
    try {
      await saveBackendSource({
        id: source?.id,
        repo: repo.trim(),
        name: name.trim(),
        blurb: blurb.trim(),
        kind,
        exe: exe.trim(),
        bare,
        tag,
        variants: picked,
      });
      onsaved();
      onclose();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  function fmtBytes(n: number): string {
    if (!n) return "";
    const mb = n / (1024 * 1024);
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
    return mb >= 10 ? `${Math.round(mb)} MB` : `${mb.toFixed(1)} MB`;
  }
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
  <div class="w-full max-w-3xl max-h-[90vh] flex flex-col rounded-md border border-card-border bg-surface shadow-xl">
    <header class="flex items-center gap-2 px-4 py-3 border-b border-card-border">
      <h6 >{source ? "Edit tracked repo" : "Track a backend repo"}</h6>
      <button
        type="button"
        class="ml-auto p-1 rounded text-txtsecondary hover:text-txtmain"
        aria-label="Close"
        onclick={onclose}><X size={16} /></button
      >
    </header>

    <div class="flex-1 overflow-y-auto px-4 py-3 flex flex-col gap-4">
      <!-- Step 1: the repo. Nothing else can be filled in until its releases
           load, because every other field is a choice over real data. -->
      <div class="flex flex-col gap-1">
        <label class="text-[0.7rem] text-txtsecondary" for="track-repo">GitHub repository</label>
        <div class="flex gap-2">
          <input
            id="track-repo"
            bind:value={repo}
            placeholder="owner/name or a github.com URL"
            disabled={!!source}
            onkeydown={(e) => e.key === "Enter" && find("")}
            class="flex-1 rounded border border-card-border bg-surface px-2 py-1 font-mono text-xs text-txtmain focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60"
          />
          <button
            type="button"
            class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary disabled:opacity-50"
            disabled={loading || !repo.trim()}
            onclick={() => find("")}><Search size={12} /> {assets ? "Reload" : "Find releases"}</button
          >
        </div>
      </div>

      {#if assets}
        <!-- Step 2: pick the build. This replaces writing an asset pattern —
             the server derives one from whatever is ticked here, wildcarding the
             parts that change between releases (build numbers, dates, shas) and
             keeping the parts that identify the flavour. -->
        <div class="flex flex-col gap-2">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[0.7rem] text-txtsecondary">Files in</span>
            <Select
              bind:value={tag}
              onchange={(v) => find(v)}
              mono
              options={assets.releases.map((r) => ({
                value: r.tag,
                label: `${r.tag}${r.prerelease ? " (pre-release)" : ""}`,
              }))}
              ariaLabel="Release tag"
              class="w-56"
            />
            <label class="ml-auto inline-flex items-center gap-1.5 text-[0.7rem] text-txtsecondary">
              <input type="checkbox" bind:checked={showAll} class="accent-primary" />
              Show every file
            </label>
          </div>
          <p class="text-[0.7rem] text-txtsecondary">
            Tick the build you want. Future releases are matched by what you tick here, so pick the flavour, not the
            version — the version number is filled in automatically each time.
          </p>

          <div class="max-h-56 overflow-y-auto rounded border border-card-border divide-y divide-card-border">
            {#each visibleAssets as a (a.name)}
              <label
                class="flex items-center gap-2 px-2 py-1.5 font-mono text-[0.7rem] cursor-pointer hover:bg-card-border/30"
              >
                <input
                  type="checkbox"
                  class="accent-primary"
                  checked={isPicked(a.name)}
                  disabled={isExtra(a.name)}
                  onchange={() => toggle(a.name)}
                />
                <span class="text-txtmain truncate" class:opacity-50={isExtra(a.name)}>{a.name}</span>
                <span class="ml-auto shrink-0 text-txtsecondary">{fmtBytes(a.size)}</span>
                {#if isExtra(a.name)}
                  <span class="shrink-0 text-txtsecondary">companion</span>
                {/if}
              </label>
            {:else}
              <p class="px-2 py-2 text-[0.7rem] text-txtsecondary">
                No likely builds in this release. Switch release, or tick “Show every file”.
              </p>
            {/each}
          </div>
        </div>

        {#if picked.length}
          <div class="flex flex-col gap-2">
            <span class="text-[0.7rem] text-txtsecondary">Selected builds</span>
            {#each picked as v (v.asset)}
              <div class="rounded border border-card-border px-2 py-2 flex flex-col gap-1.5">
                <div class="flex items-center gap-2">
                  <input
                    bind:value={v.label}
                    aria-label="Build name"
                    class="w-48 shrink-0 rounded border border-card-border bg-surface px-2 py-1 text-xs text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
                  />
                  <span class="font-mono text-[0.65rem] text-txtsecondary truncate">{v.asset}</span>
                  <button
                    type="button"
                    class="ml-auto shrink-0 p-1 rounded text-txtsecondary hover:text-error"
                    aria-label="Remove this build"
                    onclick={() => toggle(v.asset)}><Trash2 size={13} /></button
                  >
                </div>
                <!-- Some projects ship the GPU runtime as its own archive
                     (llama.cpp's cudart zips). Adding it here unpacks it into
                     the same folder, which is the only way that build runs. -->
                <div class="flex flex-wrap items-center gap-1.5">
                  {#each v.extras ?? [] as e (e)}
                    <span
                      class="inline-flex items-center gap-1 rounded border border-card-border px-1.5 py-0.5 font-mono text-[0.6rem] text-txtsecondary"
                    >
                      {e}
                      <button type="button" aria-label="Remove companion file" onclick={() => dropExtra(v, e)}
                        ><X size={10} /></button
                      >
                    </span>
                  {/each}
                  {#if freeAssets.length}
                    <!-- An action menu, not a value: it stays parked on the
                         placeholder and each pick appends a chip beside it. -->
                    <Select
                      value=""
                      onchange={(name) => name && addExtra(v, name)}
                      mono
                      options={[
                        { value: "", label: "+ companion file…" },
                        ...freeAssets.map((a) => ({ value: a.name, label: a.name })),
                      ]}
                      ariaLabel="Add companion file"
                      class="w-44"
                    />
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <!-- Step 3: what it is. The kind decides which models can be pointed at
             it and which ★ group it competes in; the executable name is what we
             look for inside the downloaded archive. -->
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div class="flex flex-col gap-1">
            <span class="text-[0.7rem] text-txtsecondary">What is this backend?</span>
            <Select bind:value={kind} onchange={onKindChange} options={kindOptions} ariaLabel="What is this backend?" />
          </div>

          <div class="flex flex-col gap-1">
            <label class="text-[0.7rem] text-txtsecondary" for="track-exe">Executable inside the download</label>
            <input
              id="track-exe"
              bind:value={exe}
              oninput={() => (exeTouched = true)}
              disabled={bare}
              class="rounded border border-card-border bg-surface px-2 py-1 font-mono text-xs text-txtmain focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
            />
            <label class="inline-flex items-center gap-1.5 text-[0.7rem] text-txtsecondary">
              <input type="checkbox" bind:checked={bare} class="accent-primary" />
              The download is the executable itself
            </label>
          </div>

          <div class="flex flex-col gap-1">
            <label class="text-[0.7rem] text-txtsecondary" for="track-name">Name</label>
            <input
              id="track-name"
              bind:value={name}
              class="rounded border border-card-border bg-surface px-2 py-1 text-xs text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
          </div>

          <div class="flex flex-col gap-1">
            <label class="text-[0.7rem] text-txtsecondary" for="track-blurb">Note (optional)</label>
            <input
              id="track-blurb"
              bind:value={blurb}
              placeholder="What this build is for"
              class="rounded border border-card-border bg-surface px-2 py-1 text-xs text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
            />
          </div>
        </div>
      {:else if !loading}
        <p class="text-[0.7rem] text-txtsecondary">
          Paste a repository that publishes its builds as release assets. Its latest release is fetched so you can pick
          the build you want — nothing here has to be typed by hand.
        </p>
      {/if}

      {#if err}
        <p class="font-mono text-[0.65rem] text-error">{err}</p>
      {/if}
    </div>

    <footer class="flex items-center gap-2 px-4 py-3 border-t border-card-border">
      {#if picked.length}
        <span class="text-[0.7rem] text-txtsecondary">
          {picked.length} build{picked.length === 1 ? "" : "s"} selected
        </span>
      {/if}
      <button type="button" class="btn btn--sm ml-auto uppercase tracking-wide" onclick={onclose}>Cancel</button>
      <button
        type="button"
        class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide border-primary text-primary hover:bg-primary/10 disabled:opacity-50"
        disabled={!canSave}
        onclick={save}><Plus size={12} /> {source ? "Save" : "Track this repo"}</button
      >
    </footer>
  </div>
</div>
