<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { tip } from "../../lib/tooltip";
  import Toggle from "../Toggle.svelte";
  import { updateScore, removeScore, type LibraryScore } from "../../stores/scoreLibrary";
  import type { ScoreModel } from "../../lib/abcScore";
  import type { ScorePlayer } from "../../lib/scorePlayer";
  import { createScorePlayer } from "../../lib/scorePlayer";
  import { Play, Pause, SkipBack, Download, Trash2, Code, Wand2, Link as LinkIcon } from "lucide-svelte";

  // One library score: engraved by abcjs, played through lib/scorePlayer.ts
  // with a playhead line and the sounding note lit (a click on the sheet
  // moves the playhead), editable as ABC, and exported as .abc,
  // .musicxml (MuseScore, Finale, Sibelius) or .mid.
  //
  // abcjs is ~400 KB, so lib/abcScore.ts (which owns it) is imported on first
  // use rather than shipped in the playground's entry chunk.
  let {
    score,
    volume = 1,
    useLabel = "",
    onuse,
    disclaimer = "",
  }: {
    score: LibraryScore;
    volume?: number;
    // Empty hides "Use in Create" (no generator takes a score).
    useLabel?: string;
    onuse?: (s: LibraryScore) => void;
    disclaimer?: string;
  } = $props();

  type Lib = typeof import("../../lib/abcScore");
  let lib = $state<Lib | null>(null);
  let loadError = $state("");
  void import("../../lib/abcScore")
    .then((m) => (lib = m))
    .catch((e) => (loadError = e instanceof Error ? e.message : String(e)));

  let sheetEl = $state<HTMLDivElement | undefined>();
  let model = $state<ScoreModel | null>(null);
  let parseError = $state("");
  let visual: { getElementFromChar: (c: number) => { abselem?: { elemset: SVGElement[] } } | null } | null = null;

  let showSource = $state(false);
  let draft = $state("");
  let title = $state("");
  let chords = $state(true);
  let confirmDelete = $state(false);

  // A different score resets the form; an edit to this one does not.
  let shownId = "";
  $effect(() => {
    if (score.id === shownId) return;
    shownId = score.id;
    draft = score.abc;
    title = score.title;
    confirmDelete = false;
    stop();
  });

  // Edits are saved debounced: the store PUTs the whole library on change.
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  function onDraft() {
    if (saveTimer) clearTimeout(saveTimer);
    const id = score.id;
    const abc = draft;
    saveTimer = setTimeout(() => updateScore(id, { abc }), 500);
  }
  function commitTitle() {
    const t = title.trim();
    if (t && t !== score.title) updateScore(score.id, { title: t });
    else title = score.title;
  }

  // Engrave and parse whatever the draft holds now.
  $effect(() => {
    const abc = draft;
    const el = sheetEl;
    if (!lib || !el) return;
    stop();
    try {
      const tunes = lib.abcjs.renderAbc(el, abc, {
        responsive: "resize",
        add_classes: true,
        foregroundColor: "currentColor",
        paddingleft: 0,
        paddingright: 0,
      });
      visual = (tunes[0] as unknown as typeof visual) ?? null;
    } catch {
      visual = null;
    }
    try {
      model = lib.parseScore(abc);
      parseError = "";
    } catch (e) {
      model = null;
      parseError = e instanceof Error ? e.message : String(e);
    }
    // Untracked: layout reads pos, which must not re-engrave the sheet each frame.
    untrack(layout);
  });

  // --- playhead -----------------------------------------------------------------
  // Times come from our own model (the clock the player runs on), places from
  // the engraving: each onset sits at the left edge of the notes that start
  // there, and the line is interpolated between onsets so it glides. All of it
  // is in the SVG's own units, so the responsive viewBox scales it for free.
  interface Mark {
    t: number;
    x: number;
    sys: number;
  }
  interface System {
    top: number;
    bottom: number;
    right: number;
  }
  let marks: Mark[] = [];
  let systems: System[] = [];
  let svg: SVGSVGElement | null = null;
  let cursor: SVGLineElement | null = null;

  function layout() {
    marks = [];
    systems = [];
    cursor = null;
    svg = sheetEl?.querySelector("svg") ?? null;
    if (!svg || !visual || !model || !lib) return;
    // abcjs wraps each system (all its staves) in one of these.
    const wrappers = [...svg.querySelectorAll<SVGGElement>(".abcjs-staff-wrapper")];
    systems = wrappers.map((g) => {
      const b = g.getBBox();
      return { top: b.y, bottom: b.y + b.height, right: b.x + b.width };
    });
    for (const p of lib.timeline(model)) {
      let x = Infinity;
      let sys = -1;
      for (const c of p.chars) {
        for (const e of visual.getElementFromChar(c)?.abselem?.elemset ?? []) {
          const g = e as SVGGraphicsElement;
          if (typeof g.getBBox !== "function") continue;
          const b = g.getBBox();
          if (!b.width && !b.height) continue;
          if (b.x < x) {
            x = b.x;
            const w = g.closest(".abcjs-staff-wrapper");
            sys = w ? wrappers.indexOf(w as SVGGElement) : -1;
          }
        }
      }
      if (sys >= 0) marks.push({ t: p.t, x, sys });
    }
    if (!marks.length) return;
    cursor = document.createElementNS("http://www.w3.org/2000/svg", "line");
    cursor.setAttribute("class", "qm-playhead");
    svg.appendChild(cursor);
    place(pos);
  }

  // The span time t falls in: from the onset at or before t to the next one,
  // or to the end of the system when the next onset starts a new system.
  function span(i: number) {
    const a = marks[i];
    const b = marks[i + 1];
    return { a, x1: b && b.sys === a.sys ? b.x : systems[a.sys].right, t1: b ? b.t : duration };
  }

  function place(t: number) {
    if (!cursor || !marks.length) return;
    let i = 0;
    while (i + 1 < marks.length && marks[i + 1].t <= t) i++;
    const { a, x1, t1 } = span(i);
    const f = t1 > a.t ? Math.min(1, Math.max(0, (t - a.t) / (t1 - a.t))) : 0;
    const x = a.x + (x1 - a.x) * f;
    const sys = systems[a.sys];
    cursor.setAttribute("x1", String(x));
    cursor.setAttribute("x2", String(x));
    cursor.setAttribute("y1", String(sys.top));
    cursor.setAttribute("y2", String(sys.bottom));
  }

  // The inverse of place(): a click on a system becomes a time.
  function seekAt(e: MouseEvent) {
    if (!svg || !marks.length) return;
    const ctm = svg.getScreenCTM();
    if (!ctm) return;
    const pt = new DOMPoint(e.clientX, e.clientY).matrixTransform(ctm.inverse());
    // The system under the pointer, or the nearer one in the gap between two.
    let si = 0;
    let best = Infinity;
    systems.forEach((s, k) => {
      const d = pt.y < s.top ? s.top - pt.y : pt.y > s.bottom ? pt.y - s.bottom : 0;
      if (d < best) [best, si] = [d, k];
    });
    let i = marks.findIndex((m) => m.sys === si);
    if (i < 0) return;
    while (i + 1 < marks.length && marks[i + 1].sys === si && marks[i + 1].x <= pt.x) i++;
    const { a, x1, t1 } = span(i);
    const f = x1 > a.x ? Math.min(1, Math.max(0, (pt.x - a.x) / (x1 - a.x))) : 0;
    seek(a.t + (t1 - a.t) * f);
  }

  // --- playback -----------------------------------------------------------------
  let player: ScorePlayer | null = null;
  let playing = $state(false);
  let pos = $state(0);
  let raf = 0;
  let lit: SVGElement[] = [];

  function unlight() {
    for (const e of lit) e.classList.remove("qm-score-hl");
    lit = [];
  }

  function frame() {
    if (!player?.playing) return;
    pos = player.position();
    place(pos);
    unlight();
    for (const ev of events) {
      if (ev.start > pos) break;
      if (ev.part < 0 || ev.end <= pos) continue;
      const set = visual?.getElementFromChar(ev.startChar)?.abselem?.elemset ?? [];
      for (const e of set) {
        e.classList.add("qm-score-hl");
        lit.push(e);
      }
    }
    raf = requestAnimationFrame(frame);
  }

  let events: ReturnType<Lib["noteEvents"]> = [];
  function play() {
    if (!lib || !model) return;
    events = lib.noteEvents(model, chords);
    player = createScorePlayer(events, {
      volume,
      onend: () => {
        playing = false;
        pos = 0;
        place(0);
        unlight();
      },
    });
    player.play(pos < duration ? pos : 0);
    playing = true;
    raf = requestAnimationFrame(frame);
  }
  // Pause keeps the position, so Play carries on from it.
  function pause() {
    cancelAnimationFrame(raf);
    player?.stop();
    player = null;
    playing = false;
    unlight();
  }
  function stop() {
    pause();
    pos = 0;
    place(0);
  }
  function seek(t: number) {
    pos = Math.max(0, Math.min(t, duration));
    place(pos);
    if (player?.playing) {
      unlight();
      player.play(pos);
    }
  }
  $effect(() => {
    player?.setVolume(volume);
  });
  onDestroy(() => {
    stop();
    if (saveTimer) {
      clearTimeout(saveTimer);
      if (draft !== score.abc) updateScore(score.id, { abc: draft });
    }
  });

  let duration = $derived(model && lib ? lib.noteEvents(model, false).reduce((m, e) => Math.max(m, e.end), 0) : 0);
  const clock = (s: number) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;

  // --- export -------------------------------------------------------------------
  function save(blob: Blob, ext: string) {
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${lib?.fileStem(score.title) ?? "score"}.${ext}`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  let exportError = $state("");
  function exportAs(kind: "abc" | "musicxml" | "mid") {
    exportError = "";
    try {
      if (kind === "abc") save(new Blob([draft], { type: "text/plain" }), "abc");
      else if (!lib) return;
      else if (kind === "mid") save(lib.toMidi(draft), "mid");
      else if (model) save(new Blob([lib.toMusicXML(model)], { type: "application/vnd.recordare.musicxml+xml" }), "musicxml");
    } catch (e) {
      exportError = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<div class="flex flex-col min-h-0 h-full">
  <div class="flex items-center gap-2 min-h-10 shrink-0 flex-wrap py-1">
    <input
      bind:value={title}
      onblur={commitTitle}
      onkeydown={(e) => {
        if (e.key === "Enter") (e.currentTarget as HTMLInputElement).blur();
        if (e.key === "Escape") {
          title = score.title;
          (e.currentTarget as HTMLInputElement).blur();
        }
      }}
      aria-label="Score title"
      class="min-w-0 flex-1 bg-transparent text-sm font-medium text-txtmain rounded px-1 -mx-1 border border-transparent hover:border-card-border focus:border-primary focus:outline-none"
    />
    <div class="flex items-center gap-1.5 shrink-0">
      <button class="btn btn--sm btn--icon" onclick={stop} disabled={!playing && pos === 0} use:tip={"Back to the start"} aria-label="Back to the start">
        <SkipBack class="w-3.5 h-3.5" />
      </button>
      {#if playing}
        <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={pause}>
          <Pause class="w-3.5 h-3.5" /> Pause
        </button>
      {:else}
        <button class="btn btn--sm btn--primary inline-flex items-center gap-1.5" onclick={play} disabled={!model}>
          <Play class="w-3.5 h-3.5" /> Play
        </button>
      {/if}
      <span class="font-mono text-micro text-txtsecondary tabular-nums w-[4.5rem]">{clock(pos)} / {clock(duration)}</span>
      <label class="flex items-center gap-1.5 text-micro font-medium uppercase tracking-wide text-txtsecondary cursor-pointer" use:tip={"Play the chord symbols under the melody"}>
        <Toggle size="sm" bind:checked={chords} disabled={playing} />
        Chords
      </label>
    </div>
  </div>

  <div class="flex items-center gap-1.5 flex-wrap shrink-0 pb-2">
    {#if useLabel && onuse}
      <button class="btn btn--sm btn--primary-outline inline-flex items-center gap-1.5" onclick={() => onuse({ ...score, abc: draft })} disabled={!draft.trim()}>
        <Wand2 class="w-3.5 h-3.5" /> {useLabel}
      </button>
    {/if}
    <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={() => exportAs("musicxml")} disabled={!model} use:tip={"MusicXML: opens in MuseScore, Finale, Sibelius, Dorico"}>
      <Download class="w-3.5 h-3.5" /> MusicXML
    </button>
    <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={() => exportAs("mid")} disabled={!model}>
      <Download class="w-3.5 h-3.5" /> MIDI
    </button>
    <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={() => exportAs("abc")}>
      <Download class="w-3.5 h-3.5" /> ABC
    </button>
    <button class="btn btn--sm inline-flex items-center gap-1.5" aria-pressed={showSource} onclick={() => (showSource = !showSource)}>
      <Code class="w-3.5 h-3.5" /> {showSource ? "Hide ABC" : "Edit ABC"}
    </button>
    <div class="ml-auto">
      {#if confirmDelete}
        <button class="btn btn--sm btn--danger" onclick={() => removeScore(score.id)}>Delete score</button>
        <button class="btn btn--sm btn--ghost" onclick={() => (confirmDelete = false)}>Keep</button>
      {:else}
        <button class="btn btn--sm btn--icon btn--danger-hover" onclick={() => (confirmDelete = true)} use:tip={"Delete from the library"} aria-label="Delete score">
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      {/if}
    </div>
  </div>

  {#if exportError}<p class="text-xs text-error pb-2">{exportError}</p>{/if}
  {#if loadError}<p class="text-xs text-error pb-2">Could not load the score engine: {loadError}</p>{/if}
  {#if parseError}<p class="text-xs text-error pb-2">{parseError}</p>{/if}

  {#if showSource}
    <textarea
      bind:value={draft}
      oninput={onDraft}
      rows="10"
      spellcheck="false"
      aria-label="ABC source"
      class="shrink-0 w-full rounded border border-card-border bg-background px-2 py-1.5 text-xs font-mono leading-relaxed resize-y min-h-[6rem] max-h-[calc(40vh/var(--qm-scale))] focus:outline-none focus:border-primary mb-2"
    ></textarea>
  {/if}

  <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll rounded-lg border border-card-border-inner bg-surface px-3 py-2">
    {#if !lib && !loadError}
      <p class="py-8 text-center text-xs text-txtsecondary">Loading the score…</p>
    {/if}
    <!-- Pointer-only by design: Play/Pause and Back to the start are the keyboard path. -->
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div bind:this={sheetEl} class="qm-sheet text-txtmain cursor-pointer" onclick={seekAt}></div>
  </div>

  {#if score.origin || disclaimer}
    <div class="shrink-0 pt-2 flex flex-col gap-1">
      {#if score.origin}
        <span class="text-micro text-txtsecondary inline-flex items-center gap-1.5 min-w-0">
          Read from
          {#if score.origin.url}
            <a class="inline-flex items-center gap-1 text-primary hover:underline truncate" href={score.origin.url} target="_blank" rel="noreferrer noopener">
              <LinkIcon class="w-3 h-3 shrink-0" />{score.origin.label}
            </a>
          {:else}
            <span class="truncate">{score.origin.label}</span>
          {/if}
        </span>
      {/if}
      {#if score.origin && disclaimer}
        <p class="text-micro text-txtsecondary leading-snug opacity-80">{disclaimer}</p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .qm-sheet :global(.qm-playhead) {
    stroke: var(--color-primary);
    stroke-width: 2;
    stroke-linecap: round;
    vector-effect: non-scaling-stroke;
    pointer-events: none;
  }
  /* abcjs draws with fill/stroke attributes; the lit note overrides both. */
  .qm-sheet :global(.qm-score-hl) {
    fill: var(--color-primary);
    stroke: var(--color-primary);
  }
</style>
