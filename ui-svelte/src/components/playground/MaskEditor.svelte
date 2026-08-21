<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { Eraser, X, Check, Brush, Square, MousePointer2, Lasso, Type, LoaderCircle, Sparkles } from "lucide-svelte";
  import { segment, type SamBox, type SamPoint } from "../../lib/samApi";

  // Paint / AI-select a mask over the source for inpaint. White region = the area
  // sd-server regenerates; black is kept. Brush paints freehand; when a SAM model
  // is available, Box/Point ask sam3_server for a mask and Lasso rasterizes a
  // polygon locally — all bake into the same white-on-transparent mask canvas, so
  // the output is one PNG data URL (white on black) at the source's natural
  // resolution, or null when nothing is selected.
  let {
    source,
    initialMask = null,
    model = "",
    onDone,
    onCancel,
  }: {
    source: string;
    initialMask?: string | null;
    model?: string; // "" = no SAM model → brush-only
    onDone: (mask: string | null) => void;
    onCancel: () => void;
  } = $props();

  // Displayed mask tint (transparent pink over the source). Kept separate from the
  // saved PNG, which is always white-on-black — save() rebuilds it from the alpha
  // channel, so the display color is free to be whatever reads best on an image.
  const MASK_RGB: [number, number, number] = [236, 72, 153]; // pink-500
  const MASK_CSS = `rgb(${MASK_RGB[0]} ${MASK_RGB[1]} ${MASK_RGB[2]})`;

  type Tool = "brush" | "box" | "point" | "lasso" | "text";
  let tool = $state<Tool>("brush");
  let brush = $state(48);
  let textPrompt = $state("");
  let busy = $state(false);
  let error = $state<string | null>(null);

  let imgEl: HTMLImageElement;
  let maskEl: HTMLCanvasElement; // persistent white-on-transparent mask
  let gizmoEl: HTMLCanvasElement; // transient prompt gizmos (never saved)
  let mctx: CanvasRenderingContext2D | null = null;
  let W = 0;
  let H = 0;

  // brush stroke
  let drawing = false;
  let last: { x: number; y: number } | null = null;
  // box drag
  let dragging = false;
  let boxStart: { x: number; y: number } | null = null;
  let boxCur: { x: number; y: number } | null = null;
  // point prompts (SAM) + lasso vertices (local), natural coords
  let points = $state<SamPoint[]>([]);
  let lasso = $state<{ x: number; y: number }[]>([]);

  function onImgLoad() {
    W = maskEl.width = gizmoEl.width = imgEl.naturalWidth;
    H = maskEl.height = gizmoEl.height = imgEl.naturalHeight;
    mctx = maskEl.getContext("2d");
    if (initialMask && mctx) bakeResult(initialMask);
  }

  function pos(e: PointerEvent | MouseEvent) {
    const r = maskEl.getBoundingClientRect();
    return {
      x: (e.clientX - r.left) * (W / r.width),
      y: (e.clientY - r.top) * (H / r.height),
    };
  }

  // --- brush ---
  function paintDot(p: { x: number; y: number }) {
    if (!mctx) return;
    mctx.fillStyle = MASK_CSS;
    mctx.beginPath();
    mctx.arc(p.x, p.y, brush / 2, 0, Math.PI * 2);
    mctx.fill();
  }

  // --- SAM result -> mask canvas (replace: the model returns one refined mask
  // per call, so a new result supersedes the previous SAM/lasso selection). The
  // returned PNG is white-on-black; strip the black to transparent and tint the
  // rest pink so it composes over any brush strokes as one mask region.
  function bakeResult(url: string) {
    const im = new Image();
    im.onload = () => {
      if (!mctx) return;
      const tmp = document.createElement("canvas");
      tmp.width = W;
      tmp.height = H;
      const t = tmp.getContext("2d")!;
      t.drawImage(im, 0, 0, W, H);
      const d = t.getImageData(0, 0, W, H);
      const px = d.data;
      for (let i = 0; i < px.length; i += 4) {
        if (px[i] > 127) {
          px[i] = MASK_RGB[0];
          px[i + 1] = MASK_RGB[1];
          px[i + 2] = MASK_RGB[2];
          px[i + 3] = 255;
        } else {
          px[i + 3] = 0;
        }
      }
      t.putImageData(d, 0, 0);
      mctx.clearRect(0, 0, W, H);
      mctx.drawImage(tmp, 0, 0);
    };
    im.src = url;
  }

  function runText() {
    if (textPrompt.trim()) runSegment({ text: textPrompt.trim() });
  }

  async function runSegment(prompt: { text?: string; box?: SamBox; points?: SamPoint[] }) {
    if (!model) return;
    busy = true;
    error = null;
    try {
      const m = await segment(model, source, prompt);
      if (m) {
        bakeResult(m);
      } else {
        error = "No object found - try another spot.";
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  // --- gizmo overlay (transient; box drag rect, point dots, lasso path) ---
  function drawGizmo() {
    const g = gizmoEl.getContext("2d");
    if (!g) return;
    g.clearRect(0, 0, W, H);
    if (tool === "box" && dragging && boxStart && boxCur) {
      g.strokeStyle = "#38bdf8";
      g.lineWidth = Math.max(2, W / 300);
      g.setLineDash([g.lineWidth * 3, g.lineWidth * 2]);
      g.strokeRect(boxStart.x, boxStart.y, boxCur.x - boxStart.x, boxCur.y - boxStart.y);
      g.setLineDash([]);
    }
    if (tool === "point") {
      const rad = Math.max(5, W / 120);
      for (const [x, y, label] of points) {
        g.beginPath();
        g.arc(x, y, rad, 0, Math.PI * 2);
        g.fillStyle = label ? "#22c55e" : "#ef4444";
        g.fill();
        g.lineWidth = rad / 3;
        g.strokeStyle = "#fff";
        g.stroke();
      }
    }
    if (tool === "lasso" && lasso.length) {
      g.strokeStyle = "#38bdf8";
      g.lineWidth = Math.max(2, W / 300);
      g.beginPath();
      g.moveTo(lasso[0].x, lasso[0].y);
      for (const p of lasso.slice(1)) g.lineTo(p.x, p.y);
      g.stroke();
      const rad = Math.max(4, W / 160);
      for (const p of lasso) {
        g.beginPath();
        g.arc(p.x, p.y, rad, 0, Math.PI * 2);
        g.fillStyle = "#38bdf8";
        g.fill();
      }
    }
  }

  // --- pointer routing by tool ---
  function down(e: PointerEvent) {
    if (busy) return;
    if (tool === "brush") {
      drawing = true;
      last = pos(e);
      paintDot(last);
      maskEl.setPointerCapture(e.pointerId);
    } else if (tool === "box") {
      dragging = true;
      boxStart = boxCur = pos(e);
      // Capture on maskEl — it owns the move/up handlers; gizmoEl is
      // pointer-events-none, so capturing there would swallow the drag.
      maskEl.setPointerCapture(e.pointerId);
    }
  }

  function move(e: PointerEvent) {
    if (busy) return;
    if (tool === "brush" && drawing && mctx && last) {
      const p = pos(e);
      mctx.strokeStyle = MASK_CSS;
      mctx.lineWidth = brush;
      mctx.lineCap = "round";
      mctx.lineJoin = "round";
      mctx.beginPath();
      mctx.moveTo(last.x, last.y);
      mctx.lineTo(p.x, p.y);
      mctx.stroke();
      last = p;
    } else if (tool === "box" && dragging) {
      boxCur = pos(e);
      drawGizmo();
    }
  }

  function up() {
    if (tool === "brush") {
      drawing = false;
      last = null;
    } else if (tool === "box" && dragging && boxStart && boxCur) {
      dragging = false;
      const x0 = Math.min(boxStart.x, boxCur.x);
      const y0 = Math.min(boxStart.y, boxCur.y);
      const x1 = Math.max(boxStart.x, boxCur.x);
      const y1 = Math.max(boxStart.y, boxCur.y);
      drawGizmo(); // clears the drag rect
      if (x1 - x0 > 4 && y1 - y0 > 4) {
        runSegment({ box: [Math.round(x0), Math.round(y0), Math.round(x1), Math.round(y1)] });
      }
    }
  }

  function click(e: MouseEvent) {
    if (busy) return;
    const p = pos(e);
    if (tool === "point") {
      const label: 0 | 1 = e.shiftKey || e.button === 2 ? 0 : 1;
      points = [...points, [Math.round(p.x), Math.round(p.y), label]];
      drawGizmo();
      runSegment({ points });
    } else if (tool === "lasso") {
      lasso = [...lasso, p];
      drawGizmo();
    }
  }

  // Lasso: close the polygon and rasterize it white onto the mask (replace).
  function closeLasso() {
    if (lasso.length < 3 || !mctx) return;
    mctx.clearRect(0, 0, W, H);
    mctx.fillStyle = MASK_CSS;
    mctx.beginPath();
    mctx.moveTo(lasso[0].x, lasso[0].y);
    for (const p of lasso.slice(1)) mctx.lineTo(p.x, p.y);
    mctx.closePath();
    mctx.fill();
    lasso = [];
    drawGizmo();
  }

  function setTool(t: Tool) {
    tool = t;
    error = null;
    dragging = false;
    boxStart = boxCur = null;
    points = [];
    lasso = [];
    drawGizmo();
  }

  function clear() {
    mctx?.clearRect(0, 0, W, H);
    points = [];
    lasso = [];
    dragging = false;
    boxStart = boxCur = null;
    error = null;
    drawGizmo();
  }

  // True if any pixel was painted/selected (alpha > 0).
  function hasStrokes(): boolean {
    if (!mctx) return false;
    const { data } = mctx.getImageData(0, 0, W, H);
    for (let i = 3; i < data.length; i += 4) if (data[i] > 0) return true;
    return false;
  }

  function save() {
    if (!hasStrokes()) {
      onDone(null);
      return;
    }
    // Emit a white-on-black PNG from the mask's alpha (the on-screen mask is pink,
    // but sd-server wants white = regenerate, black = keep, regardless of tint).
    const src = mctx!.getImageData(0, 0, W, H).data;
    const out = document.createElement("canvas");
    out.width = W;
    out.height = H;
    const o = out.getContext("2d")!;
    const d = o.createImageData(W, H);
    const dp = d.data;
    for (let i = 0; i < src.length; i += 4) {
      const on = src[i + 3] > 0 ? 255 : 0;
      dp[i] = dp[i + 1] = dp[i + 2] = on;
      dp[i + 3] = 255;
    }
    o.putImageData(d, 0, 0);
    onDone(out.toDataURL("image/png"));
  }

  const hint: Record<Tool, string> = {
    brush: "Paint the area to regenerate. Unpainted stays.",
    box: "Drag a box around the object.",
    point: "Click the object. Shift/right-click marks what to exclude.",
    lasso: "Click to trace an outline, then Close.",
    text: "Name what to select - every match is masked.",
  };
</script>

<div
  class="fixed inset-0 bg-black/90 z-50 flex flex-col md:flex-row items-center justify-center p-4 gap-3"
  role="dialog"
  aria-modal="true"
>
  <div class="relative max-w-[calc(80vw/var(--qm-scale))] max-h-[calc(72vh/var(--qm-scale))] leading-none">
    <img
      bind:this={imgEl}
      src={source}
      alt="mask source"
      onload={onImgLoad}
      class="max-w-[calc(80vw/var(--qm-scale))] max-h-[calc(72vh/var(--qm-scale))] w-auto h-auto object-contain select-none pointer-events-none"
    />
    <canvas
      bind:this={maskEl}
      class="absolute inset-0 w-full h-full opacity-50 touch-none {busy ? 'cursor-wait' : 'cursor-crosshair'}"
      onpointerdown={down}
      onpointermove={move}
      onpointerup={up}
      onpointerleave={up}
      onclick={click}
      oncontextmenu={(e) => { e.preventDefault(); if (tool === "point") click(e); }}
    ></canvas>
    <canvas
      bind:this={gizmoEl}
      class="absolute inset-0 w-full h-full pointer-events-none"
    ></canvas>
    {#if busy}
      <div class="absolute inset-0 flex items-center justify-center pointer-events-none">
        <LoaderCircle class="w-8 h-8 text-white animate-spin" />
      </div>
    {/if}
  </div>

  <div class="flex md:flex-col items-stretch gap-2 bg-surface border border-card-border rounded-lg p-2 md:w-[13rem]">
    <button class="seg-tool {tool === 'brush' ? 'seg-active' : ''}" onclick={() => setTool("brush")} use:tip={"Brush (freehand)"}>
      <Brush class="w-4 h-4" /> Brush
    </button>
    {#if model}
      <button class="seg-tool {tool === 'box' ? 'seg-active' : ''}" onclick={() => setTool("box")} use:tip={"Box select (AI)"}>
        <Square class="w-4 h-4" /> Box <Sparkles class="w-3 h-3 ml-auto opacity-40" />
      </button>
      <button class="seg-tool {tool === 'point' ? 'seg-active' : ''}" onclick={() => setTool("point")} use:tip={"Point select (AI)"}>
        <MousePointer2 class="w-4 h-4" /> Point <Sparkles class="w-3 h-3 ml-auto opacity-40" />
      </button>
      <button class="seg-tool {tool === 'lasso' ? 'seg-active' : ''}" onclick={() => setTool("lasso")} use:tip={"Lasso (freehand polygon)"}>
        <Lasso class="w-4 h-4" /> Lasso
      </button>
      {#if tool === "lasso"}
        <button class="seg-tool" onclick={closeLasso} disabled={lasso.length < 3} use:tip={"Close the polygon"}>
          <Check class="w-4 h-4" /> Close
        </button>
      {/if}
      <button class="seg-tool {tool === 'text' ? 'seg-active' : ''}" onclick={() => setTool("text")} use:tip={"Text select (AI)"}>
        <Type class="w-4 h-4" /> Text <Sparkles class="w-3 h-3 ml-auto opacity-40" />
      </button>
      {#if tool === "text"}
        <div class="flex items-center gap-1 px-1">
          <input
            type="text"
            bind:value={textPrompt}
            onkeydown={(e) => { if (e.key === "Enter") runText(); }}
            placeholder="e.g. the dog"
            disabled={busy}
            class="flex-1 min-w-0 bg-secondary/40 border border-card-border rounded px-2 py-1 text-xs text-txtmain placeholder:text-txtsecondary focus:outline-none focus:border-primary"
          />
          <button class="seg-tool !px-2" onclick={runText} disabled={busy || !textPrompt.trim()} use:tip={"Segment by text"}>
            <Check class="w-4 h-4" />
          </button>
        </div>
      {/if}
    {/if}

    {#if tool === "brush"}
      <label class="flex items-center gap-2 text-xs text-txtsecondary px-1 md:mt-1">
        Size
        <input type="range" min="8" max="160" bind:value={brush} class="accent-primary w-full" />
        <span class="w-8 font-mono text-right">{brush}</span>
      </label>
    {/if}

    <div class="hidden md:block border-t border-card-border my-1"></div>

    <button class="seg-tool" onclick={clear} use:tip={"Clear mask"}>
      <Eraser class="w-4 h-4" /> Clear
    </button>
    <button class="seg-tool" onclick={onCancel} use:tip={"Cancel"}>
      <X class="w-4 h-4" /> Cancel
    </button>
    <button class="seg-tool !bg-primary !text-white hover:opacity-90" onclick={save} use:tip={"Use mask"}>
      <Check class="w-4 h-4" /> Done
    </button>
  </div>

  <p class="text-xs text-white/60 md:absolute md:bottom-4 md:left-1/2 md:-translate-x-1/2">
    {error ?? hint[tool]}
  </p>
</div>

<style>
  .seg-tool {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.375rem 0.625rem;
    border-radius: 0.375rem;
    font-size: 0.8125rem;
    color: var(--txtsecondary, #9ca3af);
    transition: color 0.15s, background 0.15s;
  }
  .seg-tool:hover:not(:disabled) {
    color: var(--txtmain, #f3f4f6);
    background: var(--secondary, rgba(255, 255, 255, 0.08));
  }
  .seg-tool:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .seg-active {
    color: var(--txtmain, #f3f4f6);
    background: var(--secondary, rgba(255, 255, 255, 0.12));
  }
</style>
