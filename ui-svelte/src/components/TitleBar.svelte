<script lang="ts">
  // The app window's title bar. The window has no caption of its own
  // (internal/nativewin strips it), so this strip IS the caption: the whole
  // width drags, double-click maximises, and the buttons on the right are the
  // real system verbs.
  //
  // A dedicated strip rather than window buttons tucked into StatusRail,
  // because StatusRail only exists in dashboard mode. Loading, login and the
  // playground shell would otherwise be windows with no way to close them.
  import * as native from "../lib/native";
  import { appTitle, connectionState, isDarkMode } from "../stores/theme";
  import WindowControls from "./WindowControls.svelte";

  // Same signal the tab title carries, which the native window has no tab to
  // show. Without it a disconnected app window looks identical to a working
  // one until the user notices nothing is updating.
  // The window's two top corners belong to DWM, not to this bar: the rounded
  // mask leaves a stub of window frame there that the page cannot paint over,
  // and in the default frame colour it reads as a small white dot in each
  // corner. Handing the frame this bar's own rendered colour hides both.
  //
  // Read from the element rather than from a token, so it stays right whatever
  // `bg-surface` resolves to, and re-read on every theme flip. The rAF is not
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

  const dotClass = $derived(
    $connectionState === "connected"
      ? "bg-success"
      : $connectionState === "connecting"
        ? "bg-warning animate-pulse"
        : "bg-error",
  );
</script>

<!-- Nothing at all in a browser tab. The whole component is behind the feature
     test rather than just the buttons, or a browser would get an empty 2rem
     strip above the app and lose 2rem of viewport to it. -->
{#if native.isNative}
  <header
    bind:this={bar}
    class="titlebar flex h-8 shrink-0 items-center gap-2 border-b border-border bg-surface pl-4"
    onmousedown={native.dragWindow}
    ondblclick={native.toggleMaximize}
    role="presentation"
  >
    <span class="inline-block h-2 w-2 shrink-0 rounded-full {dotClass}"></span>
    <span class="truncate text-micro font-medium tracking-wide text-txtsecondary">{$appTitle}</span>

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
