<script lang="ts">
  import { link, location } from "svelte-spa-router";
  import { LayoutDashboard, Boxes, FlaskConical, Activity, KeyRound, Sun, Moon, MonitorCog, ChevronRight, ArrowUpCircle, MessageSquare, Image, Volume2, Mic, Binary, BookOpen, SlidersHorizontal } from "lucide-svelte";
  import WikiModal from "./WikiModal.svelte";
  import SettingsModal from "./SettingsModal.svelte";
  import { toggleTheme, themeMode, connectionState } from "../stores/theme";
  import { currentRoute } from "../stores/route";
  import { playgroundActivity } from "../stores/playgroundActivity";
  import { versionInfo } from "../stores/api";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";

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
  let showWiki = $state(false);
  let showSettings = $state(false);

  function isActive(path: string, current: string): boolean {
    return path === "/" ? current === "/" : current.startsWith(path);
  }

  // Connection health drives the version status bar (replaces the old dot).
  let connOk = $derived($connectionState === "connected");
  let statusTooltip = $derived(
    `Event Stream: ${$connectionState ?? "unknown"}\nAPI Version: ${$versionInfo?.version ?? "unknown"}\nCommit: ${$versionInfo?.commit?.substring(0, 7) ?? "unknown"}\nBuild Date: ${$versionInfo?.build_date ?? "unknown"}`
  );

  // Auto-update: launch the installer (server downloads it, then shuts down to
  // apply). Only shown when the backend reports an available release.
  let updating = $state(false);
  async function runUpdate(): Promise<void> {
    if (updating) return;
    const latest = $versionInfo.latest_version ?? "the latest version";
    if (!confirm(`Update to ${latest}?\n\nThe installer will launch and Quartermaster will shut down to apply it.`)) return;
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

  // Shared label styling: zero-width + invisible at rest (no reserved space, so
  // the icon stays centered in the collapsed rail), grows in on hover.
  const labelClass =
    "inline-block max-w-0 overflow-hidden whitespace-nowrap opacity-0 group-hover/rail:max-w-[8rem] group-hover/rail:opacity-100 transition-all duration-200 tracking-wide";
</script>

<!-- Icons only at rest; expands on hover (mirrors the playground side rail). -->
<aside class="group/rail flex flex-col gap-1 h-full w-14 hover:w-44 shrink-0 overflow-hidden transition-[width] duration-200 border-r border-border bg-surface py-2">
  <!-- Brand: collapses to "QM", expands to "Quartermaster Dashboard". -->
  <div class="pb-2 h-9 flex items-center font-mono text-xs uppercase tracking-[0.2em] text-primary leading-tight">
    <span class="w-14 shrink-0 flex items-center justify-center group-hover/rail:hidden">QM</span>
    <span class="hidden group-hover/rail:block whitespace-nowrap">Quartermaster<br />Dashboard</span>
  </div>

  <!-- Flat page list, no section headers -->
  <nav class="flex-1 overflow-y-auto overflow-x-hidden pretty-scroll flex flex-col gap-1">
    {#each pages as p (p.path)}
      {@const active = isActive(p.path, $currentRoute)}
      {#if p.children}
        <!-- Models: expandable parent. Clicking toggles the sub-menu. -->
        <button
          type="button"
          onclick={() => (modelsOpen = !modelsOpen)}
          class="relative w-full flex items-center gap-3 pr-3 py-2 font-mono text-sm transition-colors {active
            ? 'text-txtmain bg-secondary/60'
            : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
        >
          <span class="absolute left-0 top-0 bottom-0 w-0.5 {active ? 'bg-primary' : 'bg-transparent'}"></span>
          <span class="w-14 shrink-0 flex items-center justify-center">
            <p.icon size={18} strokeWidth={active ? 2.4 : 1.8} />
          </span>
          <span class={labelClass}>{p.label}</span>
          <ChevronRight size={14} class="ml-auto shrink-0 hidden group-hover/rail:block transition-transform {modelsOpen ? 'rotate-90' : ''}" />
        </button>
        {#if modelsOpen}
          <!-- Sub-items only make sense expanded; hidden while the rail is collapsed. -->
          <div class="hidden group-hover/rail:block">
            {#each p.children as c (c.path)}
              {@const cActive = $location === c.path || (c.path === "/models/llm" && $location === "/models")}
              <a
                href={c.path}
                use:link
                class="flex items-center gap-2.5 pl-8 pr-3 py-1.5 font-mono text-[0.8rem] border-l-2 transition-colors {cActive
                  ? 'border-primary text-txtmain bg-secondary/60'
                  : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
              >
                <c.icon size={14} strokeWidth={cActive ? 2.4 : 1.8} class="shrink-0" />
                <span class="tracking-wide whitespace-nowrap">{c.label}</span>
              </a>
            {/each}
          </div>
        {/if}
      {:else if p.path === "/test" && playgroundURL}
        <!-- Playground is a separate app on its own port: link out to it. -->
        <a
          href={playgroundURL}
          target="_blank"
          rel="noopener"
          class="relative flex items-center gap-3 pr-3 py-2 font-mono text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
        >
          <span class="w-14 shrink-0 flex items-center justify-center">
            <p.icon size={18} strokeWidth={1.8} />
          </span>
          <span class={labelClass}>{p.label}</span>
          {#if $playgroundActivity}
            <span class="ml-auto w-1.5 h-1.5 rounded-full bg-primary animate-pulse shrink-0"></span>
          {/if}
        </a>
      {:else}
        <a
          href={p.path}
          use:link
          class="relative flex items-center gap-3 pr-3 py-2 font-mono text-sm transition-colors {active
            ? 'text-txtmain bg-secondary/60'
            : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
        >
          <span class="absolute left-0 top-0 bottom-0 w-0.5 {active ? 'bg-primary' : 'bg-transparent'}"></span>
          <span class="w-14 shrink-0 flex items-center justify-center">
            <p.icon size={18} strokeWidth={active ? 2.4 : 1.8} />
          </span>
          <span class={labelClass}>{p.label}</span>
          {#if p.path === "/test" && $playgroundActivity}
            <span class="ml-auto w-1.5 h-1.5 rounded-full bg-primary animate-pulse shrink-0"></span>
          {/if}
        </a>
      {/if}
    {/each}
  </nav>

  <!-- Help: opens the quartermaster wiki modal (not a route). Sits just above
       the settings + theme/version footer. -->
  <button
    type="button"
    onclick={() => (showWiki = true)}
    class="w-full flex items-center gap-3 pr-3 py-2 font-mono text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
  >
    <span class="w-14 shrink-0 flex items-center justify-center">
      <BookOpen size={18} strokeWidth={1.8} />
    </span>
    <span class={labelClass}>Help</span>
  </button>

  <!-- Settings: below Help, above the theme/version footer. Opens as a modal,
       not a route (matches the Help button). -->
  <button
    type="button"
    onclick={() => (showSettings = true)}
    class="w-full flex items-center gap-3 pr-3 py-2 font-mono text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
  >
    <span class="w-14 shrink-0 flex items-center justify-center">
      <SlidersHorizontal size={18} strokeWidth={1.8} />
    </span>
    <span class={labelClass}>Settings</span>
  </button>

  <!-- Footer: theme toggle + version. Connection health is a full-height bar
       flush against the sidebar's right edge, same weight as the orange
       active-row accent — always visible, not just on hover. -->
  <div class="relative pr-3 py-2 flex items-center gap-2" title={statusTooltip}>
    <button
      class="w-14 shrink-0 flex items-center justify-center text-txtsecondary hover:text-txtmain transition-colors"
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
    {#if $versionInfo.update_available}
      <button
        class="hidden group-hover/rail:inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[0.6rem] font-medium text-primary hover:bg-primary/10 transition-colors disabled:opacity-60"
        onclick={runUpdate}
        disabled={updating}
        title="Update to {$versionInfo.latest_version}"
      >
        <ArrowUpCircle size={13} />
        {updating ? "Updating…" : "Update"}
      </button>
    {/if}
    <span class="ml-auto hidden group-hover/rail:inline-block font-mono text-[0.6rem] text-txtsecondary tabular-nums">
      {$versionInfo.version}
    </span>
    <span class="absolute right-0 top-0 bottom-0 w-0.5 {connOk ? 'bg-emerald-500' : 'bg-red-500'}"></span>
  </div>
</aside>

<WikiModal bind:open={showWiki} />
<SettingsModal bind:open={showSettings} />
