import type { ModelConfig } from "../stores/api";

// Pure launch-command <-> form-field helpers for ModelConfigModal: parsing a
// rendered command back into form state, the flag sets the form owns, spec
// chain manipulation, and display formatting. Nothing here touches component
// state, so it is unit-testable and keeps the modal to wiring + markup.

// Whether a rendered command leaves mmap on. llama-server folded --mmap/--no-mmap
// /--mlock/-dio into one --load-mode enum, so read that first and only fall back
// to the deprecated flag for commands saved before the switch.
export function noNoMmap(cmd: string): boolean {
  const lm = cmd.match(/(?:^|\s)(?:--load-mode|-lm)\s+(\S+)/);
  if (lm) return loadModeMmap(lm[1]);
  return !/(?:^|\s)--no-mmap(?:\s|$)/.test(cmd);
}

// --load-mode values that keep the weights mmap'd. "dio" bypasses the page cache
// entirely and "none" reads into anonymous memory; everything else maps.
export function loadModeMmap(mode: string): boolean {
  return mode !== "none" && mode !== "dio" && mode !== "mlock";
}

// sd.cpp sampling methods (mirrors the playground's SAMPLER_OPTIONS).
export const IMG_SAMPLERS = ["", "euler_a", "euler", "heun", "dpm2", "dpmpp2s_a", "dpmpp2m", "dpmpp2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd"];

// Pull a `--chat-template-file <path>` pair out of a free-form extraArgs string,
// returning the remaining args plus the path (quotes stripped, "" when absent).
// extraArgs is the only surface the qm-tools chat model can write a template
// through, and the form owns that flag elsewhere — so it has to be hoisted.
export function hoistChatTemplate(extra: string): { extra: string; path: string } {
  const m = extra.match(/(^|\s)--chat-template-file\s+("[^"]*"|\S+)/);
  if (!m) return { extra, path: "" };
  const path = m[2].replace(/^"|"$/g, "");
  return { extra: (extra.slice(0, m.index) + " " + extra.slice(m.index! + m[0].length)).trim(), path };
}

// Pull a `-cms <n>` / `--checkpoint-min-step <n>` pair out of a free-form
// extraArgs string, returning the remaining args plus the value ("" when
// absent). Installs written before the box parsed -cms captured it into
// extraArgs, where the emitter appends it after its own computed copy: harmless
// to llama-server (last wins) but it grows by one on every box round trip, and
// it silently overrides the sizer's spacing. Hoisted back into the field on
// load, exactly like a template smuggled in through extraArgs.
export function hoistCms(extra: string): { extra: string; step: number | "" } {
  const m = extra.match(/(^|\s)(?:-cms|--checkpoint-min-step)\s+(\d+)/);
  if (!m) return { extra, step: "" };
  return {
    extra: (extra.slice(0, m.index) + " " + extra.slice(m.index! + m[0].length)).replace(/\s+/g, " ").trim(),
    step: Number(m[2]),
  };
}

// Owned by other controls / autogen - swallowed when parsing the image box so
// they never bleed into extraArgs: -m modelPath, -l/--listen-port the socket,
// --max-vram the slider, --vae-on-cpu the offload pair (--backend vae=cpu is a
// separate toggle, kept).
export const IMG_IGNORE_VALUE = new Set(["-m", "--diffusion-model", "-l", "--listen-port", "--max-vram"]);
export const IMG_IGNORE_BOOL = new Set(["--vae-on-cpu"]);

// Parse an sd-server launch command into the image form fields + extraArgs
// passthrough (mirror of parseCmdFields for the diffusion emit). Default-on
// toggles (te/tiling/diffusion-fa) flip to "off" when their flag is absent.
export interface ParsedImg {
  vaePath: string; clipLPath: string; clipGPath: string; t5Path: string; textEncoderPath: string;
  offloadToCpu: string; teOnCpu: string; vaeOnCpu: string; vaeTiling: string; diffusionFa: string;
  defaultSteps: number | ""; defaultCfg: number | ""; defaultSampler: string;
  defaultWidth: number | ""; defaultHeight: number | ""; threads: number | ""; extraArgs: string;
}
export function parseImageCmdFields(cmd: string): ParsedImg {
  const toks = cmd.trim().split(/\s+/);
  let i = 0;
  while (i < toks.length && !toks[i].startsWith("-")) i++; // skip the exe
  const val = (): string => (i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "");
  let vae = "", clipL = "", clipG = "", t5 = "", llm = "", steps = "", cfg = "", sampler = "", w = "", h = "", t = "";
  let sawFa = false, sawTiling = false, sawTeCpu = false, sawVaeCpu = false, sawOffload = false;
  const extras: string[] = [];
  for (; i < toks.length; i++) {
    const tk = toks[i];
    switch (tk) {
      case "--vae": vae = val(); break;
      case "--clip_l": clipL = val(); break;
      case "--clip_g": clipG = val(); break;
      case "--t5xxl": t5 = val(); break;
      case "--llm": llm = val(); break;
      case "--diffusion-fa": sawFa = true; break;
      case "--vae-tiling": sawTiling = true; break;
      case "--offload-to-cpu": sawOffload = true; break;
      case "-t": t = val(); break;
      case "--steps": steps = val(); break;
      case "--cfg-scale": cfg = val(); break;
      case "--sampling-method": sampler = val(); break;
      case "--width": w = val(); break;
      case "--height": h = val(); break;
      case "--backend": {
        // te=cpu / vae=cpu drive the placement toggles; any other value (e.g.
        // the compute backend "cpu"/"cuda") is passthrough → extraArgs.
        const v = val();
        const parts = v.split(",").map((x) => x.trim());
        const known = parts.filter((x) => x === "te=cpu" || x === "vae=cpu");
        if (known.includes("te=cpu")) sawTeCpu = true;
        if (known.includes("vae=cpu")) sawVaeCpu = true;
        const rest = parts.filter((x) => x !== "te=cpu" && x !== "vae=cpu" && x !== "");
        if (rest.length) extras.push("--backend", rest.join(","));
        break;
      }
      default:
        if (IMG_IGNORE_VALUE.has(tk)) val();
        else if (IMG_IGNORE_BOOL.has(tk)) break;
        else {
          extras.push(tk);
          const v = i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "";
          if (v) extras.push(v);
        }
    }
  }
  const num = (s: string): number | "" => (s !== "" ? Number(s) : "");
  return {
    vaePath: vae, clipLPath: clipL, clipGPath: clipG, t5Path: t5, textEncoderPath: llm,
    offloadToCpu: sawOffload ? "on" : "", // absent => auto (can't distinguish off)
    teOnCpu: sawTeCpu ? "" : "off",
    vaeOnCpu: sawVaeCpu ? "on" : "",
    vaeTiling: sawTiling ? "" : "off",
    diffusionFa: sawFa ? "" : "off",
    defaultSteps: num(steps), defaultCfg: num(cfg), defaultSampler: sampler,
    defaultWidth: num(w), defaultHeight: num(h), threads: num(t), extraArgs: extras.join(" "),
  };
}

// Matches generate.go's effectiveSpec: MTP (baked head/sidecar) defaults to
// draft-mtp CHAINED with ngram-mod (benched better than mtp alone); everything
// else to ngram-mod. A DFlash sidecar is NEVER auto-picked (opt-in only), so it
// is not a default here either. "+"-joined; activeSpecs splits it.
export function genDefaultSpec(c: ModelConfig | null): string {
  if (c?.isMTP) return "draft-mtp+ngram-mod";
  return "ngram-mod";
}

// The KV cache type the generator emits for THIS model, read off its rendered
// command. Used to decide whether a value parsed out of the command box is a
// real edit or just the default echoed back. Was a hardcoded "q8_0"; autogen now
// picks per model (f16, stepping down to q8_0 only when f16 can't reach the
// minimum context in the VRAM budget), so a constant would mis-flag every model.
export function genDefaultKv(c: ModelConfig | null): string {
  const m = /(?:^|\s)-ctk\s+(\S+)/.exec(c?.cmd ?? "");
  return m ? m[1] : "f16";
}

// The numeric value autogen emits for `flag` on THIS model, read off its
// rendered command — the sampler-default counterpart to genDefaultKv. Lets a box
// edit tell "the user pinned this" apart from "the generator's own default was
// echoed back", so an arch-derived baseline is not silently frozen into an
// explicit per-model pin. "" when the flag is absent or non-numeric.
export function genDefaultNum(c: ModelConfig | null, flag: string): number | "" {
  return cmdNum(c?.cmd ?? "", flag);
}

// The numeric value ANY rendered command carries for `flag` ("" when the flag is
// absent or valueless). Same read as genDefaultNum against arbitrary text: a box
// edit is judged against the command the render effect last produced, which is
// the only way to tell "the user changed this" from "the user left it alone".
export function cmdNum(cmd: string, flag: string): number | "" {
  const m = new RegExp(`(?:^|\\s)${flag}\\s+(\\S+)`).exec(cmd);
  return m && m[1] !== "" && !Number.isNaN(Number(m[1])) ? Number(m[1]) : "";
}

// Does spec list s contain backend b?
export function specHas(s: string | undefined, b: string): boolean {
  return (s ?? "").split("+").includes(b);
}
// draft-mtp and draft-dflash are mutually exclusive: both drive the single -md
// draft-model slot, so chaining them emits two --spec-type flags over one draft
// file and half the pair launches against a drafter of the wrong arch.
export const SPEC_DRAFT_BACKENDS = ["draft-mtp", "draft-dflash"];

// Toggle backend b in the "+"-joined list s. "none" is exclusive (clears the
// rest); checking a real backend clears "none", and checking a draft backend
// clears the other one.
export function specToggle(s: string | undefined, b: string, on: boolean): string {
  if (b === "none") return on ? "none" : "";
  let parts = (s ?? "").split("+").filter(Boolean).filter((x) => x !== "none" && x !== b);
  if (on) {
    if (SPEC_DRAFT_BACKENDS.includes(b)) parts = parts.filter((x) => !SPEC_DRAFT_BACKENDS.includes(x));
    parts.push(b);
  }
  // Unchecking the last backend means "off" - store explicit "none" rather
  // than "" (empty would fall back to the MTP/ngram auto-default at emit).
  if (!on && parts.length === 0) return "none";
  return parts.join("+");
}
// Resolved active backends (""/unset => the generator default) so the form can
// show only the sub-knobs the chosen backends actually emit.

export function fmtCtx(n: number): string {
  return n % 1024 === 0 ? `${n / 1024}k` : `${n}`;
}

// GPU layers as value/max (max = transformer blocks). -ngl 99 is the "all
// layers" sentinel, so clamp to the block count; fall back to the raw value
// when the block count is unknown.
export function nglDisplay(ngl: number, blocks: number): string {
  return blocks > 0 ? `${Math.min(ngl, blocks)}/${blocks}` : String(ngl);
}

// Effective context the autogen sizer baked into the launch command (-c N).
export function parseCtx(cmd: string): number {
  const m = cmd.match(/(?:^|\s)-c\s+(\d+)/);
  return m ? Number(m[1]) : 0;
}
