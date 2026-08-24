<script lang="ts">
  // The app window's tabs, inline in the title bar.
  //
  // Sized to fit the bar as it already is (h-8 / 2rem): index.css keys
  // --qm-titlebar-h off that height and shortens every h-screen root by it, so a
  // taller bar is not a style tweak, it is a relayout of every full-height view
  // in both apps. The tabs are 24px pills inside the 32px bar instead.
  import { Zap, X, Plus, ExternalLink } from "lucide-svelte";
  import { appTabs, activeTabId, focusTab, closeTab, openTab } from "../stores/appTabs";
  import * as native from "../lib/native";

  let { playgroundURL = "" }: { playgroundURL?: string } = $props();

  // The whole bar is a drag handle, and a drag is a mousedown the window manager
  // steals -- the click never arrives. Every control up here has to opt out of
  // that, or it looks clickable and does nothing (see TitleBar's keepClickable).
  function keepClickable(e: MouseEvent): void {
    e.stopPropagation();
  }

  let menu = $state<{ id: string; x: number; y: number } | null>(null);

  function openMenu(e: MouseEvent, id: string): void {
    e.preventDefault(); // WebView2 would otherwise show Chromium's own menu
    e.stopPropagation();
    menu = { id, x: e.clientX, y: e.clientY };
  }

  function sendToBrowser(id: string, alsoClose: boolean): void {
    const tab = $appTabs.find((t) => t.id === id);
    menu = null;
    if (!tab) return;
    // The browser gets the PLAIN url: the embed params turn a page into a frame
    // that reports to a shell which, in a real browser tab, is not there.
    native.openExternal(tab.externalURL);
    if (alsoClose) closeTab(id);
  }

  // Middle-click closes, as everywhere else that has tabs. auxclick rather than
  // mousedown so it cannot fire mid-drag.
  function onAux(e: MouseEvent, id: string): void {
    if (e.button !== 1) return;
    e.preventDefault();
    closeTab(id);
  }

  $effect(() => {
    if (!menu) return;
    const close = () => (menu = null);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    // Capture, so a click on anything -- including another tab -- dismisses the
    // menu before that thing handles it.
    window.addEventListener("mousedown", close, true);
    window.addEventListener("keydown", onKey);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("mousedown", close, true);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  });
</script>

<!-- min-w-0 so long labels truncate inside the strip instead of pushing the
     window buttons off the right edge. -->
<div class="flex min-w-0 items-center gap-1" role="tablist">
  {#each $appTabs as tab (tab.id)}
    {@const active = $activeTabId === tab.id}
    <div
      class="group/tab flex h-6 min-w-0 max-w-[14rem] shrink items-center gap-1.5 rounded-md pl-2 pr-1 text-micro transition-colors {active
        ? 'bg-background text-txtmain'
        : 'text-txtsecondary hover:bg-secondary/40 hover:text-txtmain'}"
      onmousedown={keepClickable}
      ondblclick={keepClickable}
      oncontextmenu={(e) => openMenu(e, tab.id)}
      onauxclick={(e) => onAux(e, tab.id)}
      role="presentation"
    >
      <button
        type="button"
        class="flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 truncate text-left"
        role="tab"
        aria-selected={active}
        title={tab.label}
        onclick={() => focusTab(tab.id)}
      >
        {#if tab.busy}
          <!-- Same signal as the browser tab title's bolt, for a window that has
               no browser tab to carry it. -->
          <Zap size={11} class="shrink-0 text-primary" strokeWidth={2.4} />
        {/if}
        <span class="truncate">{tab.label}</span>
      </button>
      <button
        type="button"
        class="shrink-0 cursor-pointer rounded p-0.5 text-txtsecondary opacity-0 transition-opacity hover:bg-secondary hover:text-txtmain group-hover/tab:opacity-100 {active ? 'opacity-70' : ''}"
        title="Close tab"
        aria-label="Close tab"
        onclick={() => closeTab(tab.id)}
      >
        <X size={11} strokeWidth={2.4} />
      </button>
    </div>
  {/each}

  {#if playgroundURL}
    <button
      type="button"
      class="shrink-0 cursor-pointer rounded p-1 text-txtsecondary transition-colors hover:bg-secondary/40 hover:text-txtmain"
      title="New playground tab"
      aria-label="New playground tab"
      onmousedown={keepClickable}
      ondblclick={keepClickable}
      onclick={() => openTab(playgroundURL)}
    >
      <Plus size={12} strokeWidth={2.4} />
    </button>
  {/if}
</div>

{#if menu}
  <!-- Fixed to the viewport at the cursor: the strip itself is inside a 32px bar
       with overflow of its own, so a menu positioned within it would be clipped
       to a sliver. -->
  <div
    class="fixed z-[100] min-w-[11rem] overflow-hidden rounded-md border border-border bg-surface py-1 text-sm shadow-xl shadow-black/30"
    style="left: {menu.x}px; top: {menu.y}px"
    role="menu"
    tabindex="-1"
    onmousedown={(e) => e.stopPropagation()}
  >
    <button
      type="button"
      role="menuitem"
      class="flex w-full cursor-pointer items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary/60"
      onclick={() => sendToBrowser(menu!.id, false)}
    >
      <ExternalLink size={13} strokeWidth={1.8} />
      Open in browser
    </button>
    <button
      type="button"
      role="menuitem"
      class="flex w-full cursor-pointer items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary/60"
      onclick={() => sendToBrowser(menu!.id, true)}
    >
      <ExternalLink size={13} strokeWidth={1.8} />
      Move to browser
    </button>
    <div class="my-1 border-t border-border"></div>
    <button
      type="button"
      role="menuitem"
      class="flex w-full cursor-pointer items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary/60"
      onclick={() => {
        const id = menu!.id;
        menu = null;
        closeTab(id);
      }}
    >
      <X size={13} strokeWidth={1.8} />
      Close tab
    </button>
  </div>
{/if}
