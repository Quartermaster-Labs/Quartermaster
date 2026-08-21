<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { link } from "svelte-spa-router";
  import { LayoutDashboard, Boxes, Layers, FlaskConical, Activity, KeyRound, ArrowUpCircle, BookOpen, Settings } from "lucide-svelte";
  import WikiModal from "./WikiModal.svelte";
  import SettingsModal from "./SettingsModal.svelte";
  import { currentRoute } from "../stores/route";
  import { playgroundActivity } from "../stores/playgroundActivity";
  import { versionInfo } from "../stores/api";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { updateStatus, updateBusy, resumePolling, updateProgressLabel } from "../stores/update";
  const pages = [
    { path: "/", label: "Dashboard", icon: LayoutDashboard },
    // Models is ONE page now — the category split is tabs on the page itself,
    // so a sub-menu duplicating them would be two controls for one choice.
    { path: "/models", label: "Models", icon: Boxes },
    // Acquiring a model is its own task, not a view of the local catalog, so it
    // gets a page rather than another category tab on Models.
    { path: "/browse", label: "Browse", icon: Layers },
    { path: "/test", label: "Playground", icon: FlaskConical },
    { path: "/observe", label: "Observe", icon: Activity },
    { path: "/api-keys", label: "API Keys", icon: KeyRound },
  ];

  // The playground runs on its own port; link out to it when configured.
  const playgroundURL = $derived(
    $playgroundPort ? `${window.location.protocol}//${window.location.hostname}:${$playgroundPort}/ui/` : ""
  );

  let showWiki = $state(false);
  let showSettings = $state(false);

  function isActive(path: string, current: string): boolean {
    return path === "/" ? current === "/" : current.startsWith(path);
  }

  // Applying an update is Settings -> System's job now that the rail has no
  // footer; this only advertises that there is one, and names what is running.
  let updateTooltip = $derived(
    $versionInfo.update_available
      ? `${updateProgressLabel($updateStatus, $updateBusy)} - ${$versionInfo.latest_version} available (Settings -> System)`
      : `Settings - running ${$versionInfo?.version ?? "unknown"}`
  );

  // The apply runs on the server, so a reload mid-download does not cancel it —
  // but it does leave this tab thinking nothing is happening. /api/version
  // carries the phase, so pick the progress back up instead.
  $effect(() => resumePolling($versionInfo.update_phase));

  // Shared label styling: zero-width + invisible at rest (no reserved space, so
  // the icon stays centered in the collapsed rail), grows in on hover.
  const labelClass =
    "inline-block max-w-0 overflow-hidden whitespace-nowrap opacity-0 group-hover/rail:max-w-[8rem] group-hover/rail:opacity-100 transition-all duration-200 tracking-wide";
</script>

<!-- Icons only at rest; expands on hover (mirrors the playground side rail). -->
<aside
  class="group/rail absolute inset-y-0 left-0 flex flex-col gap-1 w-14 hover:w-44 overflow-hidden transition-[width] duration-200 border-r border-border bg-surface py-2 hover:shadow-xl hover:shadow-black/20"
>
  <!-- Brand: collapses to "QM", expands to "Quartermaster Dashboard". Same
       fixed-spacer + growing-label pattern as nav rows below, so the label
       doesn't jump left/up when the rail expands. -->
  <div class="relative pb-2 h-9 flex items-center text-label font-semibold uppercase tracking-[0.2em] text-primary leading-tight">
    <span class="w-14 shrink-0 flex items-center justify-center group-hover/rail:hidden">QM</span>
    <span class="hidden group-hover/rail:block absolute left-0 top-1/2 -translate-y-[0.72rem] whitespace-nowrap pl-[1.2rem] leading-tight">Quartermaster<br />Dashboard</span>
  </div>

  <!-- Flat page list, no section headers -->
  <nav class="flex-1 overflow-y-auto overflow-x-hidden pretty-scroll flex flex-col gap-1">
    {#each pages as p (p.path)}
      {@const active = isActive(p.path, $currentRoute)}
      {#if p.path === "/test" && playgroundURL}
        <!-- Playground is a separate app on its own port: link out to it. -->
        <a
          href={playgroundURL}
          target="_blank"
          rel="noopener"
          class="relative flex items-center gap-3 pr-3 py-2 text-sm font-medium text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
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
          class="relative flex items-center gap-3 pr-3 py-2 text-sm font-medium transition-colors {active
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
    class="w-full flex items-center gap-3 pr-3 py-2 text-sm font-medium text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
  >
    <span class="w-14 shrink-0 flex items-center justify-center">
      <BookOpen size={18} strokeWidth={1.8} />
    </span>
    <span class={labelClass}>Help</span>
  </button>

  <!-- Settings is the last row on the rail. It carries the update affordance
       the version footer used to: build details live in Settings → System, and
       an available update shows as a badge here rather than as its own row. -->
  <button
    type="button"
    onclick={() => (showSettings = true)}
    class="w-full flex items-center gap-3 pr-3 py-2 text-sm font-medium text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    use:tip={updateTooltip}
  >
    <span class="relative w-14 shrink-0 flex items-center justify-center">
      <Settings size={18} strokeWidth={1.8} />
      {#if $versionInfo.update_available}
        <span class="absolute top-0 right-3 w-1.5 h-1.5 rounded-full bg-primary"></span>
      {/if}
    </span>
    <span class={labelClass}>Settings</span>
    {#if $versionInfo.update_available}
      <ArrowUpCircle size={13} class="ml-auto mr-1 shrink-0 text-primary hidden group-hover/rail:inline-block" />
    {/if}
  </button>
</aside>

<WikiModal bind:open={showWiki} />
<SettingsModal bind:open={showSettings} />
