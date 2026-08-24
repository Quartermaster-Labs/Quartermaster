<script lang="ts">
  import { onMount } from "svelte";
  import Router from "svelte-spa-router";
  import Sidebar from "./components/Sidebar.svelte";
  import StatusRail from "./components/StatusRail.svelte";
  import ConfirmHost from "./components/ConfirmHost.svelte";
  import TitleBar from "./components/TitleBar.svelte";
  import Dashboard from "./routes/Dashboard.svelte";
  import Models from "./routes/Models.svelte";
  import Browse from "./routes/Browse.svelte";
  import Observe from "./routes/Observe.svelte";
  import PlaygroundStub from "./routes/PlaygroundStub.svelte";
  import PlaygroundApp from "./routes/PlaygroundApp.svelte";
  import ApiKeys from "./routes/ApiKeys.svelte";
  import { enableAPIEvents } from "./stores/api";
  import { refreshInferenceKey } from "./lib/inferenceAuth";
  import { startPerfPolling } from "./stores/perf";
  import { initScreenWidth, initSystemThemeListener, isDarkMode, appTitle, connectionState } from "./stores/theme";
  import { currentRoute } from "./stores/route";
  import { playgroundPort } from "./stores/playgroundAuth";
  import { get } from "svelte/store";
  import { appTabs, activeTabId, setTabState } from "./stores/appTabs";
  import { openExternal } from "./lib/native";

  // The playground is now a separate app served on its own port. /api/mode tells
  // us which one to render: the operator dashboard or the standalone playground.
  let mode = $state<"loading" | "dashboard" | "playground">("loading");

  // Playground moved to its own port; the dashboard /test route is just a stub
  // that points users to it (the sidebar links out directly).
  const routes = {
    "/": Dashboard,
    "/models": Models,
    "/models/:category": Models,
    "/browse": Browse,
    "/observe": Observe,
    "/logs": Observe,
    "/activity": Observe,
    "/performance": Observe,
    "/test": PlaygroundStub,
    "/api-keys": ApiKeys,
    "*": Dashboard,
  };

  // Routes whose content owns the whole page surface (no outer gutter).
  const FULL_BLEED = ["/", "/models", "/models/:category", "/browse", "/api-keys", "/observe", "/logs", "/activity", "/performance"];
  const fullBleed = $derived(FULL_BLEED.includes($currentRoute));

  function handleRouteLoaded(event: { detail: { route: string | RegExp } }) {
    const route = event.detail.route;
    currentRoute.set(typeof route === "string" ? route : "/");
  }

  $effect(() => {
    document.documentElement.setAttribute("data-theme", $isDarkMode ? "dark" : "light");
  });

  // Dashboard only: the playground owns its own tab title (see PlaygroundApp).
  // This effect re-runs on every connection change, so without the mode guard it
  // would overwrite the playground's title a moment after mount, as soon as the
  // event stream connected. While mode is still "loading" nobody writes, so a
  // playground tab never flashes the dashboard title.
  $effect(() => {
    if (mode !== "dashboard") return;
    const icon = $connectionState === "connecting" ? "\u{1F7E1}" : $connectionState === "connected" ? "\u{1F7E2}" : "\u{1F534}";
    document.title = `${icon} ${$appTitle}`;
  });

  // The origin every tab frame lives on. Used to authenticate their messages:
  // a postMessage carries whatever the sender chose, and one of the two things
  // they can ask for -- open this URL in the system browser -- is a shell
  // execution path. (The Go side validates the scheme too; this is the first of
  // the two gates, not the only one.)
  const playgroundOrigin = $derived(
    $playgroundPort ? `${window.location.protocol}//${window.location.hostname}:${$playgroundPort}` : "",
  );

  // The tab frames' half of this wire is lib/embed.ts. They report a label and a
  // busy flag because cross-origin means the strip can read neither from them.
  $effect(() => {
    const origin = playgroundOrigin;
    if (!origin) return;
    const onMessage = (e: MessageEvent) => {
      if (e.origin !== origin) return;
      const d = e.data as { type?: string; tab?: string; label?: string; busy?: boolean; url?: string } | null;
      if (!d || typeof d !== "object" || typeof d.tab !== "string") return;
      // A message naming a tab we do not have is stale (a frame unloading after
      // its close) or forged; either way there is nothing to apply it to.
      // get(), not $appTabs: reading the store here would make the effect depend
      // on it, so every label and busy flip would tear the listener down and
      // re-add it -- and a message landing in that gap would be lost.
      if (!get(appTabs).some((t) => t.id === d.tab)) return;
      if (d.type === "qm-tab-state") setTabState(d.tab, String(d.label ?? ""), !!d.busy);
      else if (d.type === "qm-tab-external" && typeof d.url === "string") openExternal(d.url);
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  });

  onMount(() => {
    const cleanupScreenWidth = initScreenWidth();
    const cleanupSystemTheme = initSystemThemeListener();
    enableAPIEvents(true);
    refreshInferenceKey(); // auto-attach a key to Playground inference when keys are on
    const cleanupPerf = startPerfPolling();

    // Decide which app this port serves.
    (async () => {
      try {
        const r = await fetch("/api/mode");
        const j = await r.json();
        playgroundPort.set(j.playgroundPort ?? "");
        mode = j.playground ? "playground" : "dashboard";
      } catch {
        mode = "dashboard";
      }
    })();

    return () => {
      cleanupScreenWidth();
      cleanupSystemTheme();
      enableAPIEvents(false);
      cleanupPerf();
    };
  });
</script>

<!-- One dialog host per app root; askConfirm() from anywhere renders here. -->
<ConfirmHost />

<!-- The title bar sits OUTSIDE the mode switch on purpose: loading, the
     playground login and the playground shell are all states the window can be
     closed from, and only a bar that outlives the switch gives them a close
     button. It renders nothing in a browser tab.

     Everything below it still measures itself in `h-screen`, which index.css
     shortens by the bar's height under [data-native] -- one rule instead of
     threading a height through every full-height root in the app. -->
<TitleBar home={mode === "dashboard"} />

{#if mode === "playground"}
  <PlaygroundApp />
{:else if mode === "dashboard"}
  <!-- Hidden rather than unmounted while a tab is in front: the dashboard holds
       router state, the open log stream and the metrics history, and rebuilding
       all of it on every tab switch would make the strip feel like navigation
       instead of like tabs. -->
  <div class="flex h-screen bg-background" style:display={$activeTabId ? "none" : null}>
    <!-- The slot reserves only the COLLAPSED width; the rail itself is absolute
         inside it, so expanding on hover draws over the page like a curtain
         instead of reflowing every layout to the right of it. -->
    <!-- No right border, and none anywhere along this seam: the chrome, the
         status rail and the page separate themselves by tone alone. A rule
         here would also have to stop dead at the rail's rounded corner and
         end in mid-air under the title bar. -->
    <div class="relative w-14 shrink-0 z-40">
      <Sidebar />
    </div>

    <div class="flex flex-col flex-1 min-w-0">
      <!-- The rail keeps its own tone, a clear step above the chrome, so its
           top-left corner can round off against the side rail rather than
           butting into it. The notch that carves out has to show
           the SAME tone as the side rail and the title bar around it - without
           this wrapper it would fall through to bg-background and read as a
           chipped corner rather than a curve.
           And when the FIRST rail row is the active one, that row's highlight
           is what sits behind the notch: paint it under there too, or the curve
           reads as a chip taken out of the highlight. The rail rows are h-10,
           the same height as the status rail, so only the first one can ever be
           behind it. -->
      <div class="relative shrink-0 bg-chrome">
        {#if $currentRoute === "/"}
          <span class="absolute inset-y-0 left-0 w-4 bg-secondary/60"></span>
        {/if}
        <!-- Positioned, so it paints over the underlay above and leaves it
             visible only where the corner is cut away. -->
        <div class="relative">
          <StatusRail />
        </div>
      </div>

      <main class="flex-1 overflow-auto pretty-scroll">
        <!-- Table/browser pages are full-bleed: their own panels supply the
             padding, so the content reaches the window edges instead of
             floating in a framed box. -->
        <div class="h-full {fullBleed ? '' : 'p-4'}">
          <Router {routes} on:routeLoaded={handleRouteLoaded} />
        </div>
      </main>
    </div>
  </div>
{:else}
  <div class="h-screen bg-background"></div>
{/if}

<!-- Tab frames. Kept MOUNTED whichever one is in front, which is the whole
     reason a tab is a frame and not a route: a turn in flight is a streaming
     fetch owned by that document, so unmounting the background tab -- or
     navigating the single webview away from it -- would abort the generation.
     Outside the mode switch so a frame survives anything the shell does.

     `allow` is not optional. A cross-origin frame is denied microphone and
     clipboard by default, which would break recording in the speech and audio
     tabs and every copy button in chat, silently and only inside the app. -->
{#each $appTabs as tab (tab.id)}
  <iframe
    src={tab.url}
    title={tab.label}
    class="block h-screen w-full border-0"
    allow="microphone; clipboard-read; clipboard-write; autoplay"
    style:display={$activeTabId === tab.id ? null : "none"}
  ></iframe>
{/each}
