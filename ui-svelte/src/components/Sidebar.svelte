<script lang="ts">
  import { link, location } from "svelte-spa-router";
  import { LayoutDashboard, Boxes, FlaskConical, Activity, KeyRound, Sun, Moon, MonitorCog, ChevronRight, ArrowUpCircle, MessageSquare, Image, Volume2, Mic, Binary } from "lucide-svelte";
  import { toggleTheme, themeMode, appTitle } from "../stores/theme";
  import { currentRoute } from "../stores/route";
  import { playgroundActivity } from "../stores/playgroundActivity";
  import { versionInfo } from "../stores/api";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import ConnectionStatus from "./ConnectionStatus.svelte";

  const CATEGORY_ICONS: Record<ModelCategory, typeof MessageSquare> = {
    llm: MessageSquare,
    image: Image,
    tts: Volume2,
    transcribe: Mic,
    embed: Binary,
  };

  const pages = [
    { path: "/", label: "Dashboard", icon: LayoutDashboard },
    { path: "/models", label: "Models", icon: Boxes, children: MODEL_CATEGORIES.map((c) => ({ path: `/models/${c.id}`, label: c.label, icon: CATEGORY_ICONS[c.id] })) },
    { path: "/test", label: "Playground", icon: FlaskConical },
    { path: "/observe", label: "Observe", icon: Activity },
    { path: "/api-keys", label: "API Keys", icon: KeyRound },
  ];

  // The playground runs on its own port; link out to it when configured.
  const playgroundURL = $derived(
    $playgroundPort ? `${window.location.protocol}//${window.location.hostname}:${$playgroundPort}/ui/` : ""
  );

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

  // Auto-update: launch the installer (server downloads it, then shuts down to
  // apply). Only shown when the backend reports an available release.
  let updating = $state(false);
  async function runUpdate(): Promise<void> {
    if (updating) return;
    const latest = $versionInfo.latest_version ?? "the latest version";
    if (!confirm(`Update to ${latest}?\n\nThe installer will launch and llama-quartermaster will shut down to apply it.`)) return;
    updating = true;
    try {
      const r = await fetch("/api/update", { method: "POST" });
      if (!r.ok) {
        alert("Update failed: " + (await r.text()));
        updating = false;
      }
      // On success the server shuts down; keep the spinner until the page drops.
    } catch (e) {
      alert("Update failed: " + e);
      updating = false;
    }
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
  <nav class="flex-1 overflow-y-auto py-2 pretty-scroll">
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
              class="flex items-center gap-2.5 pl-8 pr-3 py-1.5 font-mono text-[0.8rem] border-l-2 transition-colors {cActive
                ? 'border-primary text-primary bg-secondary/60'
                : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
            >
              <c.icon size={14} strokeWidth={cActive ? 2.4 : 1.8} />
              <span class="tracking-wide">{c.label}</span>
            </a>
          {/each}
        {/if}
      {:else if p.path === "/test" && playgroundURL}
        <!-- Playground is a separate app on its own port: link out to it. -->
        <a
          href={playgroundURL}
          target="_blank"
          rel="noopener"
          class="flex items-center gap-3 px-3 py-2 font-mono text-sm border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
        >
          <p.icon size={16} strokeWidth={1.8} />
          <span class="tracking-wide">{p.label}</span>
          {#if $playgroundActivity}
            <span class="ml-auto w-1.5 h-1.5 rounded-full bg-primary animate-pulse"></span>
          {/if}
        </a>
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
    {#if $versionInfo.update_available}
      <button
        class="flex items-center gap-1 rounded px-1.5 py-0.5 text-[0.6rem] font-medium text-primary hover:bg-primary/10 transition-colors disabled:opacity-60"
        onclick={runUpdate}
        disabled={updating}
        title="Update to {$versionInfo.latest_version}"
      >
        <ArrowUpCircle size={13} />
        {updating ? "Updating…" : "Update"}
      </button>
    {/if}
    <span class="font-mono text-[0.6rem] text-txtsecondary tabular-nums" title="commit {$versionInfo.commit}">
      {$versionInfo.version}
    </span>
  </div>
</aside>
