<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { get } from "svelte/store";
  import { models } from "../../stores/api";
  import { modelCategory } from "../../lib/modelUtils";
  import { userPref } from "../../stores/prefs";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { normalizeImageFile } from "../../lib/imageNormalize";
  import { confirmDecisionBackend } from "../../lib/decisionBackend";
  import {
    QUESTION_TYPES,
    MIN_CHOICES,
    MIN_LEVELS,
    MAX_LEVELS,
    MAX_FILES,
    buildRequest,
    levelProbs,
    newQuestion,
    normalizeDrafts,
    pct,
    readAsDataUrl,
    runDecision,
    scorePercent,
    sortedProbs,
    validate,
    type Answer,
    type DraftQuestion,
    type SystemOneResponse,
  } from "../../lib/decisionApi";
  import ModelSelector from "./ModelSelector.svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import {
    Split,
    Play,
    Square,
    Plus,
    X,
    ChevronUp,
    ChevronDown,
    Paperclip,
    FileAudio,
    MessageSquareText,
    CircleQuestionMark,
    ListChecks,
    Gauge,
    ToggleLeft,
  } from "lucide-svelte";

  // Decision studio. A decision model does not chat: it is shown a PROMPT (the
  // state: text, JSON, images) and asked a question, and answers with a
  // probability distribution (POST /v1/systemone). Left column is the request:
  // prompt, attachments, the question with its 3-way output toggle and the
  // answers it may give, and the model + Run pinned below. Right column is the
  // answer. The layout is the same with no model on the box, so the tab shows
  // what it does before there is anything to run. The API takes several
  // questions per request; the studio asks one, which is what keeps it a form
  // and not a builder. Prompt and question are per-user prefs, the answer is
  // only on screen.

  // The id the one question is sent under. Never shown: with one question the
  // answer card needs no name.
  const QID = "answer";

  const selectedModelStore = userPref<string>("playground-decision-model", "");
  const stateStore = userPref<string>("playground-decision-state", "");
  const questionsStore = userPref<DraftQuestion[]>("playground-decision-questions", []);

  let stateText = $state(get(stateStore));
  // Older builds saved a list; its first entry is the question carried over.
  let q = $state<DraftQuestion>(normalizeDrafts(get(questionsStore))[0] ?? newQuestion(QID));

  // Prefs save debounced, so writing on every keystroke is cheap. snapshot()
  // reads the whole tree, which is what makes the effect track nested edits.
  $effect(() => {
    stateStore.set(stateText);
  });
  $effect(() => {
    questionsStore.set([$state.snapshot(q)]);
  });

  // The picker hard-filters to decision models and renders nothing when none
  // exist, so the composer draws an empty slot of its own in that case.
  let decisionModels = $derived($models.filter((m) => !m.unlisted && modelCategory(m) === "decision"));
  let hasModels = $derived(decisionModels.length > 0);
  let model = $derived($models.find((m) => m.id === $selectedModelStore));

  $effect(() => {
    const list = decisionModels;
    if (!list.length) return;
    if (!list.some((m) => m.id === get(selectedModelStore))) selectedModelStore.set(list[0].id);
  });

  // `files` is only accepted by models that read the modality. The dashboard
  // catalog (/api/models) carries capabilities but not /v1/models'
  // architecture block, so vision (set when a projector is paired) is the
  // signal. Audio-reading decision models are not emitted yet.
  let takesImages = $derived(!!model?.capabilities?.vision);
  let takesAudio = $derived(false);
  let canAttach = $derived(takesImages || takesAudio);
  let accept = $derived([takesImages ? "image/*" : "", takesAudio ? "audio/*" : ""].filter(Boolean).join(","));

  // --- attachments ----------------------------------------------------------
  type Attached = { id: number; name: string; url: string; kind: "image" | "audio" };
  let attached = $state<Attached[]>([]);
  let nextFileId = 1;
  let fileInput = $state<HTMLInputElement | null>(null);
  let isDragging = $state(false);

  // Switching to a model that cannot read what was attached would otherwise send
  // it and earn a 400 naming nothing the user can see.
  $effect(() => {
    const img = takesImages;
    const aud = takesAudio;
    if (attached.some((a) => (a.kind === "image" && !img) || (a.kind === "audio" && !aud))) {
      attached = attached.filter((a) => (a.kind === "image" ? img : aud));
    }
  });

  async function attachFiles(list: FileList | File[] | null | undefined) {
    if (!list) return;
    const picked: Attached[] = [];
    for (const f of Array.from(list)) {
      const isImage = f.type.startsWith("image/");
      const isAudio = f.type.startsWith("audio/");
      if (!((isImage && takesImages) || (isAudio && takesAudio))) {
        error = `${f.name || "That file"}: this model does not take ${isImage ? "images" : isAudio ? "audio" : "that kind of file"}.`;
        continue;
      }
      try {
        const url = isImage ? await normalizeImageFile(f) : await readAsDataUrl(f);
        picked.push({ id: nextFileId++, name: f.name, url, kind: isImage ? "image" : "audio" });
      } catch (e) {
        error = e instanceof Error ? e.message : "Could not read the file.";
      }
    }
    const room = MAX_FILES - attached.length;
    if (picked.length > room) error = `At most ${MAX_FILES} files per request.`;
    attached = [...attached, ...picked.slice(0, Math.max(0, room))];
    if (fileInput) fileInput.value = "";
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    isDragging = false;
    if (canAttach) attachFiles(e.dataTransfer?.files);
  }

  // --- question -------------------------------------------------------------
  function addChoice(q: DraftQuestion) {
    q.choices.push({ option: "", description: "" });
  }
  function removeChoice(q: DraftQuestion, i: number) {
    q.choices.splice(i, 1);
  }
  function addLevel(q: DraftQuestion) {
    if (q.levels.length < MAX_LEVELS) q.levels.push("");
  }
  function removeLevel(q: DraftQuestion, i: number) {
    if (q.levels.length > MIN_LEVELS) q.levels.splice(i, 1);
  }
  function moveLevel(q: DraftQuestion, i: number, d: -1 | 1) {
    const j = i + d;
    if (j < 0 || j >= q.levels.length) return;
    [q.levels[i], q.levels[j]] = [q.levels[j], q.levels[i]];
  }

  // --- run ------------------------------------------------------------------
  let running = $state(false);
  let error = $state<string | null>(null);
  let result = $state<SystemOneResponse | null>(null);
  // The instructions as they were when the run started: the cards beside the
  // answers should describe what was asked, not what is being edited now.
  let askedWith = $state<Record<string, string>>({});
  let abort: AbortController | null = null;

  $effect(() => {
    playgroundStores.decisionRunning.set(running);
  });

  async function run() {
    if (running || !hasModels) return;
    const questions = [{ ...q, id: QID }];
    const problems = validate($selectedModelStore, questions);
    if (problems.length) {
      error = problems.join("\n");
      return;
    }
    // Posting loads the model, so the outdated-backend warning applies here too.
    if (!(await confirmDecisionBackend(model))) return;
    error = null;
    running = true;
    abort = new AbortController();
    const asked: Record<string, string> = {};
    asked[QID] = q.instructions.trim();
    try {
      const req = buildRequest(
        $selectedModelStore,
        stateText,
        questions,
        attached.map((a) => a.url),
      );
      result = await runDecision(req, abort.signal);
      askedWith = asked;
    } catch (e) {
      if (!(e instanceof Error && e.name === "AbortError")) {
        error = e instanceof Error ? e.message : "An error occurred";
      }
    } finally {
      running = false;
      abort = null;
    }
  }

  function onStateKey(e: KeyboardEvent) {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      run();
    }
  }

  const QUESTION_HINT: Record<DraftQuestion["type"], string> = {
    choice: "What should the model pick? e.g. What does the customer want?",
    score: "What should the model rate? e.g. How urgent is this?",
    noul: "A statement to judge true or false. e.g. The customer is angry.",
  };

  let answerList = $derived(result ? (Object.entries(result.answers) as [string, Answer][]) : []);

  const TYPE_ICON: Record<DraftQuestion["type"], typeof Split> = { choice: ListChecks, score: Gauge, noul: ToggleLeft };
  const TYPE_LABEL: Record<string, string> = Object.fromEntries(QUESTION_TYPES.map((t) => [t.id, t.label]));

  const card = "rounded-2xl border border-card-border bg-surface";
  // A textarea that IS the card's body: the card draws the border and the focus ring.
  const inner = "w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary";
  const field =
    "w-full px-2.5 py-1.5 rounded-lg border border-card-border bg-background text-[0.8125rem] focus:outline-none focus:border-primary focus:ring-2 focus:ring-primary/30 transition-colors";
  const addRow =
    "w-full rounded-lg border border-dashed border-card-border py-1.5 text-[0.75rem] text-txtsecondary inline-flex items-center justify-center gap-1 transition-colors hover:text-primary hover:border-primary disabled:opacity-40 disabled:pointer-events-none";
</script>

{#snippet bar(p: number, winner: boolean)}
  <div class="flex-1 min-w-0 h-2.5 rounded-full bg-secondary overflow-hidden">
    <div
      class="h-full rounded-full transition-[width] duration-500 ease-out {winner ? 'bg-primary' : 'bg-txtsecondary/35'}"
      style="width: {Math.max(0, Math.min(100, p * 100))}%"
    ></div>
  </div>
{/snippet}

{#snippet badge(text: string, tone: string = "bg-secondary text-txtsecondary")}
  <span class="shrink-0 w-6 h-6 rounded-full grid place-items-center font-mono text-micro font-medium tabular-nums {tone}">{text}</span>
{/snippet}

{#snippet header(Icon: typeof Split, title: string, aside: string = "")}
  <div class="flex items-center gap-2 px-4 pt-3 pb-1.5">
    <span class="w-6 h-6 rounded-md grid place-items-center bg-secondary text-primary"><Icon class="w-3.5 h-3.5" /></span>
    <span class="text-[0.8125rem] font-medium text-txtmain">{title}</span>
    <div class="flex-1"></div>
    {#if aside}<span class="text-micro text-txtsecondary">{aside}</span>{/if}
  </div>
{/snippet}

<div class="flex flex-col h-full">
  <div class="flex-1 flex flex-col md:flex-row gap-4 min-h-0 w-full py-4">
    <!-- LEFT (60%): the request, with model + Run pinned below ---------------- -->
    <div class="w-full md:w-3/5 shrink-0 flex flex-col gap-3 min-h-0">
      <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b flex flex-col gap-3 px-1 pb-4" use:scrollFade>
        <!-- Prompt (the state the model judges). Grows into the column's spare
             height, so a short question leaves no dead band under the form. -->
        <section class="{card} flex-1 min-h-[9rem] flex flex-col focus-within:border-primary transition-colors">
          {@render header(MessageSquareText, "Prompt", "text or JSON")}
          <textarea
            id="decision-state"
            aria-label="Prompt"
            class="{inner} flex-1 min-h-[5rem] px-4 pb-3"
            placeholder={"The situation to judge. Plain text, or a JSON object / array (sent as structured data)."}
            bind:value={stateText}
            onkeydown={onStateKey}
            disabled={running}
          ></textarea>
        </section>

        <!-- Attachments: only for models that read images or audio -->
        {#if canAttach}
          <section class="{card} shrink-0 flex flex-col pb-3">
            <div class="flex items-center gap-2 px-4 pt-3 pb-1.5">
              <span class="w-6 h-6 rounded-md grid place-items-center bg-secondary text-primary"><Paperclip class="w-3.5 h-3.5" /></span>
              <span class="text-[0.8125rem] font-medium text-txtmain">Files</span>
              <span class="text-micro text-txtsecondary">{takesImages ? "images" : ""}{takesImages && takesAudio ? " and " : ""}{takesAudio ? "audio" : ""}, up to {MAX_FILES}</span>
              <div class="flex-1"></div>
              <button class="btn btn--sm inline-flex items-center gap-1" onclick={() => fileInput?.click()} disabled={running || attached.length >= MAX_FILES}>
                <Paperclip class="w-3 h-3" /> Attach
              </button>
            </div>
            <div
              role="region"
              aria-label="File drop zone"
              class="mx-4 rounded-lg border border-dashed p-2 flex flex-wrap gap-2 min-h-[3rem] items-center transition-colors {isDragging
                ? 'border-primary bg-primary/10'
                : 'border-card-border bg-background'}"
              ondragover={(e) => { e.preventDefault(); isDragging = true; }}
              ondragleave={() => (isDragging = false)}
              ondrop={onDrop}
            >
              {#each attached as a (a.id)}
                <div class="relative group">
                  {#if a.kind === "image"}
                    <img src={a.url} alt={a.name} class="h-16 w-16 object-cover rounded-md border border-card-border" />
                  {:else}
                    <div class="h-16 px-3 flex items-center gap-1.5 rounded-md border border-card-border bg-surface text-[0.6875rem] text-txtsecondary">
                      <FileAudio class="w-4 h-4 shrink-0" /> <span class="max-w-[8rem] truncate">{a.name}</span>
                    </div>
                  {/if}
                  <button
                    class="absolute -top-1.5 -right-1.5 p-0.5 rounded-full bg-surface border border-card-border text-txtsecondary hover:text-error"
                    onclick={() => (attached = attached.filter((x) => x.id !== a.id))}
                    aria-label="Remove {a.name}"
                    disabled={running}
                  >
                    <X class="w-3 h-3" />
                  </button>
                </div>
              {/each}
              {#if attached.length === 0}
                <span class="text-[0.75rem] text-txtsecondary px-1">Drop files here or use Attach.</span>
              {/if}
            </div>
            <input type="file" {accept} multiple class="hidden" bind:this={fileInput} onchange={(e) => attachFiles((e.target as HTMLInputElement).files)} />
          </section>
        {/if}

        <!-- Question: what to ask, what kind of answer, and the answers allowed -->
        <section class="{card} shrink-0 flex flex-col">
          {@render header(CircleQuestionMark, "Question")}
          <textarea
            id="decision-question"
            aria-label="Question"
            class="{inner} text-[0.9375rem] px-4 pb-3"
            rows="2"
            placeholder={QUESTION_HINT[q.type]}
            bind:value={q.instructions}
            onkeydown={onStateKey}
            disabled={running}
          ></textarea>

          <div class="border-t border-card-border px-4 pt-3 pb-4 flex flex-col gap-3">
            <!-- The 3-way output toggle, as tiles: the hint under each label is
                 the explanation a bare segmented control would hide in a tooltip. -->
            <div class="grid grid-cols-3 gap-2" role="group" aria-label="Answer type">
              {#each QUESTION_TYPES as t (t.id)}
                {@const Icon = TYPE_ICON[t.id]}
                {@const on = q.type === t.id}
                <button
                  aria-pressed={on}
                  onclick={() => (q.type = t.id)}
                  disabled={running}
                  class="text-left rounded-xl border px-3 py-2.5 flex flex-col gap-1 transition-colors disabled:opacity-60 {on
                    ? 'border-primary bg-primary/10 ring-1 ring-primary'
                    : 'border-card-border bg-background hover:bg-secondary cursor-pointer'}"
                >
                  <span class="flex items-center gap-1.5 text-[0.8125rem] font-medium {on ? 'text-txtmain' : 'text-txtsecondary'}">
                    <Icon class="w-4 h-4 {on ? 'text-primary' : ''}" />
                    {t.label}
                  </span>
                  <span class="text-micro text-txtsecondary leading-snug">{t.hint}</span>
                </button>
              {/each}
            </div>

            {#if q.type === "choice"}
              <div class="flex items-baseline justify-between">
                <span class="text-[0.75rem] font-medium text-txtmain">Options</span>
                <span class="text-micro text-txtsecondary">at least {MIN_CHOICES}, description optional</span>
              </div>
              <div class="flex flex-col gap-1.5">
                {#each q.choices as c, ci (ci)}
                  <div class="flex items-center gap-2">
                    {@render badge(String.fromCharCode(65 + (ci % 26)))}
                    <input class="{field} !w-40 font-mono" placeholder="option" aria-label="Option name" spellcheck="false" bind:value={c.option} disabled={running} />
                    <input class="{field} flex-1 min-w-0" placeholder="description (optional)" aria-label="Option description" bind:value={c.description} disabled={running} />
                    <button class="icon-btn hover:!text-error" onclick={() => removeChoice(q, ci)} disabled={running || q.choices.length <= MIN_CHOICES} aria-label="Remove option">
                      <X class="w-4 h-4" />
                    </button>
                  </div>
                {/each}
              </div>
              <button class={addRow} onclick={() => addChoice(q)} disabled={running}>
                <Plus class="w-3.5 h-3.5" /> Add option
              </button>
            {:else if q.type === "score"}
              <div class="flex items-baseline justify-between">
                <span class="text-[0.75rem] font-medium text-txtmain">Levels</span>
                <span class="text-micro text-txtsecondary">lowest first, {MIN_LEVELS} to {MAX_LEVELS}</span>
              </div>
              <div class="flex flex-col gap-1.5">
                {#each q.levels as _, li (li)}
                  <div class="flex items-center gap-2">
                    {@render badge(String(li))}
                    <input class="{field} flex-1 min-w-0" placeholder="what this level means" aria-label="Level {li} description" bind:value={q.levels[li]} disabled={running} />
                    <button class="icon-btn" onclick={() => moveLevel(q, li, -1)} disabled={running || li === 0} aria-label="Move level up">
                      <ChevronUp class="w-4 h-4" />
                    </button>
                    <button class="icon-btn" onclick={() => moveLevel(q, li, 1)} disabled={running || li === q.levels.length - 1} aria-label="Move level down">
                      <ChevronDown class="w-4 h-4" />
                    </button>
                    <button class="icon-btn hover:!text-error" onclick={() => removeLevel(q, li)} disabled={running || q.levels.length <= MIN_LEVELS} aria-label="Remove level">
                      <X class="w-4 h-4" />
                    </button>
                  </div>
                {/each}
              </div>
              <button class={addRow} onclick={() => addLevel(q)} disabled={running || q.levels.length >= MAX_LEVELS}>
                <Plus class="w-3.5 h-3.5" /> Add level
              </button>
            {:else}
              <div class="flex items-baseline justify-between">
                <span class="text-[0.75rem] font-medium text-txtmain">Meaning</span>
                <span class="text-micro text-txtsecondary">optional</span>
              </div>
              <div class="flex flex-col gap-1.5">
                <div class="flex items-center gap-2">
                  {@render badge("T", "bg-success/15 text-success")}
                  <input class="{field} flex-1 min-w-0" placeholder="true means..." aria-label="True description" bind:value={q.trueDesc} disabled={running} />
                </div>
                <div class="flex items-center gap-2">
                  {@render badge("F", "bg-error/15 text-error")}
                  <input class="{field} flex-1 min-w-0" placeholder="false means..." aria-label="False description" bind:value={q.falseDesc} disabled={running} />
                </div>
              </div>
            {/if}
          </div>
        </section>
      </div>

      {#if error}
        <div class="shrink-0 mx-1 rounded-lg border border-error/40 bg-error/10 px-3 py-2 text-error text-xs whitespace-pre-wrap break-words" role="alert">{error}</div>
      {/if}

      <!-- Model picker + Run in the composer shell, matching the other tabs. -->
      <div class="composer-shell shrink-0 mx-1">
        <div class="flex items-center justify-center gap-2">
          {#if hasModels}
            <ModelSelector bind:value={$selectedModelStore} placeholder="Select a decision model…" disabled={running} category="decision" ghost dropUp />
          {:else}
            <span class="px-2 text-[0.8125rem] text-txtsecondary truncate" use:tip={"Download one from Browse > Decision"}>No decision model</span>
          {/if}
          <div class="flex-1"></div>
          {#if running}
            <button class="btn btn--sm btn--danger-outline inline-flex items-center gap-1.5" onclick={() => abort?.abort()}>
              <Square class="w-3 h-3" fill="currentColor" /> Cancel
            </button>
          {:else}
            <button class="btn btn--sm btn--primary inline-flex items-center gap-1.5" onclick={run} disabled={!hasModels} use:tip={"Run (Ctrl+Enter)"}>
              <Play class="w-3 h-3" fill="currentColor" /> Run
            </button>
          {/if}
        </div>
      </div>
    </div>

    <!-- RIGHT (40%): the answer, in one panel ----------------------------------- -->
    <div class="{card} flex-1 min-w-0 flex flex-col min-h-0 overflow-hidden">
      <div class="shrink-0 flex items-center gap-2 px-4 h-11 border-b border-card-border">
        <Split class="w-4 h-4 text-primary" />
        <span class="text-[0.8125rem] font-medium text-txtmain">Answer</span>
        <div class="flex-1"></div>
        {#if !running && answerList[0]}
          {@const a = answerList[0][1]}
          <span class="text-micro uppercase tracking-wide text-txtsecondary">{TYPE_LABEL[a.type] ?? a.type}</span>
          {#if a.type !== "noul" && typeof a.confidence === "number"}
            <span
              class="inline-flex items-center gap-1.5 rounded-full bg-secondary px-2 py-0.5 text-micro text-txtmain tabular-nums"
              use:tip={"How sure the model is of its pick"}
            >
              <span class="w-8 h-1 rounded-full bg-txtsecondary/25 overflow-hidden">
                <span class="block h-full bg-primary" style="width: {Math.max(0, Math.min(100, a.confidence * 100))}%"></span>
              </span>
              {pct(a.confidence)} sure
            </span>
          {/if}
        {/if}
      </div>

      <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
        {#if running}
          <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
            <div class="inline-block w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin"></div>
            <p class="reason-shimmer-white text-[0.8125rem]">Deciding…</p>
          </div>
        {:else if !result}
          <!-- A ghost of what an answer looks like, so the empty panel says what it is for -->
          <div class="h-full flex flex-col items-center justify-center gap-5 px-8 text-center">
            <div class="w-full max-w-xs flex flex-col gap-2.5 opacity-40" aria-hidden="true">
              {#each [0.72, 0.2, 0.08] as p, i (i)}
                <div class="flex items-center gap-2">
                  <span class="w-14 h-2 rounded-full bg-txtsecondary/40"></span>
                  {@render bar(p, i === 0)}
                </div>
              {/each}
            </div>
            <p class="text-[0.8125rem] text-txtsecondary max-w-xs leading-relaxed">
              The answer appears here as probabilities. Write a prompt, ask a question and press <span class="text-txtmain font-medium">Run</span>.
            </p>
          </div>
        {:else}
          {#each answerList as [id, a] (id)}
            {#if askedWith[id]}
              <p class="px-5 pt-4 text-[0.8125rem] text-txtsecondary leading-snug line-clamp-3">{askedWith[id]}</p>
            {/if}

            {#if a.type === "choice"}
              <div class="px-5 pt-3 pb-4 flex items-end justify-between gap-3">
                <span class="text-3xl font-semibold text-txtmain truncate" use:tip={a.choice}>{a.choice}</span>
                <span class="shrink-0 font-mono text-2xl text-primary tabular-nums">{pct(a.probabilities?.[a.choice])}</span>
              </div>
              <div class="border-t border-card-border px-5 py-4 flex flex-col gap-2.5">
                {#each sortedProbs(a.probabilities) as [opt, p] (opt)}
                  {@const winner = opt === a.choice}
                  <div class="flex items-center gap-3">
                    <span class="w-28 shrink-0 truncate text-[0.8125rem] {winner ? 'text-txtmain font-medium' : 'text-txtsecondary'}" use:tip={opt}>{opt}</span>
                    {@render bar(p, winner)}
                    <span class="w-14 shrink-0 text-right font-mono text-[0.75rem] tabular-nums {winner ? 'text-txtmain' : 'text-txtsecondary'}">{pct(p, 1)}</span>
                  </div>
                {/each}
              </div>
            {:else if a.type === "score"}
              {@const rows = levelProbs(a)}
              {@const top = rows.reduce((m, r) => (r.p > m ? r.p : m), 0)}
              {@const near = rows.find((r) => r.level === Math.round(a.score))}
              <div class="px-5 pt-3 pb-4 flex flex-col gap-3">
                <div class="flex items-end justify-between gap-3">
                  <span class="text-3xl font-semibold text-txtmain truncate">{near?.label || `Level ${Math.round(a.score)}`}</span>
                  <span class="shrink-0 font-mono text-2xl text-primary tabular-nums">
                    {a.score.toFixed(2)}<span class="text-[0.8125rem] text-txtsecondary">&nbsp;/ {Math.max(0, rows.length - 1)}</span>
                  </span>
                </div>
                <div class="relative h-5">
                  <div class="absolute inset-x-0 top-1/2 -translate-y-1/2 h-1.5 rounded-full bg-secondary"></div>
                  <div class="absolute left-0 top-1/2 -translate-y-1/2 h-1.5 rounded-full bg-primary/40" style="width: {scorePercent(a.score, rows.length)}%"></div>
                  {#each rows as r (r.level)}
                    <span
                      class="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-1.5 h-1.5 rounded-full bg-txtsecondary/50"
                      style="left: {scorePercent(r.level, rows.length)}%"
                    ></span>
                  {/each}
                  <span
                    class="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-3.5 h-3.5 rounded-full bg-primary ring-2 ring-surface"
                    style="left: {scorePercent(a.score, rows.length)}%"
                    use:tip={`Expected score ${a.score.toFixed(2)}`}
                  ></span>
                </div>
              </div>
              <div class="border-t border-card-border px-5 py-4 flex flex-col gap-2.5">
                {#each rows as r (r.level)}
                  {@const winner = r.p === top && top > 0}
                  <div class="flex items-center gap-3">
                    {@render badge(String(r.level), winner ? "bg-primary text-btn-primary-text" : "bg-secondary text-txtsecondary")}
                    <span class="w-24 shrink-0 truncate text-[0.8125rem] {winner ? 'text-txtmain font-medium' : 'text-txtsecondary'}" use:tip={r.label}>{r.label}</span>
                    {@render bar(r.p, winner)}
                    <span class="w-14 shrink-0 text-right font-mono text-[0.75rem] tabular-nums {winner ? 'text-txtmain' : 'text-txtsecondary'}">{pct(r.p, 1)}</span>
                  </div>
                {/each}
              </div>
            {:else if a.type === "noul"}
              {@const yes = a.noul >= 0.5}
              <div class="px-5 pt-3 pb-5 flex flex-col gap-4">
                <div class="flex items-end justify-between gap-3">
                  <span class="text-3xl font-semibold {yes ? 'text-success' : 'text-error'}">{yes ? "True" : "False"}</span>
                  <span class="shrink-0 font-mono text-2xl text-txtmain tabular-nums">{pct(yes ? a.noul : 1 - a.noul)}</span>
                </div>
                <div class="flex flex-col gap-1.5">
                  <div class="h-3 rounded-full bg-error/25 overflow-hidden">
                    <div class="h-full rounded-full bg-success transition-[width] duration-500 ease-out" style="width: {Math.max(0, Math.min(100, a.noul * 100))}%"></div>
                  </div>
                  <div class="flex justify-between text-micro text-txtsecondary tabular-nums">
                    <span>true {pct(a.noul, 1)}</span>
                    <span>false {pct(1 - a.noul, 1)}</span>
                  </div>
                </div>
              </div>
            {:else}
              <pre class="px-5 py-4 text-micro text-txtsecondary whitespace-pre-wrap break-words">{JSON.stringify(a, null, 2)}</pre>
            {/if}
          {/each}
        {/if}
      </div>

      {#if result?.usage && !running}
        <div class="shrink-0 flex items-center gap-2 px-4 py-2 border-t border-card-border text-[0.6875rem] text-txtsecondary tabular-nums">
          <span>{result.usage.input_tokens ?? 0} tokens in</span>
          <span>·</span>
          <span>{result.usage.output_tokens ?? 0} out</span>
          {#if result.model}<span class="ml-auto font-mono truncate">{result.model}</span>{/if}
        </div>
      {/if}
    </div>
  </div>
</div>
