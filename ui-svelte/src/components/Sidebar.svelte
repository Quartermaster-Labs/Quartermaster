<script lang="ts">
  import { link, location } from "svelte-spa-router";
  import { LayoutDashboard, Boxes, FlaskConical, Activity, Sun, Moon, MonitorCog, ChevronRight } from "lucide-svelte";
  import { toggleTheme, themeMode, appTitle } from "../stores/theme";
  import { currentRoute } from "../stores/route";
  import { playgroundActivity } from "../stores/playgroundActivity";
  import { versionInfo } from "../stores/api";
  import { MODEL_CATEGORIES } from "../lib/modelUtils";
  import ConnectionStatus from "./ConnectionStatus.svelte";

  const pages = [
    { path: "/", label: "Dashboard", icon: LayoutDashboard },
    { path: "/models", label: "Models", icon: Boxes, children: MODEL_CATEGORIES.map((c) => ({ path: `/models/${c.id}`, label: c.label })) },
    { path: "/test", label: "Test", icon: FlaskConical },
    { path: "/observe", label: "Observe", icon: Activity },
  ];

  // Models sub-menu open when on any /models route, toggleable otherwise.
  let modelsOpen = $state($currentRoute.startsWith("/models"));

  function isActive(path: string, current: string): boolean {
    return path === "/" ? current === "/" : current.startsWith(path);
  }

  function handleTitleChange(newTitle: string): void {
    appTitle.set(newTitle.replace(/\n/g, "").trim().substring(0, 64) || "llama-quartermaster");
  }
  function handleKeyDown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      const t = e.currentTarget as HTMLElement;
      handleTitleChange(t.textContent || "");
      t.blur();
    }
  }
  function handleBlur(e: FocusEvent): void {
    const t = e.currentTarget as HTMLElement;
    handleTitleChange(t.textContent || "");
  }
</script>

<aside class="flex flex-col h-full w-44 shrink-0 border-r border-border bg-surface">
  <!-- Brand + editable instance title -->
  <div class="px-3 pt-4 pb-3 border-b border-card-border-inner">
    <div class="font-mono text-[0.65rem] uppercase tracking-[0.2em] text-primary">Quartermaster</div>
    <div
      contenteditable="true"
      role="textbox"
      tabindex="0"
      aria-label="Instance title"
      class="mt-1 font-mono text-base font-bold text-txtmain outline-none rounded px-1 -mx-1 hover:bg-secondary truncate"
      onblur={handleBlur}
      onkeydown={handleKeyDown}
      title="Click to rename this instance"
    >
      {$appTitle}
    </div>
  </div>

  <!-- Flat page list, no section headers -->
  <nav class="flex-1 overflow-y-auto py-2">
    {#each pages as p (p.path)}
      {@const active = isActive(p.path, $currentRoute)}
      {#if p.children}
        <!-- Models: expandable parent. Clicking toggles the sub-menu. -->
        <button
          type="button"
          onclick={() => (modelsOpen = !modelsOpen)}
          class="w-full flex items-center gap-3 px-3 py-2 font-mono text-sm border-l-2 transition-colors {active
            ? 'border-primary text-primary bg-secondary/60'
            : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
        >
          <p.icon size={16} strokeWidth={active ? 2.4 : 1.8} />
          <span class="tracking-wide">{p.label}</span>
          <ChevronRight size={14} class="ml-auto transition-transform {modelsOpen ? 'rotate-90' : ''}" />
        </button>
        {#if modelsOpen}
          {#each p.children as c (c.path)}
            {@const cActive = $location === c.path || (c.path === "/models/llm" && $location === "/models")}
            <a
              href={c.path}
              use:link
              class="flex items-center gap-3 pl-11 pr-3 py-1.5 font-mono text-[0.8rem] border-l-2 transition-colors {cActive
                ? 'border-primary text-primary bg-secondary/60'
                : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
            >
              <span class="tracking-wide">{c.label}</span>
            </a>
          {/each}
        {/if}
      {:else}
        <a
          href={p.path}
          use:link
          class="flex items-center gap-3 px-3 py-2 font-mono text-sm border-l-2 transition-colors {active
            ? 'border-primary text-primary bg-secondary/60'
            : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
        >
          <p.icon size={16} strokeWidth={active ? 2.4 : 1.8} />
          <span class="tracking-wide">{p.label}</span>
          {#if p.path === "/test" && $playgroundActivity}
            <span class="ml-auto w-1.5 h-1.5 rounded-full bg-primary animate-pulse"></span>
          {/if}
        </a>
      {/if}
    {/each}
  </nav>

  <!-- Footer: theme, connection, version -->
  <div class="border-t border-card-border-inner px-3 py-3 flex items-center justify-between">
    <button
      class="text-txtsecondary hover:text-txtmain transition-colors"
      onclick={toggleTheme}
      title="Toggle theme (current: {$themeMode})"
      aria-label="Toggle theme"
    >
      {#if $themeMode === "system"}
        <MonitorCog size={18} />
      {:else if $themeMode === "light"}
        <Sun size={18} />
      {:else}
        <Moon size={18} />
      {/if}
    </button>
    <ConnectionStatus />
    <span class="font-mono text-[0.6rem] text-txtsecondary tabular-nums" title="commit {$versionInfo.commit}">
      {$versionInfo.version}
    </span>
  </div>
</aside>
