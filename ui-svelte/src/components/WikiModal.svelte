<script lang="ts">
  import { BookOpen, Search, X } from "lucide-svelte";
  import { WIKI_ARTICLES, searchWiki, groupWikiArticles, type WikiArticle } from "../lib/wiki";
  import { renderMarkdown } from "../lib/markdown";

  // `articleId` opens the modal directly to a given article (e.g. a chat wiki
  // citation chip). Null = default to the first article.
  let { open = $bindable(false), articleId = null }: { open?: boolean; articleId?: string | null } = $props();

  let query = $state("");
  let selectedId = $state(WIKI_ARTICLES[0].id);

  // Jump to the requested article when the modal is opened with an articleId.
  // Reads only open+articleId, so a later manual topic click won't be re-forced.
  $effect(() => {
    if (open && articleId && WIKI_ARTICLES.some((a) => a.id === articleId)) {
      selectedId = articleId;
    }
  });

  // Filter the title list by the same scorer the model uses; empty query = all.
  let list = $derived<WikiArticle[]>(query.trim() ? searchWiki(query) : WIKI_ARTICLES);
  let groups = $derived(groupWikiArticles(list));
  let selected = $derived(WIKI_ARTICLES.find((a) => a.id === selectedId) ?? WIKI_ARTICLES[0]);

  // Keep the selection valid as the filtered list changes.
  $effect(() => {
    if (list.length && !list.some((a) => a.id === selectedId)) selectedId = list[0].id;
  });

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
      aria-label="Help"
    >
      <div class="flex items-center justify-between px-4 py-3 border-b border-card-border">
        <div class="flex items-center gap-2 min-w-0 text-txtmain">
          <BookOpen size={18} class="text-primary" />
          <span class="text-sm font-medium shrink-0">Quartermaster Help</span>
          <span class="text-txtsecondary text-xs">·</span>
          <span class="text-txtsecondary text-xs truncate">Tip: you can ask the assistant in the playground for help with using Quartermaster</span>
        </div>
        <button class="text-txtsecondary hover:text-txtmain transition-colors" onclick={close} aria-label="Close">
          <X size={18} />
        </button>
      </div>

      <div class="flex flex-1 min-h-0">
        <!-- Topic list -->
        <div class="w-56 shrink-0 border-r border-card-border flex flex-col">
          <div class="p-2 border-b border-card-border">
            <div class="flex items-center gap-1.5 px-2 py-1.5 rounded-md border border-card-border bg-background">
              <Search size={14} class="shrink-0 text-txtsecondary" />
              <input
                type="text"
                placeholder="Search help…"
                bind:value={query}
                class="flex-1 min-w-0 bg-transparent text-sm focus:outline-none text-txtmain placeholder:text-txtsecondary"
              />
            </div>
          </div>
          <div class="flex-1 overflow-y-auto pretty-scroll p-1.5 flex flex-col gap-2">
            {#each groups as group (group.title)}
              <div class="flex flex-col gap-0.5">
                <div class="px-2.5 pt-1 pb-0.5 text-[0.65rem] font-semibold uppercase tracking-wider text-txtsecondary/70">
                  {group.title}
                </div>
                {#each group.items as a (a.id)}
                  <button
                    onclick={() => (selectedId = a.id)}
                    class="text-left px-2.5 py-1.5 rounded-md text-[0.8125rem] transition-colors {a.id === selectedId
                      ? 'bg-primary/15 text-primary'
                      : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/50'}"
                  >
                    {a.title}
                  </button>
                {/each}
              </div>
            {:else}
              <p class="px-2.5 py-2 text-xs text-txtsecondary">No topic matches.</p>
            {/each}
          </div>
        </div>

        <!-- Article body -->
        <div class="flex-1 min-w-0 overflow-y-auto pretty-scroll p-5">
          <h2 class="text-lg font-semibold text-txtmain mb-3">{selected.title}</h2>
          <div class="prose prose-sm dark:prose-invert max-w-none chat-prose">
            {@html renderMarkdown(selected.body)}
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}
