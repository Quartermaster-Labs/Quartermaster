<script lang="ts">
  import UIScaleControl from "./UIScaleControl.svelte";
  import { tip } from "../lib/tooltip";
  import { link } from "svelte-spa-router";
  import { LayoutDashboard, Boxes, Layers, FlaskConical, Activity, KeyRound, Sun, Moon, MonitorCog, ArrowUpCircle, BookOpen, Settings } from "lucide-svelte";
  import WikiModal from "./WikiModal.svelte";
  import SettingsModal from "./SettingsModal.svelte";
  import { toggleTheme, themeMode, connectionState } from "../stores/theme";
  import { currentRoute } from "../stores/route";
  import { playgroundActivity } from "../stores/playgroundActivity";
  import { versionInfo } from "../stores/api";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { askConfirm, notify } from "../lib/confirm";
  import type { UpdateStatus } from "../lib/types";
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

  // Connection health drives the version status bar (replaces the old dot).
  let connOk = $derived($connectionState === "connected");
  let statusTooltip = $derived(
    `Event Stream: ${$connectionState ?? "unknown"}\nAPI Version: ${$versionInfo?.version ?? "unknown"}\nCommit: ${$versionInfo?.commit?.substring(0, 7) ?? "unknown"}\nBuild Date: ${$versionInfo?.build_date ?? "unknown"}`
  );

  // Auto-update. The server downloads the new binary, verifies it, and renames
  // it over the running exe — no installer, no wizard. POST /api/update returns
  // 202 immediately and the work continues on the SERVER's lifetime, so we poll
  // for progress rather than holding a request open across a 40MB download.
  let updating = $state(false);
  let progress = $state<UpdateStatus | null>(null);
  let poller: ReturnType<typeof setInterval> | null = null;

  // Phase -> what the button says. "staging" is the rename pair: brief, but it
  // is the only moment the install is mid-swap, so it gets its own label.
  const phaseLabel: Record<string, string> = {
    downloading: "Downloading…",
    verifying: "Verifying…",
    staging: "Installing…",
    ready: "Restarting…",
  };

  let updateLabel = $derived.by(() => {
    if (!updating) return "Update";
    const p = progress?.phase ?? "";
    if (p === "downloading" && progress && progress.total > 0) {
      return `${Math.round((progress.done / progress.total) * 100)}%`;
    }
    return phaseLabel[p] ?? "Updating…";
  });

  function stopPolling(): void {
    if (poller !== null) {
      clearInterval(poller);
      poller = null;
    }
  }

  async function pollStatus(): Promise<void> {
    let st: UpdateStatus;
    try {
      const r = await fetch("/api/update/status");
      if (!r.ok) return;
      st = await r.json();
    } catch {
      // The server going away mid-poll is the EXPECTED end of an auto-restart
      // update, not a failure — keep the spinner and let the reload land.
      return;
    }
    progress = st;

    if (st.phase === "error") {
      stopPolling();
      updating = false;
      await notify("Update failed", st.error || "The update did not complete.");
      return;
    }
    if (st.phase !== "ready") return;

    // Swapped. Who restarts depends on how this install is run.
    stopPolling();
    if (st.restart === "manual") {
      updating = false;
      await notify(
        `Update to ${st.latest} installed`,
        "Restart the Quartermaster service to finish — the new version is already in place."
      );
      return;
    }
    // Auto: the server is shutting down and relaunching itself. Give it a moment
    // to come back on the same port, then reload into the new build.
    setTimeout(() => window.location.reload(), 4000);
  }

  async function runUpdate(): Promise<void> {
    if (updating) return;
    const latest = $versionInfo.latest_version ?? "the latest version";
    const auto = ($versionInfo.update_restart ?? "auto") === "auto";
    const ok = await askConfirm({
      title: `Update to ${latest}?`,
      body: auto
        ? "Quartermaster will download the new version in the background, then restart itself. Any loaded model is unloaded."
        : "Quartermaster will install the new version in the background. It runs as a service here, so restart the service when you're ready to switch to it.",
      confirmLabel: "Update",
    });
    if (!ok) return;
    updating = true;
    progress = null;
    try {
      const r = await fetch("/api/update", { method: "POST" });
      if (!r.ok) {
        await notify("Update failed", await r.text());
        updating = false;
        return;
      }
    } catch (e) {
      await notify("Update failed", String(e));
      updating = false;
      return;
    }
    // 202 accepted — the work is running server-side now.
    void pollStatus();
    poller = setInterval(() => void pollStatus(), 1000);
  }

  // The apply runs on the server, so a reload mid-download does not cancel it —
  // but it does leave this component thinking nothing is happening, and a second
  // click would just bounce off the already-in-progress guard. /api/version
  // carries the phase, so pick the progress back up instead.
  $effect(() => {
    const phase = $versionInfo.update_phase;
    if (updating || poller !== null) return;
    if (phase !== "downloading" && phase !== "verifying" && phase !== "staging") return;
    updating = true;
    void pollStatus();
    poller = setInterval(() => void pollStatus(), 1000);
  });

  $effect(() => stopPolling);

  // Shared label styling: zero-width + invisible at rest (no reserved space, so
  // the icon stays centered in the collapsed rail), grows in on hover.
  const labelClass =
    "inline-block max-w-0 overflow-hidden whitespace-nowrap opacity-0 group-hover/rail:max-w-[8rem] group-hover/rail:opacity-100 transition-all duration-200 tracking-wide";
</script>

<!-- Icons only at rest; expands on hover (mirrors the playground side rail). -->
<aside class="group/rail flex flex-col gap-1 h-full w-14 hover:w-44 shrink-0 overflow-hidden transition-[width] duration-200 border-r border-border bg-surface py-2">
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

  <!-- Settings: below Help, above the theme/version footer. Opens as a modal,
       not a route (matches the Help button). -->
  <button
    type="button"
    onclick={() => (showSettings = true)}
    class="w-full flex items-center gap-3 pr-3 py-2 text-sm font-medium text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
  >
    <span class="w-14 shrink-0 flex items-center justify-center">
      <Settings size={18} strokeWidth={1.8} />
    </span>
    <span class={labelClass}>Settings</span>
  </button>

  <!-- Footer: theme toggle + version. Connection health is a full-height bar
       flush against the sidebar's right edge, same weight as the orange
       active-row accent — always visible, not just on hover. -->
  <div class="relative pr-3 py-2 flex items-center gap-2" use:tip={statusTooltip}>
    <button
      class="w-14 shrink-0 flex items-center justify-center text-txtsecondary hover:text-txtmain transition-colors"
      onclick={toggleTheme}
      use:tip={`Toggle theme (current: ${$themeMode})`}
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
    <!-- Hover-only, like the update button and version below it: the collapsed
         rail is 14 units of icon and has no room for a three-button group. -->
    <div class="hidden group-hover/rail:flex" use:tip={"Interface size (Ctrl+Plus / Ctrl+Minus / Ctrl+0)"}>
      <UIScaleControl compact />
    </div>
    {#if $versionInfo.update_available && $versionInfo.update_blocked}
      <!-- A new version exists but this install cannot swap its own binary
           (container, read-only directory). Link to the release instead of
           offering a button that can only fail. -->
      <a
        class="hidden group-hover/rail:inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-micro font-medium text-txtsecondary hover:bg-primary/10 transition-colors"
        href={$versionInfo.release_url}
        target="_blank"
        rel="noopener noreferrer"
        use:tip={`${$versionInfo.latest_version} is available, but cannot be installed automatically: ${$versionInfo.update_blocked}`}
      >
        <ArrowUpCircle size={13} />
        {$versionInfo.latest_version}
      </a>
    {:else if $versionInfo.update_available}
      <button
        class="hidden group-hover/rail:inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-micro font-medium text-primary hover:bg-primary/10 transition-colors disabled:opacity-60"
        onclick={runUpdate}
        disabled={updating}
        use:tip={updating
          ? "Installing in the background — you can keep working until it restarts"
          : `Update to ${$versionInfo.latest_version}`}
      >
        <ArrowUpCircle size={13} />
        {updateLabel}
      </button>
    {/if}
    <span class="ml-auto hidden group-hover/rail:inline-block font-mono text-micro text-txtsecondary tabular-nums">
      {$versionInfo.version}
    </span>
    <span class="absolute right-0 top-0 bottom-0 w-0.5 {connOk ? 'bg-success' : 'bg-error'}"></span>
  </div>
</aside>

<WikiModal bind:open={showWiki} />
<SettingsModal bind:open={showSettings} />
