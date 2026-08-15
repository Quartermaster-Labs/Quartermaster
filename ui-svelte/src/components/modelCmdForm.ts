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

// Flags autogen always emits and OWNS (computed or fixed): ignored when parsing
// the box so editing them never flips a form "auto" toggle or pins a value.
// Value-flags owned by other controls (sliders / toggles / sizer), swallowed
// when parsing so they never bleed into extraArgs and double-emit:
//   -c/-ngl/--n-cpu-moe/-b  sizer; --ctx-checkpoints  its own field;
//   --chat-template-kwargs  legacy preserve-thinking form, still swallowed so an
//   older saved command does not bleed into extraArgs; -md  draft path;
//   --slot-save-path  the slotCacheOn toggle.
// --chat-template-file is NOT here: it has its own case below that captures the
// path into the advanced field. Swallowing it silently dropped a template set
// any other way (qm-tools/hand-edited extraArgs) on the first box blur.
export const IGNORE_VALUE = new Set(["-m", "--port", "--host", "--cors-origins", "-c", "-ngl", "--n-cpu-moe", "-b", "--ctx-checkpoints", "--chat-template-kwargs", "-md", "--slot-save-path"]);
// autogen's arch-derived template fix (internal/autogen/generate.go
// qwenFixedChatTemplateFile) — matched by suffix so it is never mistaken for a
// user-chosen template.
export const BUILTIN_CHAT_TEMPLATE = "templates/qwen-fixed-chat-template.jinja";
// Value-less flags owned elsewhere: --reasoning-preserve belongs to the
// preserve-thinking toggle (which is read off the override, not the box).
export const IGNORE_BOOL = new Set(["--kv-unified", "--no-warmup", "--no-webui", "--no-ui", "--jinja", "--metrics", "--props", "--reasoning-preserve", "--no-reasoning-preserve"]);

// Parsed launch-flag bundle shared by the Default form and a variant. Booleans
// are normalized to the form's on/off sense; computed flags are dropped.
export interface ParsedCmd {
  flashOn: boolean;
  mmapOn: boolean;
  mlock: boolean;
  kvInRam: boolean;
  reasoningOn: boolean;
  reasoningBudget: number | "";
  kvK: string;
  kvV: string;
  spec: string;
  threads: number | "";
  parallel: number | "";
  ub: number | "";
  extraArgs: string;
  // "" when the box carries no --chat-template-file, or carries the arch-derived
  // built-in one (that stays owned by autogen, not pinned into the field).
  chatTemplateFile: string;
  // DRY: presence of any --dry-* flag => on; absence => off. Values "" => default.
  dryOn: boolean;
  dryMultiplier: number | "";
  dryBase: number | "";
  dryAllowedLength: number | "";
  // Speculative sub-knobs (value "" / false => omit).
  specDraftNMax: number | "";
  specDefault: boolean;
  specNgramSizeN: number | "";
  specNgramSizeM: number | "";
  specNgramMinHits: number | "";
}

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

// Parse a launch command into form fields + extraArgs passthrough. Computed
// flags (-c/-ngl/--n-cpu-moe) are owned by the sliders, so they're ignored here.
export function parseCmdFields(cmd: string): ParsedCmd {
  const toks = cmd.trim().split(/\s+/);
  let i = 0;
  while (i < toks.length && !toks[i].startsWith("-")) i++; // skip the exe
  const val = (): string => (i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "");
  // A path value is emitted quoted (%q) because templates live in folders with
  // spaces - rejoin what the whitespace split broke apart, then unquote.
  const pathVal = (): string => {
    let s = val();
    if (!s.startsWith('"')) return s;
    while (!(s.length > 1 && s.endsWith('"')) && i + 1 < toks.length) s += " " + toks[++i];
    return s.replace(/^"|"$/g, "");
  };
  let fa: string | null = null,
    ctk: string | null = null,
    ctv: string | null = null,
    t: string | null = null,
    par: string | null = null,
    u: string | null = null,
    sp: string | null = null,
    reason: string | null = null,
    rBudget: string | null = null,
    ctFile: string | null = null;
  let noMmap = false,
    mlockF = false,
    noKv = false,
    specDef = false;
  let dMult: string | null = null,
    dBase: string | null = null,
    dAllow: string | null = null,
    sNMax: string | null = null,
    sNgN: string | null = null,
    sNgM: string | null = null,
    sNgHits: string | null = null;
  const extras: string[] = [];
  for (; i < toks.length; i++) {
    const tk = toks[i];
    switch (tk) {
      case "-fa": fa = val(); break;
      case "-ctk": ctk = val(); break;
      case "-ctv": ctv = val(); break;
      case "-t": t = val(); break;
      case "--parallel": par = val(); break;
      case "-ub": u = val(); break;
      case "--spec-type": { const t = val(); sp = sp ? `${sp}+${t}` : t; break; } // chained backends accumulate
      case "--chat-template-file": ctFile = pathVal(); break;
      case "--reasoning-format": reason = val(); break;
      case "--reasoning-budget": rBudget = val(); break;
      case "--reasoning": if (val() === "off") reason = "off"; break;
      case "--no-mmap": noMmap = true; break; // deprecated, still parsed for old saved commands
      case "--mlock": mlockF = true; break;
      // The enum that replaced all four. -dio is swallowed without setting a
      // field: direct-IO lives on the advanced override, not in this bundle.
      case "-lm":
      case "--load-mode": {
        const m = val();
        // dio is the advanced override's own toggle and re-emits from there; it
        // must not also stamp mmap "off" onto the form.
        if (m !== "dio") noMmap = !loadModeMmap(m);
        if (m.includes("mlock")) mlockF = true;
        break;
      }
      case "-dio": break;
      case "--no-kv-offload": noKv = true; break;
      case "--dry-multiplier": dMult = val(); break;
      case "--dry-base": dBase = val(); break;
      case "--dry-allowed-length": dAllow = val(); break;
      case "--spec-draft-n-max": sNMax = val(); break;
      case "--spec-default": specDef = true; break;
      case "--spec-ngram-map-k4v-size-n": sNgN = val(); break;
      case "--spec-ngram-map-k4v-size-m": sNgM = val(); break;
      case "--spec-ngram-map-k4v-min-hits": sNgHits = val(); break;
      default:
        if (IGNORE_VALUE.has(tk)) val();
        else if (IGNORE_BOOL.has(tk)) break;
        else {
          extras.push(tk);
          const v = i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "";
          if (v) extras.push(v);
        }
    }
  }
  return {
    flashOn: fa !== null ? fa !== "off" : false,
    mmapOn: !noMmap,
    mlock: mlockF,
    kvInRam: noKv,
    reasoningOn: reason !== "none" && reason !== "off",
    reasoningBudget: rBudget !== null && rBudget !== "" ? Number(rBudget) : "",
    kvK: ctk ?? "",
    kvV: ctv ?? "",
    spec: sp ?? "",
    threads: t !== null ? Number(t) : "",
    parallel: par !== null ? Number(par) : "",
    ub: u !== null ? Number(u) : "",
    extraArgs: extras.join(" "),
    // autogen's own Qwen 3.5/3.6 fix is arch-derived - leave it owned by the
    // generator instead of pinning its path into the user's field.
    chatTemplateFile: ctFile && !ctFile.includes(BUILTIN_CHAT_TEMPLATE) ? ctFile : "",
    // DRY is on iff any --dry-* flag survived in the box.
    dryOn: dMult !== null || dBase !== null || dAllow !== null,
    dryMultiplier: dMult !== null && dMult !== "" ? Number(dMult) : "",
    dryBase: dBase !== null && dBase !== "" ? Number(dBase) : "",
    dryAllowedLength: dAllow !== null && dAllow !== "" ? Number(dAllow) : "",
    specDraftNMax: sNMax !== null && sNMax !== "" ? Number(sNMax) : "",
    specDefault: specDef,
    specNgramSizeN: sNgN !== null && sNgN !== "" ? Number(sNgN) : "",
    specNgramSizeM: sNgM !== null && sNgM !== "" ? Number(sNgM) : "",
    specNgramMinHits: sNgHits !== null && sNgHits !== "" ? Number(sNgHits) : "",
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

// Does spec list s contain backend b?
export function specHas(s: string | undefined, b: string): boolean {
  return (s ?? "").split("+").includes(b);
}
// Toggle backend b in the "+"-joined list s. "none" is exclusive (clears the
// rest); checking a real backend clears "none".
export function specToggle(s: string | undefined, b: string, on: boolean): string {
  if (b === "none") return on ? "none" : "";
  let parts = (s ?? "").split("+").filter(Boolean).filter((x) => x !== "none" && x !== b);
  if (on) parts.push(b);
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
