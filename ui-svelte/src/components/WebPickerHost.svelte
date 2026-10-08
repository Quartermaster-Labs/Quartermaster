<script lang="ts">
  import { ArrowUp, File, Folder, X } from "lucide-svelte";
  import { humanBytes } from "../lib/hubApi";
  import { pendingPick, listPickFolder, type PickListing } from "../lib/webPicker";

  // Renders whatever openWebPicker() put in the store (see lib/webPicker.ts).
  // Mounted once at the dashboard root.
  //
  // Native <dialog>+showModal for the same reason as ConfirmHost: Browse buttons
  // live inside ModelConfigModal, itself a top-layer dialog, and nothing but a
  // later showModal can paint above it.
  let dialogEl = $state<HTMLDialogElement | null>(null);

  let root = $state("");
  let path = $state("");
  let showAll = $state(false);
  let listing = $state<PickListing | null>(null);
  let loading = $state(false);
  let error = $state("");
  // The path Select will return. Clicking a file fills it; navigating a folder
  // fills it too when picking folders. Editable, so a path outside the browsable
  // roots can still be pasted.
  let chosen = $state("");

  let folderMode = $derived(($pendingPick?.kind ?? "folder") === "folder");

  function settle(p: string | null): void {
    const req = $pendingPick;
    pendingPick.set(null);
    req?.resolve(p);
  }

  // A new request starts from its own start path, on whichever root holds it.
  $effect(() => {
    const req = $pendingPick;
    if (!req) return;
    root = "";
    path = req.start?.trim() ?? "";
    showAll = false;
    chosen = req.start?.trim() ?? "";
  });

  $effect(() => {
    const el = dialogEl;
    if (!el) return;
    if ($pendingPick) {
      if (!el.open) el.showModal();
    } else if (el.open) {
      el.close();
    }
  });

  // Re-fetch whenever the location or filter changes; `stale` drops a slow
  // reply for a folder the user already left.
  $effect(() => {
    const req = $pendingPick;
    if (!req) return;
    const opts = { kind: req.kind, root, path, all: showAll };
    let stale = false;
    loading = true;
    error = "";
    listPickFolder(opts)
      .then((l) => {
        if (stale) return;
        listing = l;
        loading = false;
        if (folderMode) chosen = l.path;
      })
      .catch((e: unknown) => {
        if (stale) return;
        // A start path outside every root (a typed backend path, say) is not an
        // error worth showing: fall back to the first root and keep the field.
        if (path && !root) {
          path = "";
          return;
        }
        listing = null;
        error = e instanceof Error ? e.message : String(e);
        loading = false;
      });
    return () => {
      stale = true;
    };
  });

  let crumbs = $derived(
    (listing?.rel ?? "")
      .split("/")
      .filter(Boolean)
      .map((label, i, all) => ({ label, path: all.slice(0, i + 1).join("/") })),
  );
  let rootLabel = $derived(listing?.roots.find((r) => r.id === listing?.root)?.label ?? "");

  function go(nextRoot: string, nextPath: string): void {
    root = nextRoot;
    path = nextPath;
  }
</script>

{#if $pendingPick}
  {@const req = $pendingPick}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <dialog
    bind:this={dialogEl}
    class="bg-surface text-txtmain rounded-lg border border-card-border shadow-xl w-full max-w-3xl h-[calc(80vh/var(--qm-scale))] p-0 backdrop:bg-black/50 m-auto"
    aria-label={req.title}
    oncancel={(e) => {
      e.preventDefault();
      settle(null);
    }}
    onclick={(e) => e.target === dialogEl && settle(null)}
  >
    <div class="flex flex-col h-full">
      <div class="flex items-center justify-between px-4 py-3 border-b border-card-border">
        <div class="flex items-center gap-2 min-w-0">
          <Folder size={18} class="text-primary shrink-0" />
          <span class="text-sm font-medium truncate">{req.title}</span>
        </div>
        <button class="text-txtsecondary hover:text-txtmain transition-colors" onclick={() => settle(null)} aria-label="Close">
          <X size={18} />
        </button>
      </div>

      <div class="flex flex-1 min-h-0">
        <!-- Roots: the only places this picker can go. -->
        <div class="w-44 shrink-0 border-r border-card-border p-1.5 flex flex-col gap-0.5 overflow-y-auto pretty-scroll">
          {#each listing?.roots ?? [] as r (r.id)}
            <button
              class="text-left px-2 py-1 rounded-md text-[0.8125rem] truncate transition-colors {r.id === listing?.root
                ? 'bg-secondary/60 text-txtmain'
                : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
              title={r.path}
              onclick={() => go(r.id, "")}
            >
              {r.label}
            </button>
          {/each}
        </div>

        <div class="flex-1 min-w-0 flex flex-col">
          <div class="flex items-center gap-1 px-3 py-1.5 border-b border-card-border text-xs min-w-0">
            <button
              class="px-1.5 py-0.5 rounded-md transition-colors {crumbs.length
                ? 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'
                : 'text-txtmain'}"
              onclick={() => listing && go(listing.root, "")}
            >
              {rootLabel || "..."}
            </button>
            {#each crumbs as c (c.path)}
              <span class="text-txtsecondary/50">/</span>
              <button
                class="px-1.5 py-0.5 rounded-md max-w-40 truncate transition-colors {c.path === listing?.rel
                  ? 'text-txtmain'
                  : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
                onclick={() => listing && go(listing.root, c.path)}
              >
                {c.label}
              </button>
            {/each}
            <div class="flex-1"></div>
            {#if !folderMode && (listing?.filtered || showAll)}
              <label class="flex items-center gap-1 px-1.5 text-txtsecondary cursor-pointer select-none">
                <input type="checkbox" bind:checked={showAll} />
                All files
              </label>
            {/if}
            <button
              class="px-1.5 py-0.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary/50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
              onclick={() => listing && go(listing.root, listing.parent)}
              disabled={!listing?.parent}
              aria-label="Up one level"
            >
              <ArrowUp size={14} />
            </button>
          </div>

          <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll">
            {#if loading}
              <p class="px-4 py-3 text-xs text-txtsecondary">Loading...</p>
            {:else if error}
              <p class="px-4 py-3 text-xs text-error">{error}</p>
            {:else if listing && listing.entries.length === 0}
              <p class="px-4 py-3 text-xs text-txtsecondary">
                {folderMode ? "No subfolders here." : "Nothing to pick here."}
              </p>
            {:else if listing}
              <div class="p-1.5 flex flex-col gap-0.5">
                {#each listing.entries as e (e.path)}
                  <button
                    class="flex items-center gap-2 pl-2 pr-1 py-0.5 rounded-md text-left transition-colors {chosen === e.path
                      ? 'bg-secondary/60'
                      : 'hover:bg-secondary/50'}"
                    onclick={() => (e.dir ? listing && go(listing.root, e.path) : (chosen = e.path))}
                    ondblclick={() => !e.dir && settle(e.path)}
                  >
                    {#if e.dir}
                      <Folder size={14} class="shrink-0 text-primary" />
                    {:else}
                      <File size={14} class="shrink-0 text-txtsecondary" />
                    {/if}
                    <span class="flex-1 min-w-0 truncate text-[0.8125rem] text-txtmain">{e.name}</span>
                    <span class="shrink-0 w-20 text-right text-[0.7rem] text-txtsecondary tabular-nums">
                      {e.dir ? "" : humanBytes(e.size)}
                    </span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
          {#if listing?.truncated}
            <div class="px-4 py-2 border-t border-card-border text-[0.7rem] text-txtsecondary">
              Showing the first 4000 entries.
            </div>
          {/if}
        </div>
      </div>

      <div class="flex items-center gap-2 border-t border-card-border-inner px-4 py-3">
        <input
          type="text"
          spellcheck="false"
          class="flex-1 min-w-0 font-mono text-xs rounded border border-card-border bg-surface px-2 py-1 text-txtmain focus:outline-none focus:ring-2 focus:ring-primary"
          bind:value={chosen}
          placeholder={folderMode ? "Folder path" : "File path"}
          aria-label="Selected path"
          onkeydown={(e) => e.key === "Enter" && chosen.trim() && settle(chosen.trim())}
        />
        <button class="btn btn--sm" onclick={() => settle(null)}>Cancel</button>
        <button class="btn btn--sm btn--primary" disabled={!chosen.trim()} onclick={() => settle(chosen.trim())}>
          {folderMode ? "Use this folder" : "Select"}
        </button>
      </div>
    </div>
  </dialog>
{/if}
