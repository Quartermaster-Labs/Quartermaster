<script lang="ts">
  import { onMount } from "svelte";
  import Router from "svelte-spa-router";
  import Sidebar from "./components/Sidebar.svelte";
  import StatusRail from "./components/StatusRail.svelte";
  import ConfirmHost from "./components/ConfirmHost.svelte";
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

  function handleRouteLoaded(event: { detail: { route: string | RegExp } }) {
    const route = event.detail.route;
    currentRoute.set(typeof route === "string" ? route : "/");
  }

  $effect(() => {
    document.documentElement.setAttribute("data-theme", $isDarkMode ? "dark" : "light");
  });

  $effect(() => {
    const icon = $connectionState === "connecting" ? "\u{1F7E1}" : $connectionState === "connected" ? "\u{1F7E2}" : "\u{1F534}";
    document.title = `${icon} ${$appTitle}`;
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

{#if mode === "playground"}
  <PlaygroundApp />
{:else if mode === "dashboard"}
  <div class="flex h-screen bg-background">
    <Sidebar />

    <div class="flex flex-col flex-1 min-w-0">
      <StatusRail />

      <main class="flex-1 overflow-auto pretty-scroll">
        <div class="h-full p-4">
          <Router {routes} on:routeLoaded={handleRouteLoaded} />
        </div>
      </main>
    </div>
  </div>
{:else}
  <div class="h-screen bg-background"></div>
{/if}
