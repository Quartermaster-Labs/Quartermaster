<script lang="ts">
  import { Eraser, X, Check } from "lucide-svelte";

  // Paint a mask over the source for inpaint. Painted (white) region = the area
  // sd-server regenerates; unpainted (black) is kept. Output is a PNG data URL at
  // the source's natural resolution, or null when nothing was painted.
  let {
    source,
    initialMask = null,
    onDone,
    onCancel,
  }: {
    source: string;
    initialMask?: string | null;
    onDone: (mask: string | null) => void;
    onCancel: () => void;
  } = $props();

  let imgEl: HTMLImageElement;
  let canvasEl: HTMLCanvasElement;
  let ctx: CanvasRenderingContext2D | null = null;
  let brush = $state(48);
  let drawing = false;
  let last: { x: number; y: number } | null = null;

  function onImgLoad() {
    canvasEl.width = imgEl.naturalWidth;
    canvasEl.height = imgEl.naturalHeight;
    ctx = canvasEl.getContext("2d");
    if (initialMask && ctx) {
      const m = new Image();
      m.onload = () => ctx?.drawImage(m, 0, 0);
      m.src = initialMask;
    }
  }

  function pos(e: PointerEvent) {
    const r = canvasEl.getBoundingClientRect();
    return {
      x: (e.clientX - r.left) * (canvasEl.width / r.width),
      y: (e.clientY - r.top) * (canvasEl.height / r.height),
    };
  }

  function dot(p: { x: number; y: number }) {
    if (!ctx) return;
    ctx.fillStyle = "#fff";
    ctx.beginPath();
    ctx.arc(p.x, p.y, brush / 2, 0, Math.PI * 2);
    ctx.fill();
  }

  function down(e: PointerEvent) {
    drawing = true;
    last = pos(e);
    dot(last);
    canvasEl.setPointerCapture(e.pointerId);
  }

  function move(e: PointerEvent) {
    if (!drawing || !ctx || !last) return;
    const p = pos(e);
    ctx.strokeStyle = "#fff";
    ctx.lineWidth = brush;
    ctx.lineCap = "round";
    ctx.lineJoin = "round";
    ctx.beginPath();
    ctx.moveTo(last.x, last.y);
    ctx.lineTo(p.x, p.y);
    ctx.stroke();
    last = p;
  }

  function up() {
    drawing = false;
    last = null;
  }

  function clear() {
    ctx?.clearRect(0, 0, canvasEl.width, canvasEl.height);
  }

  // True if any pixel was painted (alpha > 0).
  function hasStrokes(): boolean {
    if (!ctx) return false;
    const { data } = ctx.getImageData(0, 0, canvasEl.width, canvasEl.height);
    for (let i = 3; i < data.length; i += 4) if (data[i] > 0) return true;
    return false;
  }

  function save() {
    if (!hasStrokes()) {
      onDone(null);
      return;
    }
    // Composite the white strokes over a black background = the mask.
    const out = document.createElement("canvas");
    out.width = canvasEl.width;
    out.height = canvasEl.height;
    const o = out.getContext("2d")!;
    o.fillStyle = "#000";
    o.fillRect(0, 0, out.width, out.height);
    o.drawImage(canvasEl, 0, 0);
    onDone(out.toDataURL("image/png"));
  }
</script>

<div
  class="fixed inset-0 bg-black/90 z-50 flex flex-col items-center justify-center p-4 gap-3"
  role="dialog"
  aria-modal="true"
>
  <div class="relative max-w-[80vw] max-h-[72vh] leading-none">
    <img
      bind:this={imgEl}
      src={source}
      alt="mask source"
      onload={onImgLoad}
      class="max-w-[80vw] max-h-[72vh] w-auto h-auto object-contain select-none pointer-events-none"
    />
    <canvas
      bind:this={canvasEl}
      class="absolute inset-0 w-full h-full opacity-50 cursor-crosshair touch-none"
      onpointerdown={down}
      onpointermove={move}
      onpointerup={up}
      onpointerleave={up}
    ></canvas>
  </div>

  <div class="flex items-center gap-4 bg-surface border border-card-border rounded-lg px-4 py-2">
    <label class="flex items-center gap-2 text-xs text-txtsecondary">
      Brush
      <input type="range" min="8" max="160" bind:value={brush} class="accent-primary" />
      <span class="w-8 font-mono">{brush}</span>
    </label>
    <button
      class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
      onclick={clear}
      title="Clear mask"
    >
      <Eraser class="w-4 h-4" /> Clear
    </button>
    <button
      class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
      onclick={onCancel}
      title="Cancel"
    >
      <X class="w-4 h-4" /> Cancel
    </button>
    <button
      class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm bg-primary text-white hover:opacity-90 transition-opacity"
      onclick={save}
      title="Use mask"
    >
      <Check class="w-4 h-4" /> Done
    </button>
  </div>
  <p class="text-xs text-white/60">Paint the area to regenerate. Unpainted stays.</p>
</div>
