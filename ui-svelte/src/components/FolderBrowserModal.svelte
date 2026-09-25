<script lang="ts">
  import { ArrowUp, Copy, File, Folder, X } from "lucide-svelte";
  import { listHubFolder, humanBytes, type HubFolder, type HubFileEntry } from "../lib/hubApi";

  // Read-only view of the models folder: a headless box has no file manager to open.
  let { open = $bindable(false), startPath = '' }: { open?: boolean; startPath?: string } = $props();

  let path = $state(""); // "" = models root
  let folder = $state<HubFolder | null>(null);
  let loading = $state(false);
  let error = $state("");
  let copied = $state("");

  // Each open starts at startPath; after that the user's clicks own `path`.
  $effect(() => {
    if (open) {
      path = startPath;
      copied = "";
    }
  });

  // Re-fetch on every path change; `stale` drops a slow reply for a folder the
  // user has already navigated away from (or closed the modal on).
  $effect(() => {
    if (!open) return;
    const target = path;
    let stale = false;
    loading = true;
    error = "";
    listHubFolder(target)
      .then((f) => {
        if (stale) return;
        folder = f;
        loading = false;
      })
      .catch((e: unknown) => {
        if (stale) return;
        folder = null;
        error = e instanceof Error ? e.message : String(e);
        loading = false;
      });
    return () => {
      stale = true;
    };
  });

  // rel is the slash path under the root; every prefix of it is a crumb.
  let crumbs = $derived(
    (folder?.rel ?? "")
      .split("/")
      .filter(Boolean)
      .map((label, i, all) => ({ label, path: all.slice(0, i + 1).join("/") })),
  );

  // Dirs report a size on some filesystems; ISO timestamps carry more than a row shows.
  const sizeLabel = (e: HubFileEntry) => (e.dir ? "" : humanBytes(e.size));
  const asDate = (s: string) => s.slice(0, 10);

  function navigate(target: string) {
    path = target;
    copied = "";
  }

  function copy(text: string) {
    // Clipboard access is denied outside a secure context; flash Copied anyway so
    // the click is never silently dead.
    navigator.clipboard?.writeText(text).catch(() => {});
    copied = text;
    setTimeout(() => (copied = copied === text ? "" : copied), 1500);
  }

  function close() {
    open = false;
  }
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={close}
    onkeydown={(e) => e.key === "Escape" && close()}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-full max-w-3xl h-[calc(80vh/var(--qm-scale))] flex flex-col rounded-lg border border-card-border bg-surface shadow-xl overflow-hidden"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
      aria-label="Models folder"
    >
      <div class="flex items-center justify-between px-4 py-3 border-b border-card-border">
        <div class="flex items-center gap-2 min-w-0 text-txtmain">
          <Folder size={18} class="text-primary" />
          <span class="text-sm font-medium shrink-0">Models folder</span>
          <span class="text-txtsecondary text-xs">·</span>
          <span class="text-txtsecondary text-xs truncate">{folder?.path ?? startPath}</span>
        </div>
        <button class="text-txtsecondary hover:text-txtmain transition-colors" onclick={close} aria-label="Close">
          <X size={18} />
        </button>
      </div>

      <div class="flex items-center gap-1 px-3 py-1.5 border-b border-card-border text-xs min-w-0">
        <button
          class="px-1.5 py-0.5 rounded-md transition-colors {crumbs.length
            ? 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'
            : 'text-txtmain'}"
          onclick={() => navigate("")}
        >
          models
        </button>
        {#each crumbs as c (c.path)}
          <span class="text-txtsecondary/50">/</span>
          <button
            class="px-1.5 py-0.5 rounded-md max-w-40 truncate transition-colors {c.path === folder?.rel
              ? 'text-txtmain'
              : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
            onclick={() => navigate(c.path)}
          >
            {c.label}
          </button>
        {/each}
        <div class="flex-1"></div>
        <button
          class="flex items-center gap-1 px-1.5 py-0.5 rounded-md transition-colors disabled:opacity-40 disabled:cursor-not-allowed {copied === folder?.path
            ? 'text-primary'
            : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
          onclick={() => folder && copy(folder.path)}
          disabled={!folder}
          aria-label="Copy folder path"
        >
          <Copy size={13} />
          {copied === folder?.path ? "Copied" : ""}
        </button>
        <button
          class="px-1.5 py-0.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary/50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          onclick={() => folder && navigate(folder.parent)}
          disabled={!folder?.parent}
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
        {:else if folder && folder.entries.length === 0}
          <p class="px-4 py-3 text-xs text-txtsecondary">This folder is empty.</p>
        {:else if folder}
          <div class="p-1.5 flex flex-col gap-0.5">
            {#each folder.entries as e (e.path)}
              <div class="flex items-center gap-2 pl-2 pr-1 rounded-md {e.dir ? 'hover:bg-secondary/50' : ''}">
                <!-- A file is not a destination, so its label button is disabled
                     rather than clickable-looking. -->
                <button
                  class="flex items-center gap-2 flex-1 min-w-0 text-left disabled:cursor-default"
                  disabled={!e.dir}
                  onclick={() => navigate(e.path)}
                >
                  {#if e.dir}
                    <Folder size={14} class="shrink-0 text-primary" />
                  {:else}
                    <File size={14} class="shrink-0 text-txtsecondary" />
                  {/if}
                  <span class="flex-1 min-w-0 truncate text-[0.8125rem] text-txtmain">{e.name}</span>
                </button>
                {#if e.modified}
                  <span class="shrink-0 text-[0.7rem] text-txtsecondary/60">{asDate(e.modified)}</span>
                {/if}
                <span class="shrink-0 w-20 text-right text-[0.7rem] text-txtsecondary tabular-nums">{sizeLabel(e)}</span>
                <button
                  class="shrink-0 flex items-center gap-1 px-1 py-0.5 rounded-md text-[0.7rem] transition-colors {copied === e.path
                    ? 'text-primary'
                    : 'text-txtsecondary/60 hover:text-txtmain'}"
                  onclick={() => copy(e.path)}
                  aria-label="Copy path"
                >
                  <Copy size={13} />
                  {copied === e.path ? "Copied" : ""}
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      {#if folder?.truncated}
        <div class="px-4 py-2 border-t border-card-border text-[0.7rem] text-txtsecondary">
          Showing the first 4000 entries.
        </div>
      {/if}
    </div>
  </div>
{/if}
