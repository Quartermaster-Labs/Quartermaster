<script lang="ts">
  // The app window's title bar. The window has no caption of its own
  // (internal/nativewin strips it), so this strip IS the caption: the whole
  // width drags, double-click maximises, and the buttons on the right are the
  // real system verbs.
  //
  // A dedicated strip rather than window buttons tucked into StatusRail,
  // because StatusRail only exists in dashboard mode. Loading, login and the
  // playground shell would otherwise be windows with no way to close them.
  import { push } from "svelte-spa-router";
  import * as native from "../lib/native";
  import { appTitle, connectionState, isDarkMode } from "../stores/theme";
  // The same master art the tray, the taskbar and the favicon carry, derived
  // by packaging/icons/gen.py. assets/logo.png is the older plated wordmark and
  // would put a second, different icon on screen.
  import mark from "../assets/mark.png";
  import { versionInfo } from "../stores/api";
  import WindowControls from "./WindowControls.svelte";
  import TabStrip from "./TabStrip.svelte";
  import { showDashboard } from "../stores/appTabs";
  import { playgroundPort } from "../stores/playgroundAuth";

  // The playground runs on its own port; the strip needs its URL both to open a
  // tab and to hand one to the real browser. Empty when it is not enabled, which
  // is what hides the strip's + button.
  const playgroundURL = $derived(
    $playgroundPort ? `${window.location.protocol}//${window.location.hostname}:${$playgroundPort}/ui/` : "",
  );

  // Only the dashboard has a router to go home to. In playground mode (a
  // different port, decided by /api/mode) there is no "/" route, so the caller
  // says whether the wordmark is a link at all rather than this component
  // guessing and pushing a hash nothing listens to.
  let { home = false }: { home?: boolean } = $props();

  // The wordmark is the ONLY way back to the dashboard -- it is not a tab, it is
  // what the window is, with tabs opened on top of it. So this both drops the
  // active tab and puts the router back on the dashboard's own home page.
  function goHome(): void {
    showDashboard();
    push("/");
  }

  // The whole bar is a drag handle, and a drag is a mousedown the window
  // manager steals -- the click never lands. Stopping propagation here keeps
  // the wordmark a button while everything around it still moves the window.
  function keepClickable(e: MouseEvent): void {
    e.stopPropagation();
  }

  // Same signal the tab title carries, which the native window has no tab to
  // show. Without it a disconnected app window looks identical to a working
  // one until the user notices nothing is updating.
  // The window's two top corners belong to DWM, not to this bar: the rounded
  // mask leaves a stub of window frame there that the page cannot paint over,
  // and in the default frame colour it reads as a small white dot in each
  // corner. Handing the frame this bar's own rendered colour hides both.
  //
  // Read from the element rather than from a token, so it stays right whatever
  // `bg-chrome` resolves to, and re-read on every theme flip. The rAF is not
  // decoration: `data-theme` is set by an effect of its own, and reading the
  // computed style in the same tick can catch the colour the bar is leaving.
  let bar = $state<HTMLElement | null>(null);
  $effect(() => {
    void $isDarkMode;
    const el = bar;
    if (!el) return;
    const id = requestAnimationFrame(() =>
      native.setCaptionColor(getComputedStyle(el).backgroundColor),
    );
    return () => cancelAnimationFrame(id);
  });

  // The version only becomes known once the event stream connects, and the
  // store's placeholder until then is the literal string "unknown" -- which is
  // worse than nothing in a caption bar, so render nothing instead of it.
  const version = $derived($versionInfo.version === "unknown" ? "" : $versionInfo.version);

  // The mark carries the connection state the green dot used to: full colour
  // when connected, pulsing and desaturated while connecting, greyed out and
  // ringed in the error colour when the stream is down. Losing the signal
  // entirely was not an option - the native window has no browser tab to fall
  // back on, and a dead app otherwise looks exactly like a working one.
  const markClass = $derived(
    $connectionState === "connected"
      ? ""
      : $connectionState === "connecting"
        ? "saturate-50 opacity-70 animate-pulse"
        : "grayscale opacity-60 ring-1 ring-error rounded-full",
  );
  const markTitle = $derived(
    $connectionState === "connected"
      ? "Connected"
      : $connectionState === "connecting"
        ? "Connecting..."
        : "Disconnected",
  );
</script>

<!-- Nothing at all in a browser tab. The whole component is behind the feature
     test rather than just the buttons, or a browser would get an empty 2rem
     strip above the app and lose 2rem of viewport to it. -->
{#if native.isNative}
  <header
    bind:this={bar}
    class="titlebar flex h-8 shrink-0 items-center gap-2 bg-chrome pl-4"
    onmousedown={native.dragWindow}
    ondblclick={native.toggleMaximize}
    role="presentation"
  >
    <!-- No border-b: the side rail and the status rail below are the same
         bg-chrome, and a rule across the top drew a line through what should
         read as one continuous frame. Tone alone separates chrome from
         content. -->
    <img src={mark} alt="" title={markTitle} class="h-4 w-4 shrink-0 select-none object-contain transition-all {markClass}" draggable="false" />
    {#if home}
      <!-- cursor-pointer is explicit: Tailwind v4's preflight gives buttons
           `cursor: default`, so a bare <button> here would look inert. -->
      <button
        type="button"
        class="cursor-pointer truncate text-micro font-medium tracking-wide text-txtsecondary transition-colors hover:text-txtmain"
        onmousedown={keepClickable}
        ondblclick={keepClickable}
        onclick={goHome}
        title="Go to the dashboard"
      >
        {$appTitle}
      </button>
    {:else}
      <span class="truncate text-micro font-medium tracking-wide text-txtsecondary">{$appTitle}</span>
    {/if}
    <!-- Dimmer and monospaced so it reads as a build stamp beside the name
         rather than as part of it -- $appTitle is user-editable, and the two
         must not look like one string. shrink-0 keeps the version whole while
         the title above it is the thing that truncates. -->
    {#if version}
      <span
        class="shrink-0 font-mono text-micro tabular-nums text-txtsecondary/60"
        title={`Version: ${version}
Commit: ${$versionInfo.commit?.substring(0, 7) ?? "unknown"}
Build date: ${$versionInfo.build_date ?? "unknown"}`}
      >
        {version}
      </span>
    {/if}

    <!-- Tabs sit between the build stamp and the empty middle: they read as
         opened ON the app rather than as part of its name. -->
    {#if home}
      <TabStrip {playgroundURL} />
    {/if}

    <!-- Grows to fill, so the whole empty middle of the bar is draggable rather
         than just the few pixels around the wordmark. -->
    <div class="flex-1"></div>

    <WindowControls />
  </header>
{/if}

<style>
  /* Dragging a frameless window is a mousedown that never becomes a click: the
     window manager takes the mouse mid-gesture. Without this the pointer sweeps
     a text selection across the bar on the way, and it stays highlighted after
     the drag ends. */
  .titlebar {
    user-select: none;
    -webkit-user-select: none;
  }
</style>
