<script lang="ts">
  import { onMount } from "svelte";
  import { Eye, EyeOff, Copy, Trash2, Plus, Check } from "lucide-svelte";
  import { models, listApiKeys, upsertApiKey, deleteApiKey } from "../stores/api";
  import { refreshInferenceKey } from "../lib/inferenceAuth";
  import { modelCategory, MODEL_CATEGORIES } from "../lib/modelUtils";
  import type { ApiKey } from "../lib/types";

  let keys = $state<ApiKey[]>([]);
  // The auto-managed Playground key is hidden from the list; it exists only so
  // the in-browser Playground can reach every model when user keys are scoped.
  const visibleKeys = $derived(keys.filter((k) => !k.builtin));
  let loadErr = $state<string | null>(null);
  let available = $state(false); // false => server not running with -generate

  // New-key form state.
  let newName = $state("");
  let newScope = $state<Set<string>>(new Set());
  let showPicker = $state(false); // model scope picker is collapsed until opened
  let saving = $state(false);
  let formErr = $state<string | null>(null);

  // Per-row UI state (keyed by key name).
  let revealed = $state<Set<string>>(new Set());
  let copied = $state<string | null>(null);
  let editingScope = $state<string | null>(null);
  let editScope = $state<Set<string>>(new Set());

  // Listable models grouped by category, in MODEL_CATEGORIES order; empty groups dropped.
  const grouped = $derived.by(() => {
    const listed = ($models || []).filter((m) => !m.unlisted && !m.peerID);
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
    if (!confirm(`Delete API key "${name}"? Apps using it will lose access.`)) return;
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

  function mask(s: string): string {
    return s.length <= 8 ? "•".repeat(s.length) : s.slice(0, 3) + "•".repeat(s.length - 6) + s.slice(-3);
  }
</script>

<div class="max-w-3xl mx-auto">
  <h5 class="mb-1">API keys</h5>
  <p class="text-xs text-txtsecondary mb-4">
    Keys let other apps (e.g. an agent harness) reach the models over the OpenAI-compatible API. A key with no model
    scope has full access; otherwise it only sees and can call the models you pick. The local dashboard never needs a key.
  </p>

  {#if !available}
    <div class="card">
      <p class="text-sm text-txtsecondary">
        API-key management requires the server to run with <code class="font-mono">-generate</code>.
      </p>
    </div>
  {:else}
    {#if loadErr}
      <div class="card mb-4 border-error/50">
        <p class="text-sm text-error">{loadErr}</p>
      </div>
    {/if}

    <!-- Create -->
    <div class="card mb-4">
      <h6 class="!pb-0 mb-3">New key</h6>
      <div class="flex flex-wrap items-end gap-3">
        <label class="flex flex-col gap-1">
          <span class="text-[0.7rem] uppercase tracking-wide text-txtsecondary">Name</span>
          <input
            class="rounded border border-card-border bg-background px-2 py-1 font-mono text-sm"
            placeholder="e.g. pi-harness"
            bind:value={newName}
          />
        </label>
        <button class="btn btn--sm uppercase tracking-wide" onclick={create} disabled={saving}>
          <Plus size={14} /> Create
        </button>
      </div>

      <div class="mt-3">
        <button
          type="button"
          class="btn btn--sm uppercase tracking-wide"
          onclick={() => (showPicker = !showPicker)}
          disabled={grouped.length === 0}
        >
          {showPicker ? "Hide models" : "Choose models"}
          <span class="normal-case text-txtsecondary">
            ({newScope.size === 0 ? "full access" : `${newScope.size} selected`})
          </span>
        </button>
        {#if grouped.length === 0}
          <p class="mt-1 text-xs text-txtsecondary">No models in the catalog yet.</p>
        {:else if showPicker}
          <p class="mt-2 text-[0.7rem] uppercase tracking-wide text-txtsecondary">
            Allowed models <span class="normal-case">(none selected = full access)</span>
          </p>
          {@render picker(newScope, (next) => (newScope = next))}
        {/if}
      </div>
      {#if formErr}<p class="mt-2 text-xs text-error">{formErr}</p>{/if}
    </div>

    <!-- List -->
    {#if visibleKeys.length === 0}
      <p class="font-mono text-xs text-txtsecondary">No keys yet. Create one above.</p>
    {:else}
      <ul class="space-y-3">
        {#each visibleKeys as k (k.name)}
          <li class="card">
            <div class="flex items-center gap-3">
              <span class="font-mono text-sm font-bold text-txtmain">{k.name}</span>
              <span class="text-[0.7rem] uppercase tracking-wide text-txtsecondary">
                {k.models && k.models.length ? `${k.models.length} model${k.models.length > 1 ? "s" : ""}` : "full access"}
              </span>
              <button
                class="ml-auto btn btn--sm uppercase tracking-wide hover:border-error hover:text-error"
                onclick={() => remove(k.name)}
                title="Delete key"
              >
                <Trash2 size={14} />
              </button>
            </div>

            <!-- Secret -->
            <div class="mt-2 flex items-center gap-2">
              <code class="flex-1 truncate rounded border border-card-border bg-background px-2 py-1 font-mono text-xs">
                {revealed.has(k.name) ? k.key : mask(k.key)}
              </code>
              <button
                class="btn btn--sm"
                onclick={() => (revealed = toggle(revealed, k.name))}
                title={revealed.has(k.name) ? "Hide" : "Show"}
              >
                {#if revealed.has(k.name)}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
              </button>
              <button class="btn btn--sm" onclick={() => copy(k.key)} title="Copy">
                {#if copied === k.key}<Check size={14} class="text-primary" />{:else}<Copy size={14} />{/if}
              </button>
            </div>

            <!-- Scope -->
            <div class="mt-2">
              {#if editingScope === k.name}
                {@render picker(editScope, (next) => (editScope = next))}
                <div class="mt-2 flex gap-2">
                  <button class="btn btn--sm uppercase tracking-wide" onclick={() => saveScope(k.name)} disabled={saving}>
                    Save scope
                  </button>
                  <button class="btn btn--sm uppercase tracking-wide" onclick={() => (editingScope = null)}>Cancel</button>
                </div>
              {:else}
                <div class="flex flex-wrap items-center gap-1.5">
                  {#if k.models && k.models.length}
                    {#each k.models as m (m)}
                      <span class="rounded bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.7rem] text-txtsecondary">{m}</span>
                    {/each}
                  {:else}
                    <span class="font-mono text-[0.7rem] text-txtsecondary">every model</span>
                  {/if}
                  <button class="btn btn--sm uppercase tracking-wide ml-2" onclick={() => startEdit(k)}>Edit scope</button>
                </div>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<!-- Model picker grouped by category. `sel` is the current Set; `set` swaps in a new one. -->
{#snippet picker(sel: Set<string>, set: (next: Set<string>) => void)}
  <div class="mt-2 space-y-2">
    {#each grouped as g (g.label)}
      <div>
        <span class="text-[0.7rem] uppercase tracking-wide text-txtsecondary">{g.label}</span>
        <div class="mt-1 flex flex-wrap gap-2">
          {#each g.ids as id (id)}
            <button
              type="button"
              onclick={() => set(toggle(sel, id))}
              class="rounded border px-2 py-0.5 font-mono text-xs transition-colors {sel.has(id)
                ? 'border-primary text-primary bg-secondary/60'
                : 'border-card-border text-txtsecondary hover:text-txtmain'}"
            >
              {id}
            </button>
          {/each}
        </div>
      </div>
    {/each}
  </div>
{/snippet}
