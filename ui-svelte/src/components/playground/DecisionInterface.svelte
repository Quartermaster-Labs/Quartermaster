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
    nextQuestionId,
    normalizeDrafts,
    pct,
    readAsDataUrl,
    runDecision,
    scorePercent,
    sortedProbs,
    validate,
    type Answer,
    type DraftQuestion,
    type QuestionType,
    type SystemOneResponse,
  } from "../../lib/decisionApi";
  import ModelSelector from "./ModelSelector.svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import { Split, Play, Square, Plus, Trash2, X, ChevronUp, ChevronDown, Paperclip, FileAudio } from "lucide-svelte";

  // Decision studio. A decision model does not chat: it is shown a STATE (text,
  // JSON, images) and asked QUESTIONS, and answers each with a probability
  // distribution (POST /v1/systemone). Left column is the request: state,
  // attachments, the question builder, and the model + Run pinned below. Right
  // column is the answer, one card per question. Nothing is kept per run: the
  // question set and state text are per-user prefs so a tuned set survives a
  // reload, and the answers are only on screen.

  const selectedModelStore = userPref<string>("playground-decision-model", "");
  const stateStore = userPref<string>("playground-decision-state", "");
  const questionsStore = userPref<DraftQuestion[]>("playground-decision-questions", []);

  let stateText = $state(get(stateStore));
  let questions = $state<DraftQuestion[]>(initialQuestions());
  function initialQuestions(): DraftQuestion[] {
    const saved = normalizeDrafts(get(questionsStore));
    return saved.length ? saved : [newQuestion("q1")];
  }

  // Prefs save debounced, so writing on every keystroke is cheap. snapshot()
  // reads the whole tree, which is what makes the effect track nested edits.
  $effect(() => {
    stateStore.set(stateText);
  });
  $effect(() => {
    questionsStore.set($state.snapshot(questions));
  });

  // The picker hard-filters to decision models and renders nothing when none
  // exist, so gate on that list here too.
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

  // --- question builder -----------------------------------------------------
  function addQuestion() {
    questions = [...questions, newQuestion(nextQuestionId(questions))];
  }
  function removeQuestion(uid: string) {
    questions = questions.filter((q) => q.uid !== uid);
  }
  function setType(q: DraftQuestion, type: QuestionType) {
    q.type = type;
  }
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
    if (running) return;
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
    for (const q of questions) asked[q.id.trim()] = q.instructions.trim();
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

  let answerList = $derived(result ? (Object.entries(result.answers) as [string, Answer][]) : []);

  const field =
    "w-full px-2.5 py-1.5 rounded-md border border-card-border bg-background text-[0.8125rem] focus:outline-none focus:ring-2 focus:ring-primary/40";
</script>

{#snippet bar(p: number, winner: boolean)}
  <div class="flex-1 min-w-0 h-2 rounded-full bg-secondary overflow-hidden">
    <div
      class="h-full rounded-full transition-[width] duration-300 {winner ? 'bg-primary' : 'bg-txtsecondary/40'}"
      style="width: {Math.max(0, Math.min(100, p * 100))}%"
    ></div>
  </div>
{/snippet}

{#snippet confidence(c: number | undefined)}
  {#if typeof c === "number"}
    <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums" use:tip={"How sure the model is of its pick"}>
      confidence {pct(c)}
    </span>
  {/if}
{/snippet}

<div class="flex flex-col h-full">
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary text-center px-6">
      <Split class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No decision model configured. Add a decision model (served on /v1/systemone) to ask it questions about a situation.</p>
    </div>
  {:else}
    <div class="flex-1 flex flex-col md:flex-row gap-4 min-h-0 w-full py-4">
      <!-- LEFT (60%): the request, with model + Run pinned below ---------------- -->
      <div class="w-full md:w-3/5 shrink-0 flex flex-col gap-3 min-h-0">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b flex flex-col gap-4 px-1 pb-4" use:scrollFade>
          <!-- State -->
          <section class="flex flex-col gap-1.5">
            <label for="decision-state" class="text-xs uppercase tracking-wide text-txtsecondary">State</label>
            <textarea
              id="decision-state"
              class="{field} resize-y min-h-[6rem] leading-relaxed"
              rows="6"
              placeholder={'What is going on. Plain text, or a JSON object / array (sent as structured data).'}
              bind:value={stateText}
              onkeydown={onStateKey}
              disabled={running}
            ></textarea>
          </section>

          <!-- Attachments: only for models that read images or audio -->
          {#if canAttach}
            <section class="flex flex-col gap-1.5">
              <div class="flex items-center justify-between">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">
                  Files <span class="normal-case tracking-normal">({takesImages ? "images" : ""}{takesImages && takesAudio ? " and " : ""}{takesAudio ? "audio" : ""}, up to {MAX_FILES})</span>
                </span>
                <button class="btn btn--sm inline-flex items-center gap-1" onclick={() => fileInput?.click()} disabled={running || attached.length >= MAX_FILES}>
                  <Paperclip class="w-3 h-3" /> Attach
                </button>
              </div>
              <div
                role="region"
                aria-label="File drop zone"
                class="rounded-md border border-dashed p-2 flex flex-wrap gap-2 min-h-[3rem] items-center transition-colors {isDragging
                  ? 'border-primary bg-primary/10'
                  : 'border-card-border bg-surface'}"
                ondragover={(e) => { e.preventDefault(); isDragging = true; }}
                ondragleave={() => (isDragging = false)}
                ondrop={onDrop}
              >
                {#each attached as a (a.id)}
                  <div class="relative group">
                    {#if a.kind === "image"}
                      <img src={a.url} alt={a.name} class="h-16 w-16 object-cover rounded border border-card-border" />
                    {:else}
                      <div class="h-16 px-3 flex items-center gap-1.5 rounded border border-card-border bg-background text-[0.6875rem] text-txtsecondary">
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

          <!-- Questions -->
          <section class="flex flex-col gap-2">
            <div class="flex items-center justify-between">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Questions</span>
              <button class="btn btn--sm inline-flex items-center gap-1" onclick={addQuestion} disabled={running}>
                <Plus class="w-3 h-3" /> Add question
              </button>
            </div>

            {#each questions as q, qi (q.uid)}
              <div class="rounded-xl border border-card-border bg-surface p-3 flex flex-col gap-2.5">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="font-mono text-micro text-txtsecondary tabular-nums">{qi + 1}</span>
                  <input
                    class="{field} !w-40 font-mono"
                    placeholder="question id"
                    aria-label="Question id"
                    spellcheck="false"
                    bind:value={q.id}
                    disabled={running}
                  />
                  <div class="seg">
                    {#each QUESTION_TYPES as t (t.id)}
                      <button aria-pressed={q.type === t.id} onclick={() => setType(q, t.id)} disabled={running} use:tip={t.hint}>{t.label}</button>
                    {/each}
                  </div>
                  <button
                    class="ml-auto icon-btn hover:!text-error"
                    onclick={() => removeQuestion(q.uid)}
                    disabled={running}
                    use:tip={"Remove question"}
                    aria-label="Remove question"
                  >
                    <Trash2 class="w-4 h-4" />
                  </button>
                </div>

                <textarea
                  class="{field} resize-y leading-relaxed"
                  rows="2"
                  placeholder="Instructions: what should the model decide? (plain text, or JSON)"
                  aria-label="Instructions"
                  bind:value={q.instructions}
                  disabled={running}
                ></textarea>

                {#if q.type === "choice"}
                  <div class="flex flex-col gap-1.5">
                    <span class="text-micro uppercase tracking-wide text-txtsecondary">Options (at least {MIN_CHOICES}; a description is optional)</span>
                    {#each q.choices as c, ci (ci)}
                      <div class="flex items-center gap-1.5">
                        <input class="{field} !w-40 font-mono" placeholder="option" aria-label="Option name" spellcheck="false" bind:value={c.option} disabled={running} />
                        <input class="{field} flex-1 min-w-0" placeholder="description (optional)" aria-label="Option description" bind:value={c.description} disabled={running} />
                        <button class="icon-btn hover:!text-error" onclick={() => removeChoice(q, ci)} disabled={running || q.choices.length <= MIN_CHOICES} aria-label="Remove option">
                          <X class="w-4 h-4" />
                        </button>
                      </div>
                    {/each}
                    <button class="self-start text-[0.75rem] text-primary hover:underline inline-flex items-center gap-1" onclick={() => addChoice(q)} disabled={running}>
                      <Plus class="w-3 h-3" /> Add option
                    </button>
                  </div>
                {:else if q.type === "score"}
                  <div class="flex flex-col gap-1.5">
                    <span class="text-micro uppercase tracking-wide text-txtsecondary">Levels, lowest first ({MIN_LEVELS} to {MAX_LEVELS})</span>
                    {#each q.levels as _, li (li)}
                      <div class="flex items-center gap-1.5">
                        <span class="shrink-0 w-5 text-right font-mono text-micro text-txtsecondary tabular-nums">{li}</span>
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
                    <button
                      class="self-start text-[0.75rem] text-primary hover:underline inline-flex items-center gap-1 disabled:opacity-40 disabled:no-underline"
                      onclick={() => addLevel(q)}
                      disabled={running || q.levels.length >= MAX_LEVELS}
                    >
                      <Plus class="w-3 h-3" /> Add level
                    </button>
                  </div>
                {:else}
                  <div class="flex flex-col gap-1.5">
                    <span class="text-micro uppercase tracking-wide text-txtsecondary">What true and false mean (optional)</span>
                    <input class="{field}" placeholder="true means..." aria-label="True description" bind:value={q.trueDesc} disabled={running} />
                    <input class="{field}" placeholder="false means..." aria-label="False description" bind:value={q.falseDesc} disabled={running} />
                  </div>
                {/if}
              </div>
            {/each}

            {#if questions.length === 0}
              <p class="text-[0.8125rem] text-txtsecondary px-1">No questions yet. Add one to ask the model something.</p>
            {/if}
          </section>
        </div>

        {#if error}
          <div class="shrink-0 text-error text-xs px-1 whitespace-pre-wrap break-words" role="alert">{error}</div>
        {/if}

        <!-- Model picker + Run in the composer shell, matching the other tabs. -->
        <div class="composer-shell shrink-0">
          <div class="flex items-center justify-center gap-2">
            <ModelSelector bind:value={$selectedModelStore} placeholder="Select a decision model…" disabled={running} category="decision" ghost dropUp />
            <div class="flex-1"></div>
            {#if running}
              <button class="btn btn--sm btn--danger-outline inline-flex items-center gap-1.5" onclick={() => abort?.abort()}>
                <Square class="w-3 h-3" fill="currentColor" /> Cancel
              </button>
            {:else}
              <button class="btn btn--sm btn--primary inline-flex items-center gap-1.5" onclick={run} use:tip={"Run (Ctrl+Enter in the state box)"}>
                <Play class="w-3 h-3" fill="currentColor" /> Run
              </button>
            {/if}
          </div>
        </div>
      </div>

      <!-- RIGHT (40%): answers ---------------------------------------------------- -->
      <div class="flex-1 min-w-0 flex flex-col min-h-0">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
          {#if running}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
              <div class="inline-block w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin"></div>
              <p class="reason-shimmer-white text-[0.8125rem]">Deciding…</p>
            </div>
          {:else if !result}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center px-4">
              <Split class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Answers appear here as probabilities. Describe a state, add questions and press Run.</p>
            </div>
          {:else}
            <div class="flex flex-col gap-3 pb-6 px-1">
              {#each answerList as [id, a] (id)}
                <div class="rounded-xl border border-card-border bg-surface p-3 flex flex-col gap-2">
                  <div class="flex items-baseline gap-2 min-w-0">
                    <span class="font-mono text-[0.8125rem] text-txtmain truncate">{id}</span>
                    <span class="shrink-0 text-micro uppercase tracking-wide text-txtsecondary">{a.type}</span>
                    <div class="flex-1"></div>
                    {#if a.type !== "noul"}{@render confidence(a.confidence)}{/if}
                  </div>
                  {#if askedWith[id]}
                    <p class="text-[0.75rem] text-txtsecondary leading-snug line-clamp-3">{askedWith[id]}</p>
                  {/if}

                  {#if a.type === "choice"}
                    <div class="flex flex-col gap-1.5">
                      {#each sortedProbs(a.probabilities) as [opt, p] (opt)}
                        {@const winner = opt === a.choice}
                        <div class="flex items-center gap-2">
                          <span class="w-28 shrink-0 truncate text-[0.8125rem] {winner ? 'text-txtmain font-medium' : 'text-txtsecondary'}" use:tip={opt}>{opt}</span>
                          {@render bar(p, winner)}
                          <span class="w-12 shrink-0 text-right font-mono text-micro tabular-nums {winner ? 'text-txtmain' : 'text-txtsecondary'}">{pct(p, 1)}</span>
                        </div>
                      {/each}
                    </div>
                  {:else if a.type === "score"}
                    {@const rows = levelProbs(a)}
                    {@const top = rows.reduce((m, r) => (r.p > m ? r.p : m), 0)}
                    <div class="flex items-center gap-3">
                      <span class="font-mono text-lg leading-none text-txtmain tabular-nums">{a.score.toFixed(2)}</span>
                      <div class="relative flex-1 h-5">
                        <div class="absolute inset-x-0 top-1/2 h-px bg-card-border"></div>
                        {#each rows as r (r.level)}
                          <span
                            class="absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-1.5 h-1.5 rounded-full bg-txtsecondary/50"
                            style="left: {scorePercent(r.level, rows.length)}%"
                          ></span>
                        {/each}
                        <span
                          class="absolute top-0 bottom-0 -translate-x-1/2 w-0.5 rounded bg-primary"
                          style="left: {scorePercent(a.score, rows.length)}%"
                          use:tip={`Expected score ${a.score.toFixed(2)}`}
                        ></span>
                      </div>
                    </div>
                    <div class="flex flex-col gap-1.5">
                      {#each rows as r (r.level)}
                        <div class="flex items-center gap-2">
                          <span class="w-5 shrink-0 text-right font-mono text-micro text-txtsecondary tabular-nums">{r.level}</span>
                          <span class="w-28 shrink-0 truncate text-[0.8125rem] text-txtsecondary" use:tip={r.label}>{r.label}</span>
                          {@render bar(r.p, r.p === top && top > 0)}
                          <span class="w-12 shrink-0 text-right font-mono text-micro tabular-nums text-txtsecondary">{pct(r.p, 1)}</span>
                        </div>
                      {/each}
                    </div>
                  {:else if a.type === "noul"}
                    <div class="flex flex-col gap-1">
                      <div class="flex items-baseline justify-between text-[0.8125rem]">
                        <span class="text-txtsecondary">false</span>
                        <span class="font-mono text-base text-txtmain tabular-nums">{pct(a.noul, 1)} <span class="text-micro text-txtsecondary">true</span></span>
                      </div>
                      <div class="h-2.5 rounded-full bg-secondary overflow-hidden">
                        <div class="h-full rounded-full bg-primary transition-[width] duration-300" style="width: {Math.max(0, Math.min(100, a.noul * 100))}%"></div>
                      </div>
                    </div>
                  {:else}
                    <pre class="text-micro text-txtsecondary whitespace-pre-wrap break-words">{JSON.stringify(a, null, 2)}</pre>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>

        {#if result?.usage}
          <div class="shrink-0 flex items-center gap-2 pt-3 mt-2 border-t border-card-border text-[0.6875rem] uppercase tracking-wide text-txtsecondary">
            <span class="tabular-nums">{result.usage.input_tokens ?? 0} in</span>
            <span>·</span>
            <span class="tabular-nums">{result.usage.output_tokens ?? 0} out</span>
            <span class="normal-case tracking-normal">tokens</span>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>
