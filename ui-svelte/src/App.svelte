<script lang="ts">
  import { onMount } from "svelte";
  import Router from "svelte-spa-router";
  import Sidebar from "./components/Sidebar.svelte";
  import StatusRail from "./components/StatusRail.svelte";
  import Dashboard from "./routes/Dashboard.svelte";
  import Models from "./routes/Models.svelte";
  import Observe from "./routes/Observe.svelte";
  import Playground from "./routes/Playground.svelte";
  import PlaygroundStub from "./routes/PlaygroundStub.svelte";
  import ApiKeys from "./routes/ApiKeys.svelte";
  import { enableAPIEvents } from "./stores/api";
  import { refreshInferenceKey } from "./lib/inferenceAuth";
  import { startPerfPolling } from "./stores/perf";
  import { initScreenWidth, initSystemThemeListener, isDarkMode, appTitle, connectionState } from "./stores/theme";
  import { currentRoute } from "./stores/route";

  // Playground keeps live state (streaming, attachments), so it is always mounted
  // and toggled via CSS rather than routed. Everything else is a plain route.
  const routes = {
    "/": Dashboard,
    "/models": Models,
    "/models/:category": Models,
    "/observe": Observe,
    "/logs": Observe,
    "/activity": Observe,
    "/performance": Observe,
    "/test": PlaygroundStub,
    "/api-keys": ApiKeys,
    "*": Dashboard,
  };

  const isTest = $derived($currentRoute === "/test");

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

    return () => {
      cleanupScreenWidth();
      cleanupSystemTheme();
      enableAPIEvents(false);
      cleanupPerf();
    };
  });
</script>

<div class="flex h-screen">
  <Sidebar />

  <div class="flex flex-col flex-1 min-w-0">
    <StatusRail />

    <main class="flex-1 overflow-auto">
      <div class="h-full p-4" class:hidden={!isTest}>
        <Playground />
      </div>
      <div class="h-full p-4" class:hidden={isTest}>
        <Router {routes} on:routeLoaded={handleRouteLoaded} />
      </div>
    </main>
  </div>
</div>
