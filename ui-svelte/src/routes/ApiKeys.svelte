<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { onMount } from "svelte";
  import { Eye, EyeOff, Copy, Trash2, Plus, Check, KeyRound, X, SlidersHorizontal, HelpCircle } from "lucide-svelte";

  import { models, listApiKeys, upsertApiKey, deleteApiKey } from "../stores/api";
  import { refreshInferenceKey } from "../lib/inferenceAuth";
  import { modelCategory, MODEL_CATEGORIES } from "../lib/modelUtils";
  import type { ApiKey } from "../lib/types";
  import { askConfirm } from "../lib/confirm";

  // Read once, then it is a paragraph you scroll past forever - so it lives
  // behind the ? in the toolbar rather than on top of the list. tooltip.ts
  // renders with white-space:pre-line, so the blank lines survive.
  const HELP = [
    "Let other apps reach your models over the OpenAI-compatible API.",
    "An unscoped key can call everything; a scoped one only sees the models you pick.",
    "The dashboard and Playground never need one.",
  ].join("\n\n");

  let keys = $state<ApiKey[]>([]);
  // The auto-managed Playground key is hidden from the list; it exists only so
  // the in-browser Playground can reach every model when user keys are scoped.
  const visibleKeys = $derived(keys.filter((k) => !k.builtin));
  let loadErr = $state<string | null>(null);
  let available = $state(false); // false => server not running with -generate

  // New-key form state.
  let newName = $state("");
  let newScope = $state<Set<string>>(new Set());
  let creating = $state(false); // the new-key form is a row of the list, not a permanent box
  let showPicker = $state(false); // model scope picker is collapsed until opened
  let saving = $state(false);
  let formErr = $state<string | null>(null);

  // Per-row UI state (keyed by key name).
  let revealed = $state<Set<string>>(new Set());
  let copied = $state<string | null>(null);
  let editingScope = $state<string | null>(null);
  let editScope = $state<Set<string>>(new Set());

  // Listable models grouped by category, in MODEL_CATEGORIES order; empty groups dropped.
  // Unlisted vision twins ARE included: they're callable model ids a scoped key must be
  // able to reach, even though they're hidden from the operator model picker.
  const grouped = $derived.by(() => {
    const listed = ($models || []).filter((m) => !m.peerID && (!m.unlisted || m.capabilities?.vision));
    return MODEL_CATEGORIES.map((c) => ({
      label: c.label,
      ids: listed.filter((m) => modelCategory(m) === c.id).map((m) => m.id).sort(),
    })).filter((g) => g.ids.length > 0);
  });

  async function load(): Promise<void> {
    try {
      keys = await listApiKeys();
      available = true;
      refreshInferenceKey(); // keep the Playground's auto-key in sync after edits
    } catch (e) {
      // 501 => server without -generate; show a friendly notice rather than an error.
      const msg = e instanceof Error ? e.message : String(e);
      if (msg.includes("501")) available = false;
      else loadErr = msg;
    }
  }

  onMount(load);

  function toggle(set: Set<string>, id: string): Set<string> {
    const next = new Set(set);
    next.has(id) ? next.delete(id) : next.add(id);
    return next;
  }

  async function create(): Promise<void> {
    formErr = null;
    if (!newName.trim()) {
      formErr = "Name is required.";
      return;
    }
    if (keys.some((k) => k.name.toLowerCase() === newName.trim().toLowerCase())) {
      formErr = "A key with that name already exists.";
      return;
    }
    saving = true;
    try {
      await upsertApiKey(newName.trim(), [...newScope]);
      newName = "";
      newScope = new Set();
      showPicker = false;
      creating = false;
      await load();
    } catch (e) {
      formErr = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  async function saveScope(name: string): Promise<void> {
    saving = true;
    try {
      await upsertApiKey(name, [...editScope]);
      editingScope = null;
      await load();
    } catch (e) {
      loadErr = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  async function remove(name: string): Promise<void> {
    const ok = await askConfirm({
      title: `Delete API key "${name}"?`,
      body: "Apps using it will lose access.",
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await deleteApiKey(name);
      await load();
    } catch (e) {
      loadErr = e instanceof Error ? e.message : String(e);
    }
  }

  async function copy(secret: string): Promise<void> {
    await navigator.clipboard.writeText(secret);
    copied = secret;
    setTimeout(() => (copied = copied === secret ? null : copied), 1200);
  }

  function startEdit(k: ApiKey): void {
    editingScope = k.name;
    editScope = new Set(k.models || []);
  }

  function startCreate(): void {
    creating = true;
    formErr = null;
    newName = "";
    newScope = new Set();
    showPicker = false;
  }

  // Whole-category select: picking "every vision model" one chip at a time is
  // the tedious half of scoping a key.
  function toggleGroup(sel: Set<string>, ids: string[], set: (n: Set<string>) => void): void {
    const all = ids.every((id) => sel.has(id));
    const next = new Set(sel);
    for (const id of ids) all ? next.delete(id) : next.add(id);
    set(next);
  }

  function scopeLabel(k: ApiKey): string {
    const n = k.models?.length ?? 0;
    return n ? `${n} model${n > 1 ? "s" : ""}` : "Full access";
  }

  function mask(s: string): string {
    return s.length <= 8 ? "•".repeat(s.length) : s.slice(0, 3) + "•".repeat(s.length - 6) + s.slice(-3);
  }
</script>

<!-- Full-bleed, like Models and Browse: this is a list page, and a centred
     column left most of the window empty while every key line was still
     truncating. -->
<div class="flex flex-col h-full">
  <!-- Toolbar, matching the other list pages: title left, the page's one action
       right. The explainer is a tooltip on the ? - it is worth reading once,
       then it is a paragraph you scroll past forever. -->
  <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border shrink-0">
    <h5 class="mb-0">API keys</h5>
    <button
      type="button"
      class="icon-btn shrink-0 text-txtsecondary"
      use:tip={HELP}
      aria-label="What API keys are for"
    >
      <HelpCircle size={14} />
    </button>
    {#if available}
      <button class="btn btn--sm btn--primary ml-auto shrink-0 uppercase tracking-wide" onclick={startCreate} disabled={creating}>
        <Plus size={14} class="mr-1 inline-block -mt-px" />New key
      </button>
    {/if}
  </div>

  {#if !available}
    <p class="px-3 py-4 text-sm text-txtsecondary">
      API-key management requires the server to run with <code class="font-mono">-generate</code>.
    </p>
  {:else}
    {#if loadErr}
      <p class="shrink-0 border-b border-card-border-inner bg-error/10 px-3 py-2 text-sm text-error">{loadErr}</p>
    {/if}

    <!-- One surface spanning the page: the create form and every key are rows on
         it, hairline-separated, rather than a stack of floating boxes. -->
    <div class="min-h-0 flex-1 overflow-auto pretty-scroll bg-surface divide-y divide-card-border-inner">
      {#if creating}
        <div class="p-4">
          <div class="mb-3 flex items-center gap-2">
            <KeyRound size={15} class="shrink-0 text-primary" />
            <h6 class="mb-0">New key</h6>
            <button class="icon-btn ml-auto" onclick={() => (creating = false)} use:tip={"Cancel"} aria-label="Cancel"><X size={14} /></button>
          </div>

          <div class="flex flex-wrap items-end gap-3">
            <label class="flex min-w-0 flex-col gap-1">
              <span class="text-[0.7rem] uppercase tracking-wide text-txtsecondary">Name</span>
              <input
                class="w-56 rounded border border-card-border bg-background px-2 py-1 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-primary"
                placeholder="e.g. pi-harness"
                bind:value={newName}
                onkeydown={(e) => e.key === "Enter" && create()}
              />
            </label>
            <button
              type="button"
              class="btn btn--sm uppercase tracking-wide"
              onclick={() => (showPicker = !showPicker)}
              disabled={grouped.length === 0}
            >
              <SlidersHorizontal size={13} class="mr-1 inline-block -mt-px" />Scope
              <span class="normal-case text-txtsecondary">({newScope.size === 0 ? "full access" : `${newScope.size} selected`})</span>
            </button>
            <button class="btn btn--sm btn--primary ml-auto uppercase tracking-wide" onclick={create} disabled={saving}>
              <Plus size={14} class="mr-1 inline-block -mt-px" />Create
            </button>
          </div>

          {#if grouped.length === 0}
            <p class="mt-2 text-xs text-txtsecondary">No models in the catalog yet.</p>
          {:else if showPicker}
            {@render picker(newScope, (next) => (newScope = next))}
          {/if}
          {#if formErr}<p class="mt-2 text-xs text-error">{formErr}</p>{/if}
        </div>
      {/if}

      {#if visibleKeys.length === 0}
        <div class="flex flex-col items-center justify-center gap-2 px-4 py-14 text-center">
          <KeyRound size={26} class="text-txtsecondary/50" />
          <p class="text-sm text-txtsecondary">No API keys yet.</p>
          {#if !creating}
            <button class="btn btn--sm uppercase tracking-wide" onclick={startCreate}><Plus size={14} class="mr-1 inline-block -mt-px" />Create one</button>
          {/if}
        </div>
      {:else}
        {#each visibleKeys as k (k.name)}
          {@const scoped = !!(k.models && k.models.length)}
          <div class="p-4">
            <!-- Identity line: what it is called, how far it reaches, and the
                 secret itself - the three things you open this page for. -->
            <div class="flex flex-wrap items-center gap-2">
              <KeyRound size={15} class="shrink-0 text-txtsecondary" />
              <span class="font-mono text-sm font-bold text-txtmain">{k.name}</span>
              <span
                class="rounded-full border px-2 py-0.5 text-[0.65rem] font-medium uppercase tracking-wide {scoped
                  ? 'border-card-border text-txtsecondary'
                  : 'border-primary/50 text-primary'}"
              >
                {scopeLabel(k)}
              </span>

              <div class="ml-auto flex min-w-0 items-center gap-1">
                <code class="min-w-0 max-w-[22rem] flex-1 truncate rounded border border-card-border bg-background px-2 py-1 font-mono text-xs text-txtsecondary">
                  {revealed.has(k.name) ? k.key : mask(k.key)}
                </code>
                <button class="icon-btn" onclick={() => (revealed = toggle(revealed, k.name))} use:tip={revealed.has(k.name) ? "Hide" : "Show"} aria-label="Reveal key">
                  {#if revealed.has(k.name)}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
                </button>
                <button class="icon-btn" onclick={() => copy(k.key)} use:tip={"Copy"} aria-label="Copy key">
                  {#if copied === k.key}<Check size={14} class="text-primary" />{:else}<Copy size={14} />{/if}
                </button>
                <button
                  class="icon-btn"
                  aria-pressed={editingScope === k.name}
                  onclick={() => (editingScope === k.name ? (editingScope = null) : startEdit(k))}
                  use:tip={"Edit scope"}
                  aria-label="Edit scope"
                ><SlidersHorizontal size={14} /></button>
                <button class="icon-btn hover:text-error" onclick={() => remove(k.name)} use:tip={"Delete key"} aria-label="Delete key"><Trash2 size={14} /></button>
              </div>
            </div>

            <!-- Scope: chips at rest, the picker in place when editing. -->
            {#if editingScope === k.name}
              {@render picker(editScope, (next) => (editScope = next))}
              <div class="mt-3 flex gap-2">
                <button class="btn btn--sm btn--primary uppercase tracking-wide" onclick={() => saveScope(k.name)} disabled={saving}>Save scope</button>
                <button class="btn btn--sm uppercase tracking-wide" onclick={() => (editingScope = null)}>Cancel</button>
              </div>
            {:else if scoped}
              <div class="mt-2 flex flex-wrap items-center gap-1.5 pl-6">
                {#each k.models ?? [] as m (m)}
                  <span class="rounded bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.7rem] text-txtsecondary">{m}</span>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  {/if}
</div>

<!-- Model picker grouped by category. `sel` is the current Set; `set` swaps in a new one. -->
{#snippet picker(sel: Set<string>, set: (next: Set<string>) => void)}
  <div class="mt-3 rounded-md border border-card-border-inner bg-surface-2 p-3">
    <p class="mb-2 text-[0.7rem] uppercase tracking-wide text-txtsecondary">
      Allowed models <span class="normal-case">(none selected = full access)</span>
    </p>
    <div class="space-y-2">
      {#each grouped as g (g.label)}
        <div>
          <button
            type="button"
            class="mb-1 text-[0.7rem] uppercase tracking-wide text-txtsecondary hover:cursor-pointer hover:text-primary"
            onclick={() => toggleGroup(sel, g.ids, set)}
            use:tip={"Select or clear the whole category"}
          >
            {g.label} <span class="tabular-nums">({g.ids.filter((id) => sel.has(id)).length}/{g.ids.length})</span>
          </button>
          <div class="flex flex-wrap gap-1.5">
            {#each g.ids as id (id)}
              <button type="button" class="chip-toggle" aria-pressed={sel.has(id)} onclick={() => set(toggle(sel, id))}>{id}</button>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  </div>
{/snippet}
