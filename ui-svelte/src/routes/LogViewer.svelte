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

<div class="flex flex-col h-full w-full gap-2">
  <div class="flex items-center gap-2">
    <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Show</span>
    <div class="seg">
      <button aria-pressed={$viewModeStore === "panels"} onclick={() => viewModeStore.set("panels")}>Both</button>
      <button aria-pressed={$viewModeStore === "proxy"} onclick={() => viewModeStore.set("proxy")}>Proxy</button>
      <button aria-pressed={$viewModeStore === "upstream"} onclick={() => viewModeStore.set("upstream")}>Upstream</button>
    </div>
  </div>

  <div class="flex-1 w-full overflow-hidden">
    {#if $viewModeStore === "panels"}
      <ResizablePanels {direction} storageKey="logviewer-panel-group">
        {#snippet leftPanel()}
          <LogPanel id="proxy" title="Proxy Logs" subtitle={PROXY_HINT} logData={$proxyLogs} />
        {/snippet}
        {#snippet rightPanel()}
          <LogPanel id="upstream" title="Upstream Logs" subtitle={UPSTREAM_HINT} logData={$upstreamLogs} />
        {/snippet}
      </ResizablePanels>
    {:else if $viewModeStore === "proxy"}
      <LogPanel id="proxy" title="Proxy Logs" subtitle={PROXY_HINT} logData={$proxyLogs} />
    {:else}
      <LogPanel id="upstream" title="Upstream Logs" subtitle={UPSTREAM_HINT} logData={$upstreamLogs} />
    {/if}
  </div>
</div>
