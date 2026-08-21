<script lang="ts">
  import { proxyLogs, upstreamLogs } from "../stores/api";
  import { screenWidth } from "../stores/theme";
  import { persistentStore } from "../stores/persistent";
  import LogPanel from "../components/LogPanel.svelte";
  import ResizablePanels from "../components/ResizablePanels.svelte";

  type ViewMode = "proxy" | "upstream" | "panels";

  const viewModeStore = persistentStore<ViewMode>("logviewer-view-mode", "panels");

  let direction = $derived<"horizontal" | "vertical">(
    $screenWidth === "xs" || $screenWidth === "sm" ? "vertical" : "horizontal",
  );

  // What each stream actually carries — proxy = quartermaster's own events,
  // upstream = the raw stdout/stderr of whatever backend it spawned.
  const PROXY_HINT =
    "Quartermaster's own log: startup/shutdown & config reload, HTTP access lines (method, path, status, size, duration), model scheduling (spawn command, health-check, TTL unload, exit status), preload and config-regeneration progress, and routing/API-key rejections. Failed requests log as WARN/ERROR; health, metrics and dashboard polling are hidden unless logLevel is debug.";
  const UPSTREAM_HINT =
    "Raw stdout + stderr of the spawned backends (llama-server / sd-server / tts-server / upscaler), interleaved across every running model - load progress, per-request timings, and backend errors.";
</script>

<!-- The stream picker rides in the panel header where the title used to be:
     it names the stream you are looking at, so a separate labelled row above the
     panels was a whole line of chrome saying the same thing twice. -->
{#snippet streamPicker()}
  <div class="seg shrink-0">
    <button aria-pressed={$viewModeStore === "panels"} onclick={() => viewModeStore.set("panels")}>Both</button>
    <button aria-pressed={$viewModeStore === "proxy"} onclick={() => viewModeStore.set("proxy")}>Proxy</button>
    <button aria-pressed={$viewModeStore === "upstream"} onclick={() => viewModeStore.set("upstream")}>Upstream</button>
  </div>
{/snippet}

<div class="flex flex-col h-full w-full">
  <div class="flex-1 w-full overflow-hidden">
    {#if $viewModeStore === "panels"}
      <ResizablePanels {direction} storageKey="logviewer-panel-group">
        {#snippet leftPanel()}
          <LogPanel id="proxy" title="Proxy Logs" subtitle={PROXY_HINT} logData={$proxyLogs} header={streamPicker} />
        {/snippet}
        {#snippet rightPanel()}
          <!-- Both panels are on screen, so only the left one carries the
               picker; the right keeps its name so the two stay tellable apart. -->
          <LogPanel id="upstream" title="Upstream Logs" subtitle={UPSTREAM_HINT} logData={$upstreamLogs} />
        {/snippet}
      </ResizablePanels>
    {:else if $viewModeStore === "proxy"}
      <LogPanel id="proxy" title="Proxy Logs" subtitle={PROXY_HINT} logData={$proxyLogs} header={streamPicker} />
    {:else}
      <LogPanel id="upstream" title="Upstream Logs" subtitle={UPSTREAM_HINT} logData={$upstreamLogs} header={streamPicker} />
    {/if}
  </div>
</div>
