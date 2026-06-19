<script lang="ts">
  import { models, loadModel, unloadSingleModel, getModelConfig, putModelOverride, type ModelConfig, type ModelOverride } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { prettifyModelName } from "../lib/modelUtils";
  import { scrollFade } from "../lib/scrollFade";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import InferenceFeedback from "./InferenceFeedback.svelte";

  let pendingLoads = $state<Record<string, boolean>>({});
  const loadControllers = new Map<string, AbortController>();

  // Per-model config editor (cogwheel) state.
  let configModelId = $state<string | null>(null);
  let configOpen = $state(false);
  function openConfig(id: string): void {
    configModelId = id;
    configOpen = true;
  }
  function closeConfig(): void {
    configOpen = false;
  }

  const showUnlistedStore = persistentStore<boolean>("showUnlisted", true);
  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "name");

  // A card: one gguf family. primary is the base entry; variants are the rest.
  // group/ports come off the primary (members share a swap group). active = any
  // member is on the GPU.
  type Card = {
    key: string;
    primary: Model;
    variants: Model[];
    group: string;
    ports: string[];
  };

  function isLive(m: Model): boolean {
    return m.state === "ready" || m.state === "starting" || m.state === "stopping";
  }

  // Collapse a flat model list into family cards (single-member families become
  // standalone one-entry cards).
  function buildCards(members: Model[]): Card[] {
    const byFamily = new Map<string, Model[]>();
    const standalone: Model[] = [];
    for (const m of members) {
      if (!m.family) {
        standalone.push(m);
        continue;
      }
      const arr = byFamily.get(m.family) ?? [];
      arr.push(m);
      byFamily.set(m.family, arr);
    }
    const cards: Card[] = [];
    const mk = (key: string, fam: Model[]): Card => {
      const sorted = [...fam].sort((a, b) => a.id.length - b.id.length || a.id.localeCompare(b.id));
      const primary = sorted[0];
      return {
        key,
        primary,
        variants: sorted.slice(1).sort((a, b) => a.id.localeCompare(b.id)),
        group: primary.group ?? "",
        ports: primary.listeners ?? [],
      };
    };
    for (const [key, fam] of byFamily) cards.push(mk(key, fam));
    for (const m of standalone) cards.push(mk(m.id, [m]));
    cards.sort((a, b) => a.primary.id.localeCompare(b.primary.id));
    return cards;
  }

  let view = $derived.by(() => {
    const listed = $models.filter((m) => $showUnlistedStore || !m.unlisted);
    const local = listed.filter((m) => !m.peerID);
    const peers = listed.filter((m) => m.peerID);

    const cards = buildCards(local);
    // Active families lift into the top panel; the rest fill the card grid.
    const activeCards = cards.filter((c) => isLive(c.primary) || c.variants.some(isLive));
    const idleCards = cards.filter((c) => !(isLive(c.primary) || c.variants.some(isLive)));

    // The live members themselves (each gets a mini-settings block up top).
    const liveMembers: Model[] = [];
    for (const c of [...activeCards]) {
      for (const m of [c.primary, ...c.variants]) if (isLive(m)) liveMembers.push(m);
    }

    const peersByPeerId = peers.reduce(
      (acc, m) => {
        const k = m.peerID || "unknown";
        (acc[k] ??= []).push(m);
        return acc;
      },
      {} as Record<string, Model[]>,
    );

    return { idleCards, liveMembers, peersByPeerId };
  });

  // --- Staged selection: a model placed into the top card to edit then load ---
  let stagedId = $state<string | null>(null);
  function stageModel(id: string): void {
    stagedId = id;
    expanded = {};
  }
  function unstage(): void {
    stagedId = null;
  }
  const stagedModel = $derived(stagedId ? ($models.find((m) => m.id === stagedId) ?? null) : null);
  // Top card shows every live member, plus the staged model when it isn't live.
  const topMembers = $derived.by(() => {
    const list = [...view.liveMembers];
    if (stagedModel && !isLive(stagedModel) && !list.some((m) => m.id === stagedModel.id)) {
      list.push(stagedModel);
    }
    return list;
  });

  // Once the staged model is actually live, drop the staging marker — it now
  // shows under the live members anyway.
  $effect(() => {
    if (stagedModel && isLive(stagedModel)) stagedId = null;
  });

  // --- Active model launch params (fetched per top member) ---
  let configs = $state<Record<string, ModelConfig>>({});
  $effect(() => {
    const ids = topMembers.map((m) => m.id);
    for (const id of ids) {
      if (configs[id]) continue;
      getModelConfig(id)
        .then((c) => (configs = { ...configs, [id]: c }))
        .catch(() => {});
    }
  });

  // --- Inline param editing for the active model (apply via Reload) ---
  type Draft = { ctx: string; kvK: string; kvV: string; spec: string; reasoningFmt: string };
  let drafts = $state<Record<string, Draft>>({});
  let baselines = $state<Record<string, Draft>>({});
  let reloading = $state<Record<string, boolean>>({});

  const KV_OPTS = ["", "q8_0", "q4_0", "q5_1", "f16", "bf16"];
  const SPEC_OPTS = ["", "none", "draft-mtp", "ngram-mod"];
  const REASON_OPTS = ["", "auto"];

  function draftFrom(cfg: ModelConfig): Draft {
    const f = parseFlags(cfg.cmd);
    const o = cfg.override;
    return {
      ctx: String(o?.ctx || f.ctx || ""),
      kvK: o?.kvK ?? f.kvK ?? "",
      kvV: o?.kvV ?? f.kvV ?? "",
      spec: o?.spec ?? "",
      reasoningFmt: o?.reasoningFmt ?? "",
    };
  }

  // Seed a draft + baseline once per fetched config.
  $effect(() => {
    for (const [id, cfg] of Object.entries(configs)) {
      if (drafts[id]) continue;
      const d = draftFrom(cfg);
      drafts = { ...drafts, [id]: { ...d } };
      baselines = { ...baselines, [id]: { ...d } };
    }
  });

  function draftDirty(id: string): boolean {
    const a = drafts[id];
    const b = baselines[id];
    if (!a || !b) return false;
    return a.ctx !== b.ctx || a.kvK !== b.kvK || a.kvV !== b.kvV || a.spec !== b.spec || a.reasoningFmt !== b.reasoningFmt;
  }

  // Apply any edited params (regen + hot-reload config) then (re)load the model.
  // Used for both "Reload" (live, dirty) and "Load" (staged) from the top card.
  async function applyDraftAndLoad(m: Model): Promise<void> {
    if (reloading[m.id]) return;
    reloading[m.id] = true;
    try {
      if (draftDirty(m.id)) {
        const cfg = configs[m.id];
        const d = drafts[m.id];
        if (cfg && d) {
          const override: ModelOverride = {
            ...(cfg.override ?? {}),
            ctx: d.ctx ? Number(d.ctx) : 0,
            kvK: d.kvK,
            kvV: d.kvV,
            spec: d.spec,
            reasoningFmt: d.reasoningFmt,
          };
          await putModelOverride(m.id, override);
          // Drop caches so they re-seed from the freshly generated command.
          configs = Object.fromEntries(Object.entries(configs).filter(([k]) => k !== m.id));
          drafts = Object.fromEntries(Object.entries(drafts).filter(([k]) => k !== m.id));
          baselines = Object.fromEntries(Object.entries(baselines).filter(([k]) => k !== m.id));
        }
      }
      await loadModel(m.id);
    } catch (e) {
      console.error(e);
    } finally {
      reloading[m.id] = false;
    }
  }

  // Pull a curated set of effective launch flags straight off the run command,
  // so the panel reflects what's actually running.
  const FLAG_MAP: Record<string, string> = {
    "-c": "ctx",
    "--ctx-size": "ctx",
    "-ngl": "ngl",
    "--n-gpu-layers": "ngl",
    "--n-cpu-moe": "cpuMoe",
    "-ctk": "kvK",
    "--cache-type-k": "kvK",
    "-ctv": "kvV",
    "--cache-type-v": "kvV",
    "--spec-type": "spec",
    "--reasoning-format": "reasoningFmt",
    "--reasoning": "reasoning", // captures the `--reasoning off` switch
  };
  function parseFlags(cmd: string): Record<string, string> {
    const out: Record<string, string> = {};
    const toks = cmd.split(/\s+/).filter(Boolean);
    for (let i = 0; i < toks.length; i++) {
      const key = FLAG_MAP[toks[i]];
      if (key && i + 1 < toks.length) out[key] = toks[i + 1];
    }
    return out;
  }

  // Effective (resolved) values the empty "inherit" option falls back to, parsed
  // from the actual launch command, so the dropdown shows what "default" means.
  function specDefault(f: Record<string, string>): string {
    return f.spec || "auto";
  }
  function reasonDefault(f: Record<string, string>): string {
    return f.reasoning === "off" ? "off" : f.reasoningFmt || "auto";
  }
  // GPU layers as value/max (max = transformer blocks). -ngl 99 is the "all
  // layers" sentinel, so clamp to the block count. Falls back to the raw value
  // when the block count is unknown (0).
  function nglDisplay(ngl: string | undefined, blocks: number): string {
    if (ngl == null) return "—";
    const n = Number(ngl);
    if (!blocks || !Number.isFinite(n)) return ngl;
    return `${Math.min(n, blocks)}/${blocks}`;
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

  function display(m: Model): string {
    return $showIdorNameStore === "id" ? m.id : prettifyModelName(m.name || m.id);
  }

  // Card outline + status text color by state.
  function stateRing(state: string): string {
    if (state === "ready") return "border-success/60";
    if (state === "starting") return "border-warning/60";
    if (state === "stopping") return "border-error/60";
    return "border-card-border";
  }
  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }

  // Per-card variant expansion (load individual variants). dropUp flips the
  // popup above the trigger when a bottom-row card lacks room below, so it stays
  // in view instead of being clipped by the scroll container.
  let expanded = $state<Record<string, boolean>>({});
  let dropUp = $state<Record<string, boolean>>({});
  const POPUP_H = 260; // ~max-h-60 (240px) + margin
  function toggleExpand(key: string, e?: MouseEvent): void {
    const opening = !expanded[key];
    if (opening && e) {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
      dropUp[key] = window.innerHeight - rect.bottom < POPUP_H && rect.top > POPUP_H;
    }
    expanded[key] = opening;
  }
</script>

<div class="flex flex-col h-full gap-3">
  {#snippet playIcon()}
    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path d="M6 4l10 6-10 6V4z" /></svg>
  {/snippet}
  {#snippet stopIcon()}
    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><rect x="5" y="5" width="10" height="10" rx="1.5" /></svg>
  {/snippet}
  {#snippet hint(text: string)}
    <span
      class="inline-flex items-center justify-center w-3 h-3 shrink-0 rounded-full border border-card-border text-txtsecondary text-[0.5rem] leading-none cursor-help normal-case hover:text-txtmain hover:border-txtmain"
      title={text}
      aria-label={text}>?</span>
  {/snippet}

  <!-- Header / toolbar -->
  <div class="flex items-center justify-between shrink-0">
    <h2 class="!pb-0">Models</h2>
    <div class="flex items-center gap-2">
      <button
        class="btn btn--sm uppercase tracking-wide"
        onclick={() => showIdorNameStore.update((p) => (p === "name" ? "id" : "name"))}
        title="Toggle id / name display"
      >
        {$showIdorNameStore === "id" ? "ID" : "Name"}
      </button>
      <button
        class="btn btn--sm uppercase tracking-wide"
        onclick={() => showUnlistedStore.update((p) => !p)}
        title="Show or hide unlisted models"
      >
        {$showUnlistedStore ? "Hide unlisted" : "Show unlisted"}
      </button>
    </div>
  </div>

  <!-- TOP: active model(s) — mini launch params + upstream log -->
  <div class="shrink-0 h-72 min-h-[14rem]">
    {#if topMembers.length === 0}
      <div class="card h-full flex items-center justify-center text-center">
        <div>
          <p class="font-mono text-sm text-txtsecondary">No model loaded.</p>
          <p class="font-mono text-xs text-txtsecondary mt-1">Pick one from the grid below — or open a variant — to load it onto the GPU.</p>
        </div>
      </div>
    {:else}
      <div class="grid h-full grid-cols-1 lg:grid-cols-2 gap-3">
        <!-- Active model settings -->
        <div class="card h-full overflow-auto pretty-scroll">
          {#each topMembers as m (m.id)}
            {@const cfg = configs[m.id]}
            {@const flags = cfg ? parseFlags(cfg.cmd) : {}}
            {@const d = drafts[m.id]}
            {@const dirty = draftDirty(m.id)}
            {@const live = isLive(m)}
            <div class="mb-3 last:mb-0">
              <div class="flex items-center gap-2">
                <span class="inline-block w-2.5 h-2.5 rounded-full {dotClass(m.state)}"></span>
                <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{live ? m.state : "staged"}</span>
                <div class="ml-auto flex items-center gap-1.5">
                  <button
                    class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
                    onclick={() => openConfig(m.id)}
                    aria-label="Edit parameters"
                    title="Edit parameters / variants"
                  >
                    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4">
                      <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                    </svg>
                  </button>
                  {#if live && dirty}
                    <button
                      class="btn btn--sm btn--primary inline-flex items-center gap-1.5 uppercase tracking-wide"
                      onclick={() => applyDraftAndLoad(m)}
                      disabled={reloading[m.id]}
                      title="Apply edited parameters and restart this model"
                    >
                      {@render playIcon()}
                      {reloading[m.id] ? "Reloading…" : "Reload"}
                    </button>
                  {/if}
                  {#if live}
                    <button
                      class="btn btn--sm inline-flex items-center gap-1.5 uppercase tracking-wide hover:border-error hover:text-error"
                      onclick={() => unloadSingleModel(m.id)}
                      disabled={m.state !== "ready"}
                    >
                      {@render stopIcon()}
                      Unload
                    </button>
                  {:else}
                    <button
                      class="btn btn--sm btn--primary inline-flex items-center gap-1.5 uppercase tracking-wide"
                      onclick={() => applyDraftAndLoad(m)}
                      disabled={reloading[m.id]}
                      title="Load this model with the parameters above"
                    >
                      {@render playIcon()}
                      {reloading[m.id] ? "Loading…" : "Load"}
                    </button>
                    <button
                      class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-error hover:border-error transition-colors"
                      onclick={unstage}
                      aria-label="Remove from staging"
                      title="Clear selection"
                    >
                      <svg viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4"><path d="M6 6l8 8M14 6l-8 8" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
                    </button>
                  {/if}
                </div>
              </div>
              <div class="mt-1.5 font-mono text-sm uppercase tracking-widest text-txtsecondary break-words" title={m.id}>{display(m)}</div>

              <!-- Editable launch params (apply via Reload) -->
              {#if d}
                <div class="mt-2 grid grid-cols-3 gap-x-3 gap-y-2 font-mono text-xs">
                  <label class="flex flex-col gap-0.5">
                    <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">Ctx {@render hint("Context window (tokens) this model loads with. Empty = the size the autogen sizer picked to fit free VRAM. Applies on Reload.")}</span>
                    <input
                      type="number" min="0" step="1024" placeholder="auto" bind:value={d.ctx}
                      onwheel={(e) => {
                        if (document.activeElement !== e.currentTarget) return;
                        e.preventDefault();
                        d.ctx = String(Math.max(0, (Number(d.ctx) || 0) + (e.deltaY < 0 ? 1024 : -1024)));
                      }}
                      class="w-full rounded border border-card-border bg-background px-1.5 py-1 text-txtmain tabular-nums focus:outline-none focus:ring-2 focus:ring-primary"
                    />
                  </label>
                  <div>
                    <div class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">GPU layers {@render hint("Layers resident on the GPU (-ngl), as chosen by the sizer for the current plan. Read-only here; pin it via the cogwheel's offload setting.")}</div>
                    <div class="text-txtmain tabular-nums pt-1.5">{nglDisplay(flags.ngl, cfg?.blockCount ?? 0)}</div>
                  </div>
                  <div>
                    <div class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">CPU MoE {@render hint("Expert layers offloaded to the CPU (--n-cpu-moe) for MoE models. Read-only here; pin it via the cogwheel's offload setting.")}</div>
                    <div class="text-txtmain tabular-nums pt-1.5">{flags.cpuMoe ?? "—"}</div>
                  </div>
                  <label class="flex flex-col gap-0.5">
                    <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">KV K {@render hint("Quantization of the attention key cache (-ctk). Lower bits = less VRAM, slightly less accuracy. auto = q8_0. Must match KV V for flash-attention.")}</span>
                    <select bind:value={d.kvK} class="w-full rounded border border-card-border bg-background px-1.5 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary">
                      {#each KV_OPTS as o (o)}<option value={o}>{o === "" ? "auto" : o}</option>{/each}
                    </select>
                  </label>
                  <label class="flex flex-col gap-0.5">
                    <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">KV V {@render hint("Quantization of the attention value cache (-ctv). Lower bits = less VRAM. auto = q8_0. Must match KV K for flash-attention.")}</span>
                    <select bind:value={d.kvV} class="w-full rounded border border-card-border bg-background px-1.5 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary">
                      {#each KV_OPTS as o (o)}<option value={o}>{o === "" ? "auto" : o}</option>{/each}
                    </select>
                  </label>
                  <label class="flex flex-col gap-0.5">
                    <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">Spec {@render hint("Speculative decoding to speed up generation (--spec-type). ngram-mod is the default; draft-mtp needs a model with MTP layers; none disables it.")}</span>
                    <select bind:value={d.spec} class="w-full rounded border border-card-border bg-background px-1.5 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary">
                      {#each SPEC_OPTS as o (o)}<option value={o}>{o === "" ? specDefault(flags) : o}</option>{/each}
                    </select>
                  </label>
                  <label class="flex flex-col gap-0.5">
                    <span class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">Reasoning {@render hint("How the model's chain-of-thought is parsed (--reasoning-format). auto lets llama.cpp detect it (reasoning stays on); off disables reasoning.")}</span>
                    <select bind:value={d.reasoningFmt} class="w-full rounded border border-card-border bg-background px-1.5 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary">
                      {#each REASON_OPTS as o (o)}<option value={o}>{o === "" ? reasonDefault(flags) : o}</option>{/each}
                    </select>
                  </label>
                </div>
              {/if}

              {#if cfg}
                <details class="mt-2">
                  <summary class="font-mono text-[0.65rem] text-txtsecondary cursor-pointer hover:text-txtmain">Launch command</summary>
                  <pre class="mt-1 whitespace-pre-wrap break-all font-mono text-[0.65rem] text-txtsecondary bg-background rounded p-2">{cfg.cmd}</pre>
                </details>
              {/if}
            </div>
          {/each}
        </div>

        <!-- Live inference feedback (replaces the upstream log here) -->
        <div class="h-full min-h-0">
          <InferenceFeedback models={topMembers} />
        </div>
      </div>
    {/if}
  </div>

  <!-- BOTTOM: flat card grid of other models -->
  <div class="flex-1 overflow-y-auto min-h-0 pretty-scroll scroll-fade-y px-0.5 py-1" use:scrollFade>
    {#if view.idleCards.length > 0}
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {#each view.idleCards as card (card.key)}
          {@const m = card.primary}
          {@const variantCount = card.variants.length + 1}
          {@const staged = stagedId === m.id || card.variants.some((v) => v.id === stagedId)}
          <div class="card !overflow-visible border {stateRing(m.state)} {staged ? 'ring-2 ring-primary' : ''} flex flex-col gap-2 p-3 transition-colors">
            <!-- Top row: status + badges + cog -->
            <div class="flex items-center gap-2">
              <span class="inline-block w-2 h-2 rounded-full {dotClass(m.state)}"></span>
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{m.state}</span>
              <div class="ml-auto flex items-center gap-1">
                {#each card.ports as port (port)}
                  <span class="font-mono text-[0.55rem] text-primary border border-primary/40 rounded px-1 py-0.5" title="Listener {port}">{port}</span>
                {/each}
                <button
                  class="inline-flex items-center justify-center p-1 rounded border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
                  onclick={() => openConfig(m.id)}
                  aria-label="Edit parameters"
                  title="Edit parameters / variants"
                >
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-3.5 h-3.5">
                    <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                  </svg>
                </button>
              </div>
            </div>

            <!-- Name -->
            <span class="font-mono text-[0.7rem] uppercase tracking-widest text-txtsecondary truncate {m.unlisted ? 'opacity-70' : ''}" title={m.id}>
              {display(m)}
            </span>

            <!-- Footer: variants + load -->
            <div class="mt-auto flex items-center gap-2 relative">
              {#if variantCount > 1}
                <button
                  class="font-mono text-[0.65rem] text-txtsecondary hover:text-txtmain border border-card-border rounded px-1.5 py-0.5 tabular-nums"
                  onclick={(e) => toggleExpand(card.key, e)}
                  title="Show variants"
                >
                  {variantCount} variants {expanded[card.key] ? "▴" : "▾"}
                </button>
              {/if}
              <div class="ml-auto">
                {#if pendingLoads[m.id]}
                  <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={() => cancelLoad(m.id)}>Cancel</button>
                {:else}
                  <button class="btn btn--sm inline-flex items-center gap-1.5 hover:border-primary hover:text-primary" onclick={() => handleLoadModel(m.id)}>
                    {@render playIcon()}
                    Load
                  </button>
                {/if}
              </div>

              <!-- Variants popup menu: pick one to open in the top editor card -->
              {#if expanded[card.key] && card.variants.length > 0}
                <!-- click-catcher: closes the menu on outside click -->
                <button class="fixed inset-0 z-10 cursor-default" aria-label="Close variants" onclick={() => toggleExpand(card.key)}></button>
                <div class="absolute z-20 left-0 right-0 {dropUp[card.key] ? 'bottom-full mb-1' : 'top-full mt-1'} rounded-md border border-card-border bg-surface shadow-lg p-1.5 flex flex-col gap-0.5 max-h-60 overflow-y-auto pretty-scroll">
                  <div class="px-1.5 pb-1 font-mono text-[0.55rem] uppercase tracking-wide text-txtsecondary">Open in editor</div>
                  {#each [card.primary, ...card.variants] as v (v.id)}
                    <div class="flex items-center gap-1 rounded hover:bg-background transition-colors {stagedId === v.id ? 'bg-background' : ''}">
                      <button
                        class="flex-1 min-w-0 text-left flex items-start gap-2 px-1.5 py-1"
                        onclick={() => stageModel(v.id)}
                        title={v.id}
                      >
                        <span class="inline-block w-1.5 h-1.5 rounded-full mt-1 shrink-0 {dotClass(v.state)}"></span>
                        <span class="font-mono text-xs text-txtmain break-words whitespace-normal">{display(v)}</span>
                        {#if isLive(v)}<span class="ml-auto shrink-0 font-mono text-[0.55rem] uppercase text-txtsecondary">{v.state}</span>{/if}
                      </button>
                      <button
                        class="shrink-0 inline-flex items-center justify-center p-1 mr-0.5 rounded border border-card-border text-txtsecondary hover:text-txtmain hover:bg-card-border/40 transition-colors"
                        onclick={(e) => { e.stopPropagation(); openConfig(card.primary.id); }}
                        aria-label="Edit parameters"
                        title="Edit parameters / variants"
                      >
                        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3">
                          <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                        </svg>
                      </button>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <p class="font-mono text-xs text-txtsecondary px-1">No other models.</p>
    {/if}

    <!-- Peer models -->
    {#if Object.keys(view.peersByPeerId).length > 0}
      <div class="mt-6">
        <h3 class="mb-2">Peer models</h3>
        {#each Object.entries(view.peersByPeerId).sort(([a], [b]) => a.localeCompare(b)) as [peerId, peerModels] (peerId)}
          <div class="mb-3">
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
</div>

<ModelConfigModal modelId={configModelId} open={configOpen} onclose={closeConfig} />
