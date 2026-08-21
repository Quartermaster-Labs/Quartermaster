<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { get } from "svelte/store";
  import { slide } from "svelte/transition";
  import { push } from "svelte-spa-router";
  import { models, unloadSingleModel, getModelConfig, type ModelConfig } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { prettifyModelName, modelCategory, type ModelCategory } from "../lib/modelUtils";
  import { Settings, Square, Image, MessageCircle, HelpCircle } from "lucide-svelte";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import InferenceFeedback from "./InferenceFeedback.svelte";

  // category "all" (dashboard) shows every live model; a specific category
  // (Models page) scopes to that tab. Staging (pre-load editing) is driven by the
  // page's card grid — this panel just renders whatever is live.
  let { category = "all" as ModelCategory | "all" }: { category?: ModelCategory | "all" } = $props();

  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "name");

  // Per-model config editor (cogwheel) state.
  let configModelId = $state<string | null>(null);
  let configOpenFor = $state("");
  let configOpen = $state(false);
  function openConfig(family: string, openFor = ""): void {
    configModelId = family;
    configOpenFor = openFor || family;
    configOpen = true;
  }
  function closeConfig(): void {
    configOpen = false;
    configs = {}; // drop the id-keyed cache so edited params refetch
  }

  function isLive(m: Model): boolean {
    return m.state === "ready" || m.state === "starting" || m.state === "stopping";
  }

  // The live members shown up top (each gets a mini-settings block + feedback).
  const liveMembers = $derived(
    $models.filter(
      (m) => !m.peerID && isLive(m) && (category === "all" || modelCategory(m) === category),
    ),
  );

  // --- Active model launch params (fetched per member) ---
  let configs = $state<Record<string, ModelConfig>>({});
  $effect(() => {
    for (const m of liveMembers) {
      if (configs[m.id]) continue;
      getModelConfig(m.id)
        .then((c) => (configs = { ...configs, [m.id]: c }))
        .catch(() => {});
    }
  });

  // Every category except embed/segment has a playground tab. Embedders and
  // rerankers have no UI (API only); SAM segmenters are driven from the Images
  // playground select tool.
  function playable(m: Model): boolean {
    const cat = modelCategory(m);
    return cat !== "embed" && cat !== "segment" && !m.capabilities?.reranker;
  }
  function playgroundTab(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "images";
    if (c?.audio_speech) return "speech";
    if (c?.audio_transcriptions) return "audio";
    return "chat";
  }
  function playLabel(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "Generate";
    if (c?.audio_speech) return "Speak";
    if (c?.audio_transcriptions) return "Transcribe";
    return "Chat";
  }
  // Playground is a separate app on its own port — open it with model + tab.
  function chatWith(m: Model): void {
    const tab = playgroundTab(m);
    const port = get(playgroundPort);
    if (!port) {
      push("/test");
      return;
    }
    const u = `${window.location.protocol}//${window.location.hostname}:${port}/ui/?model=${encodeURIComponent(m.id)}&tab=${tab}`;
    window.open(u, "_blank", "noopener");
  }

  // Pull effective launch flags straight off the run command.
  const FLAG_MAP: Record<string, string> = {
    "-c": "ctx",
    "--ctx-size": "ctx",
    "-ngl": "ngl",
    "--n-gpu-layers": "ngl",
    "--n-cpu-moe": "cpuMoe",
    "-ctk": "kvK",
    "--cache-type-k": "kvK",
    "-ctv": "kvV",
    "--cache-type-v": "kvV",
    "--spec-type": "spec",
    "--reasoning-format": "reasoningFmt",
    "--reasoning": "reasoning",
    "--steps": "steps",
    "--cfg-scale": "cfg",
    "--sampling-method": "sampler",
    "--width": "width",
    "--height": "height",
  };
  function parseFlags(cmd: string): Record<string, string> {
    const out: Record<string, string> = {};
    const toks = cmd.split(/\s+/).filter(Boolean);
    for (let i = 0; i < toks.length; i++) {
      const key = FLAG_MAP[toks[i]];
      if (key && i + 1 < toks.length) out[key] = toks[i + 1];
    }
    return out;
  }
  function specList(cmd: string): string {
    const toks = cmd.split(/\s+/).filter(Boolean);
    const specs: string[] = [];
    for (let i = 0; i < toks.length - 1; i++) {
      if (toks[i] === "--spec-type") specs.push(toks[i + 1]);
    }
    return specs.length ? specs.join(" + ") : "none";
  }
  function reasonDefault(f: Record<string, string>): string {
    return f.reasoning === "off" ? "off" : f.reasoningFmt || "auto";
  }
  function nglDisplay(ngl: string | undefined, blocks: number): string {
    if (ngl == null) return "-";
    const n = Number(ngl);
    if (!blocks || !Number.isFinite(n)) return ngl;
    return `${Math.min(n, blocks)}/${blocks}`;
  }

  function display(m: Model): string {
    return $showIdorNameStore === "id" ? m.id : prettifyModelName(m.name || m.id);
  }
  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }
</script>

{#snippet hint(text: string)}
  <!-- lucide glyph rather than a hand-drawn "?" bubble: the old one needed a
       sub-11px font to fit its circle, which no longer exists in the scale. -->
  <span class="inline-flex shrink-0 text-txtsecondary cursor-help hover:text-txtmain" use:tip={text} aria-label={text}>
    <HelpCircle size={12} />
  </span>
{/snippet}
{#snippet roField(label: string, value: string, help: string)}
  <div>
    <div class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">{label} {@render hint(help)}</div>
    <div class="font-mono text-txtmain tabular-nums pt-1.5 break-all">{value}</div>
  </div>
{/snippet}

{#if liveMembers.length > 0}
  <div class="shrink-0 h-72 min-h-[14rem]" transition:slide={{ duration: 250 }}>
    <!-- One card, two halves: staging params and live feedback are the same
         "what is running right now" surface, so a divider separates them rather
         than a gap between two floating cards. -->
    <div class="card h-full p-0 grid grid-cols-1 lg:grid-cols-2 divide-y lg:divide-y-0 lg:divide-x divide-card-border-inner">
      <!-- Active model settings -->
      <div class="h-full min-h-0 overflow-auto pretty-scroll p-4">
        {#each liveMembers as m (m.id)}
          {@const cfg = configs[m.id]}
          <!-- A running model keeps serving under the args it SPAWNED with, so show
               m.runningCmd (actual launched argv) rather than the pending config. -->
          {@const effCmd = m.runningCmd || cfg?.cmd || ""}
          {@const flags = effCmd ? parseFlags(effCmd) : {}}
          <div class="mb-3 last:mb-0">
            <div class="flex items-center gap-2">
              <span class="inline-block w-2.5 h-2.5 rounded-full {dotClass(m.state)}"></span>
              <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">{m.state}</span>
              <div class="ml-auto flex items-center gap-1.5">
                <button
                  class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
                  onclick={() => openConfig(m.id)}
                  aria-label="Edit parameters"
                  use:tip={"Edit parameters / variants"}
                >
                  <Settings size={16} />
                </button>
                {#if playable(m)}
                  <button
                    class="btn btn--sm py-1.5 inline-flex items-center gap-1.5 uppercase tracking-wide hover:border-primary hover:text-primary"
                    onclick={() => chatWith(m)}
                    disabled={m.state !== "ready"}
                    use:tip={`Open this model in the ${playgroundTab(m)} playground`}
                  >
                    {#if m.capabilities?.image_generation}
                      <Image size={13} class="shrink-0" />
                      Generate
                    {:else}
                      <MessageCircle size={13} class="shrink-0" />
                      {playLabel(m)}
                    {/if}
                  </button>
                {/if}
                <button
                  class="btn btn--sm py-1.5 inline-flex items-center gap-1.5 uppercase tracking-wide hover:border-error hover:text-error"
                  onclick={() => unloadSingleModel(m.id)}
                  disabled={m.state !== "ready"}
                >
                  <Square size={12} class="shrink-0" fill="currentColor" />
                  Unload
                </button>
              </div>
            </div>
            <!-- The card's title. Was mono + uppercase + tracking-widest in a
                 secondary colour, i.e. the three least readable settings at once
                 on the one string that identifies what's running. -->
            <div class="mt-1.5 font-mono text-base font-medium text-txtmain break-words" use:tip={m.id}>{display(m)}</div>

            {#if cfg?.isImage}
              <div class="mt-2 grid grid-cols-3 gap-x-3 gap-y-2 text-label">
                {@render roField("Steps", flags.steps ?? "-", "Sampling steps per image (--steps). More = slower, usually higher quality. Distilled/Turbo models want few (4–8).")}
                {@render roField("CFG", flags.cfg ?? "-", "Classifier-free guidance scale (--cfg-scale). Turbo/distilled models REQUIRE 1.0 - higher blurs. Standard models use ~7.")}
                {@render roField("Sampler", flags.sampler ?? "-", "Sampling method (--sampling-method). euler / euler_a are safe defaults; lcm pairs with low-step distilled models.")}
                {@render roField("Width", flags.width ?? "-", "Default image width in px (--width). Per-request width still overrides this.")}
                {@render roField("Height", flags.height ?? "-", "Default image height in px (--height). Per-request height still overrides this.")}
                {@render roField("CPU offload", effCmd.includes("--offload-to-cpu") ? "on" : "off", "Page diffusion weights to RAM (--offload-to-cpu): saves VRAM, slower per step.")}
              </div>
            {:else if cfg}
              <div class="mt-2 grid grid-cols-3 gap-x-3 gap-y-2 text-label">
                {@render roField("Ctx", flags.ctx ?? "-", "Context window (tokens) this model loaded with (-c), as sized by the autogen sizer to fit free VRAM.")}
                {@render roField("GPU layers", nglDisplay(flags.ngl, cfg.blockCount ?? 0), "Layers resident on the GPU (-ngl), as chosen by the sizer for the current plan.")}
                {@render roField("CPU MoE", flags.cpuMoe ?? "-", "Expert layers offloaded to the CPU (--n-cpu-moe) for MoE models.")}
                {@render roField("KV K", flags.kvK ?? "-", "Quantization of the attention key cache (-ctk). Lower bits = less VRAM, slightly less accuracy.")}
                {@render roField("KV V", flags.kvV ?? "-", "Quantization of the attention value cache (-ctv). Lower bits = less VRAM.")}
                {@render roField("Spec", specList(effCmd), "Speculative decoding chain (--spec-type), one entry per backend. none = disabled.")}
                {@render roField("Reasoning", reasonDefault(flags), "How the model's chain-of-thought is parsed (--reasoning-format). auto = llama.cpp detects it; off = reasoning disabled.")}
              </div>
            {/if}

            {#if cfg}
              <details class="mt-2">
                <summary class="text-micro text-txtsecondary cursor-pointer hover:text-txtmain">Launch command</summary>
                <pre class="well mt-1 whitespace-pre-wrap break-all font-mono text-micro text-txtsecondary">{effCmd}</pre>
              </details>
            {/if}
          </div>
        {/each}
      </div>

      <!-- Live inference feedback -->
      <div class="h-full min-h-0 p-4">
        <InferenceFeedback models={liveMembers} />
      </div>
    </div>
  </div>
{/if}

<ModelConfigModal modelId={configModelId} openForId={configOpenFor} open={configOpen} onclose={closeConfig} />
