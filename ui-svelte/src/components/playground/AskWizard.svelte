<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { Send, ChevronLeft, ChevronRight, Check } from "lucide-svelte";
  import { fly } from "svelte/transition";
  import { tick } from "svelte";
  import { composeAskAnswer, isOtherOption, type AskQuestion } from "../../lib/askBlock";
  import { fetchFxRate, convertMoneyLabel, type FxRate } from "../../lib/currency";

  // Renders a model's ```ask block (see lib/askBlock.ts) as a one-question-at-a-time
  // wizard: options stacked vertically, click to answer, step through. Sending is
  // an ordinary user message — the model sees prose, not a protocol.
  let {
    questions,
    onSubmit,
    disabled = false,
  }: { questions: AskQuestion[]; onSubmit: (text: string) => void; disabled?: boolean } = $props();

  let step = $state(0);
  let dir = $state(1); // travel direction, so the slide matches Back vs Next
  let answers = $state<Record<string, string[]>>({});
  let other = $state<Record<string, string>>({});
  let sent = $state(false);
  let otherInput = $state<HTMLInputElement | null>(null);

  const q = $derived(questions[Math.min(step, questions.length - 1)]);
  const isLast = $derived(step >= questions.length - 1);

  // An "Other" option is picked → the free-text field is the actual answer, so it
  // has to be visible even when the model did not set allowOther.
  const otherPicked = $derived((answers[q.id] ?? []).some(isOtherOption));
  // Answers already given carry forward: a currency chosen in step 1 has to be
  // visible when step 2 asks for a budget, or the number is denominated in
  // whatever the user happened to assume.
  const CURRENCY_SIGNS: Record<string, string> = { "€": "EUR", $: "USD", "£": "GBP", "¥": "JPY", "₹": "INR", "₽": "RUB", "₺": "TRY", "лв": "BGN", zł: "PLN", Kč: "CZK" };
  function currencyCode(text: string): string {
    const s = text.trim();
    const paren = /\(([A-Za-z]{3})\)/.exec(s); // "Romanian leu (RON)"
    if (paren) return paren[1].toUpperCase();
    const code = /\b([A-Z]{3})\b/.exec(s.toUpperCase());
    if (code && code[1] !== "THE") return code[1];
    for (const [sign, iso] of Object.entries(CURRENCY_SIGNS)) if (s.includes(sign)) return iso;
    return "";
  }
  // currencyOf reads whatever a currency question has been answered with, from
  // chips or from the free-text field.
  function currencyOf(item: AskQuestion): string {
    if (!/currenc|\bpay in\b/i.test(item.label + " " + item.id)) return "";
    const vals = [...(answers[item.id] ?? []).filter((v) => !isOtherOption(v)), other[item.id] ?? ""];
    for (const v of vals) {
      const c = currencyCode(v);
      if (c) return c;
    }
    return "";
  }
  const chosenCurrency = $derived.by(() => {
    for (const item of questions) {
      if (item.id === q.id) break; // only what was asked BEFORE this step
      const c = currencyOf(item);
      if (c) return c;
    }
    return "";
  });
  const isMoneyQuestion = $derived(/price|budget|cost|spend|afford|pay/i.test(q.label));
  const currencyHint = $derived(isMoneyQuestion ? chosenCurrency : "");
  // Options were written before the currency was known, so their amounts may be
  // in a different one. Never a rate guessed here — that is the failure the
  // convert_currency tool exists to prevent.
  const optionCurrencyClash = $derived.by(() => {
    if (!isMoneyQuestion || !chosenCurrency) return "";
    for (const opt of q.options) {
      const c = currencyCode(opt);
      if (c && c !== chosenCurrency) return c;
    }
    return "";
  });

  // Rates are fetched the moment the currency is ANSWERED, not when the budget
  // step is reached — waiting until then puts a live upstream round trip in front
  // of the user, which is the several-second stall. By the time they click Next
  // the pair is usually already in hand. Keyed "FROM>TO"; a null value means the
  // lookup was tried and failed.
  let rates = $state<Record<string, FxRate | null>>({});
  const rateKey = $derived(optionCurrencyClash ? `${optionCurrencyClash}>${chosenCurrency}` : "");
  const fxRate = $derived(rateKey ? (rates[rateKey] ?? null) : null);
  const fxTried = $derived(!!rateKey && rateKey in rates);

  // Every pair a later money question could need, given the currency so far.
  const neededPairs = $derived.by(() => {
    let to = "";
    const pairs: string[] = [];
    for (const item of questions) {
      const c = currencyOf(item);
      if (c) {
        to = c;
        continue;
      }
      if (!to || !/price|budget|cost|spend|afford|pay/i.test(item.label)) continue;
      for (const opt of item.options) {
        const from = currencyCode(opt);
        if (from && from !== to && !pairs.includes(`${from}>${to}`)) pairs.push(`${from}>${to}`);
      }
    }
    return pairs;
  });

  $effect(() => {
    const want = neededPairs.filter((k) => !(k in rates));
    if (!want.length) return;
    // Debounced: the currency can come from the free-text field, and firing per
    // keystroke would request DKK as "D", then "DK".
    const t = setTimeout(() => {
      for (const key of want) {
        const [from, to] = key.split(">");
        fetchFxRate(from, to).then((r) => {
          rates = { ...rates, [key]: r };
        });
      }
    }, 300);
    return () => clearTimeout(t);
  });

  // The displayed options: converted when a rate is in hand, otherwise the
  // originals. An option with no amount in it ("No fixed budget") is a real
  // choice and passes through unconverted.
  const shownOptions = $derived.by(() => {
    if (!optionCurrencyClash || !fxRate) return q.options.map((o) => ({ label: o, value: o }));
    return q.options.map((o) => {
      const conv = convertMoneyLabel(o, fxRate!.rate, optionCurrencyClash, chosenCurrency);
      // The value carries BOTH: the model needs the figure in the user's own
      // currency to search on, and the original so it can see the bracket came
      // from its own list rather than from the user quoting a converted number.
      // No "≈" on the chip: a bracket is already a range, so the tilde adds a
      // hedge to something nobody was going to read as exact. The caption below
      // carries the rate and the word "approximate" once, for all of them.
      return conv ? { label: conv, value: `${conv} (converted from ${o})` } : { label: o, value: o };
    });
  });

  // The free-text field stands in for the options only once the rate lookup has
  // FAILED — while it is in flight the skeletons say the list is coming, and
  // swapping a text field in and back out again would just be a flicker.
  const showOther = $derived(q.allowOther || otherPicked || (!!optionCurrencyClash && fxTried && !fxRate));

  function picked(opt: string): boolean {
    return (answers[q.id] ?? []).includes(opt);
  }

  async function focusOther() {
    await tick(); // the field may only exist because of the click that got us here
    otherInput?.focus();
  }

  function choose(opt: string) {
    const cur = answers[q.id] ?? [];
    if (q.type === "multi") {
      const on = !cur.includes(opt);
      answers = { ...answers, [q.id]: on ? [...cur, opt] : cur.filter((v) => v !== opt) };
      if (on && isOtherOption(opt)) focusOther();
      return; // multi needs an explicit Next — the user isn't done yet
    }
    const on = !cur.includes(opt);
    answers = { ...answers, [q.id]: on ? [opt] : [] };
    if (!on) return;
    if (isOtherOption(opt)) {
      // "Other" is not an answer, it is a request to type one. Advancing here is
      // the bug: it sends a question answered with the word "Other".
      focusOther();
      return;
    }
    // Moving off "Other" to a real option drops text that only existed for it.
    if (!q.allowOther && cur.some(isOtherOption)) other = { ...other, [q.id]: "" };
    // Single choice is unambiguous, so answering IS advancing.
    next();
  }

  function go(to: number) {
    dir = to > step ? 1 : -1;
    step = to;
  }

  function next() {
    if (isLast) submit();
    else go(step + 1);
  }

  function submit() {
    if (sent || disabled) return;
    // Free text is an answer like any other; merge it in at send time so typing
    // and clicking can be combined on the same question.
    const merged: Record<string, string[]> = {};
    for (const item of questions) {
      let extra = (other[item.id] ?? "").trim();
      const chips = answers[item.id] ?? [];
      // A typed answer replaces the "Other" chip that opened the field — sending
      // both would read as two answers, one of which says nothing. With nothing
      // typed the chip stays: "none of these fit" is still information.
      const kept = extra ? chips.filter((v) => !isOtherOption(v)) : chips;
      if (extra && /price|budget|cost|spend|afford|pay/i.test(item.label)) {
        const cur = moneyCurrencyFor(item);
        if (cur && !currencyCode(extra)) extra = `${extra} ${cur}`;
      }
      merged[item.id] = [...kept, ...(extra ? [extra] : [])];
    }
    sent = true;
    onSubmit(composeAskAnswer(questions, merged));
  }

  // Same lookup as chosenCurrency, but for an arbitrary question at send time.
  function moneyCurrencyFor(target: AskQuestion): string {
    for (const item of questions) {
      if (item.id === target.id) break;
      if (!/currenc|\bpay in\b/i.test(item.label + " " + item.id)) continue;
      const vals = [...(answers[item.id] ?? []).filter((v) => !isOtherOption(v)), other[item.id] ?? ""];
      for (const v of vals) {
        const c = currencyCode(v);
        if (c) return c;
      }
    }
    return "";
  }

  function isDone(i: number): boolean {
    const item = questions[i];
    const chips = (answers[item.id] ?? []).filter((v) => !isOtherOption(v));
    return !!(chips.length || (other[item.id] ?? "").trim());
  }
</script>

<div
  class="mt-3 overflow-hidden rounded-xl border border-card-border bg-surface shadow-sm transition-opacity {sent
    ? 'opacity-50 pointer-events-none'
    : ''}"
>
  <!-- Progress rail: the whole card is a form, so how far in you are should be
       readable without counting dots. -->
  <div class="h-0.5 w-full bg-secondary">
    <div class="h-full bg-primary transition-all duration-300" style="width: {((step + 1) / questions.length) * 100}%"></div>
  </div>

  <div class="flex flex-col gap-3 p-3.5">
    <div class="flex items-start justify-between gap-3">
      <span class="text-[0.9375rem] font-medium leading-snug text-txtmain">{q.label}</span>
      <!-- Step dots double as navigation back to anything already answered. -->
      <div class="mt-1.5 flex shrink-0 items-center gap-1">
        {#each questions as item, i (item.id)}
          <button
            type="button"
            class="h-1.5 rounded-full transition-all {i === step
              ? 'w-4 bg-primary'
              : isDone(i)
                ? 'w-1.5 bg-primary/50 hover:bg-primary'
                : 'w-1.5 bg-txtsecondary/25 hover:bg-txtsecondary/50'}"
            use:tip={item.label}
            aria-label={item.label}
            onclick={() => go(i)}
            {disabled}
          ></button>
        {/each}
      </div>
    </div>

    {#key q.id}
      <div class="flex flex-col gap-2" in:fly={{ x: dir * 12, duration: 140 }}>
        {#if optionCurrencyClash}
          <!-- The model wrote these brackets before it knew the currency, so they
               are in ITS currency and mean nothing to this user. Live rate, same
               server cache the convert_currency tool uses — a rate invented here
               would be exactly the failure that tool exists to prevent. If the
               lookup fails the options are dropped rather than shown wrong: the
               text field below is then the whole question. -->
          <p class="text-xs text-txtsecondary">
            {#if fxRate}
              Converted from {optionCurrencyClash} at {fxRate.rate.toLocaleString("en-US", { maximumFractionDigits: 4 })}
              {chosenCurrency}/{optionCurrencyClash}{fxRate.date && fxRate.date !== "n/a" ? `, ${fxRate.date}` : ""} - approximate.
            {:else if fxTried}
              Could not fetch a {optionCurrencyClash}→{chosenCurrency} rate. Enter your budget in {chosenCurrency}.
            {:else}
              <span class="reason-shimmer thinking-dots">Converting to {chosenCurrency}</span>
            {/if}
          </p>
        {/if}
        {#if optionCurrencyClash && !fxTried}
          <!-- Skeletons in the options' own shape and count, so the list does not
               appear out of nowhere and shift the buttons under the cursor. -->
          <div class="flex flex-col gap-1.5">
            {#each q.options as _, i (i)}
              <div class="flex items-center gap-2.5 rounded-lg border border-card-border bg-background/40 px-3 py-2">
                <span class="h-4 w-4 shrink-0 rounded-full bg-txtsecondary/15"></span>
                <span
                  class="h-3 animate-pulse rounded bg-txtsecondary/15"
                  style="width: {45 + ((i * 17) % 35)}%; animation-delay: {i * 90}ms"
                ></span>
              </div>
            {/each}
          </div>
        {/if}
        {#if q.options.length && (!optionCurrencyClash || fxRate)}
          <!-- Vertical list: options are phrases, not tags — side by side they
               wrap into a mush and hide how many there are. -->
          <div class="flex flex-col gap-1.5">
            {#each shownOptions as opt (opt.value)}
              <!-- Hover moves the BACKGROUND only. Primary (orange) is reserved
                   for the picked state: tinting the border on hover too made
                   every row the cursor crossed look half-selected. -->
              <button
                type="button"
                class="group flex items-center gap-2.5 w-full text-left px-3 py-2 rounded-lg border text-sm transition-colors {picked(opt.value)
                  ? 'border-primary bg-primary/10 text-txtmain'
                  : 'border-card-border bg-background/40 text-txtmain hover:bg-secondary'}"
                onclick={() => choose(opt.value)}
                {disabled}
              >
                <!-- Circle = pick one, square = pick any. Shape carries the rule
                     so it doesn't need explaining in words. -->
                <span
                  class="flex h-4 w-4 shrink-0 items-center justify-center border transition-colors {q.type === 'multi'
                    ? 'rounded-[0.25rem]'
                    : 'rounded-full'} {picked(opt.value) ? 'border-primary bg-primary text-white' : 'border-txtsecondary/40 group-hover:border-txtsecondary/70'}"
                >
                  {#if picked(opt.value)}<Check class="h-3 w-3" />{/if}
                </span>
                <span class="min-w-0 flex-1">{opt.label}</span>
              </button>
            {/each}
          </div>
        {/if}

        {#if showOther}
          <!-- Currency prefix: the question two steps back already settled which
               money this is, so the field says so instead of leaving the user to
               retype it (or the model to guess). -->
          <div
            class="flex items-center rounded-lg border bg-background/40 transition-colors focus-within:border-primary {otherPicked
              ? 'border-primary'
              : 'border-card-border'}"
          >
            {#if currencyHint}
              <span class="shrink-0 border-r border-card-border py-2 pl-3 pr-2.5 text-sm font-medium text-txtsecondary">{currencyHint}</span>
            {/if}
            <input
              type="text"
              class="w-full bg-transparent px-3 py-2 text-sm placeholder:text-txtsecondary/60 focus:outline-none"
              placeholder={otherPicked ? "Type your answer…" : q.options.length ? "Something else…" : "Type your answer…"}
              bind:this={otherInput}
              bind:value={other[q.id]}
              onkeydown={(e) => { if (e.key === "Enter") next(); }}
              {disabled}
            />
          </div>
        {/if}
      </div>
    {/key}
  </div>

  <div class="flex items-center justify-between gap-2 border-t border-card-border bg-background/30 px-3 py-2">
    <button
      type="button"
      class="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-txtsecondary transition-colors hover:bg-secondary hover:text-txtmain disabled:opacity-0"
      onclick={() => go(step - 1)}
      disabled={disabled || step === 0}
    >
      <ChevronLeft class="h-3.5 w-3.5" /> Back
    </button>
    <div class="flex items-center gap-1">
      <!-- Skipping is explicit and cheap: an unanswered question is sent as "no
           preference" rather than silently dropped. -->
      <button
        type="button"
        class="rounded-md px-2.5 py-1 text-xs text-txtsecondary transition-colors hover:bg-secondary hover:text-txtmain"
        onclick={next}
        disabled={disabled || sent}
      >
        {isLast ? "Skip & send" : "Skip"}
      </button>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-sm text-btn-primary-text transition-opacity hover:opacity-90 disabled:opacity-50"
        onclick={next}
        disabled={disabled || sent}
      >
        {#if isLast}
          <Send class="h-3.5 w-3.5" /> Send answers
        {:else}
          Next <ChevronRight class="h-3.5 w-3.5" />
        {/if}
      </button>
    </div>
  </div>
</div>
