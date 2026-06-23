<script lang="ts">
  import { selectedTabStore, type PlaygroundTab } from "../stores/playground";
  import { MessageSquare, Image, Volume2, Mic, ListOrdered, Zap } from "lucide-svelte";
  import ChatInterface from "../components/playground/ChatInterface.svelte";
  import ImageInterface from "../components/playground/ImageInterface.svelte";
  import AudioInterface from "../components/playground/AudioInterface.svelte";
  import SpeechInterface from "../components/playground/SpeechInterface.svelte";
  import RerankInterface from "../components/playground/RerankInterface.svelte";
  import ConcurrencyInterface from "../components/playground/ConcurrencyInterface.svelte";

  type Tab = PlaygroundTab;

  const tabs: { id: Tab; label: string; icon: typeof MessageSquare }[] = [
    { id: "chat", label: "Chat", icon: MessageSquare },
    { id: "images", label: "Images", icon: Image },
    { id: "speech", label: "Speech", icon: Volume2 },
    { id: "audio", label: "Transcription", icon: Mic },
    { id: "rerank", label: "Rerank", icon: ListOrdered },
    { id: "concurrency", label: "Load Test", icon: Zap },
  ];
</script>

<div class="h-full flex flex-col gap-3">
  <!-- Tab navigation banner -->
  <div class="card !py-2 shrink-0">
    <div class="flex items-center gap-1 overflow-x-auto">
      {#each tabs as tab (tab.id)}
        {@const active = $selectedTabStore === tab.id}
        <button
          class="flex items-center gap-2 px-3 py-1.5 rounded font-mono text-sm whitespace-nowrap transition-colors {active
            ? 'bg-primary/10 text-primary'
            : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => selectedTabStore.set(tab.id)}
        >
          <tab.icon size={15} strokeWidth={active ? 2.4 : 1.8} />
          {tab.label}
        </button>
      {/each}
    </div>
  </div>

  <!-- Tab content -->
  <div class="flex-1 overflow-hidden relative min-h-0">
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "chat"}>
      <ChatInterface />
    </div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "images"}>
      <ImageInterface />
    </div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "speech"}>
      <SpeechInterface />
    </div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "audio"}>
      <AudioInterface />
    </div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "rerank"}>
      <RerankInterface />
    </div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "concurrency"}>
      <ConcurrencyInterface />
    </div>
  </div>
</div>

<style>
  .tab-hidden {
    display: none;
  }
</style>
