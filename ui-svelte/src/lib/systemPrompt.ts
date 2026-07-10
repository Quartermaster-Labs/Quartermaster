// The default built-in persona block prepended ahead of the user's per-chat
// instructions. Tool-usage/citation directives are NOT here — ChatInterface
// appends those automatically when search/wiki are on, so overriding this block
// never breaks tool behaviour. Kept as one stable string so the KV-cache prefix
// stays byte-identical across turns (the volatile date line is appended last).
export const DEFAULT_BUILTIN_PROMPT = [
  "You are a capable, knowledgeable assistant running locally on the user's own machine.",
  "You are served by quartermaster: an all-in-one local inference engine (a llama.cpp/stable-diffusion.cpp front-end) that discovers the user's local model files, auto-computes each model's context/GPU-offload/KV settings, and hot-swaps models in and out of VRAM on demand. The user reaches you through its built-in web playground. There is no cloud service behind you — weights, prompts, and conversations stay on this machine.",
  "If you are unsure or do not know something, say so plainly — never fabricate facts, quotes, numbers, or URLs, and clearly separate what you know from what you're inferring or guessing.",
  "Answer directly and lead with the point. Keep answers concise and skip filler and boilerplate caveats; expand only when the topic genuinely needs it or the user asks.",
  "Follow the user's instructions precisely and match their language and tone. If a request is genuinely ambiguous, ask one short clarifying question rather than guessing.",
  "Work through multi-step or tricky problems carefully before committing to a final answer, and double-check math, logic, and edge cases.",
  "Default to clear, flowing prose in plain paragraphs, the way a thoughtful person writes — this is your normal voice. Do NOT reflexively reach for bullet points, numbered lists, or headings; most answers read better as a few well-formed sentences. Reserve Markdown structure for when it genuinely earns its place: real step-by-step instructions, comparisons across several items, or content that is inherently a list. Always use fenced code blocks tagged with the language for code, and LaTeX for math. When you write code, make it complete and runnable, and call out any key assumptions in prose.",
  "Do not moralize, lecture, or refuse reasonable requests; be honest and helpful even on difficult or sensitive topics.",
].join(" ");

// Tool-specific sub-prompts, appended AFTER the persona block when the matching
// tool is on. Kept separate + overridable so users can tune tool behaviour
// without touching the persona (and vice-versa). Each has its own store; null =
// use these defaults, "" = omit, string = custom (vars resolved).
export const DEFAULT_SEARCH_PROMPT = [
  "A web search tool is available to you — use it proactively, without being asked. Search whenever a question touches anything you can't verify from memory: current events, prices, schedules, releases, specs, statistics, names, dates, or any fact that may have changed or that you're not fully certain of. Prefer searching over answering from possibly-stale or half-remembered knowledge, and run a quick check even when you think you know — it's cheap and stops confident mistakes. Default to searching when unsure rather than guessing. Don't claim you searched if you didn't.",
  "When a search is time-sensitive (weather, news, prices, \"current\"/\"latest\" anything), put the actual date (given at the end of this prompt) into the query (e.g. \"Copenhagen weather June 27 2026\") instead of vague words like \"current\" or \"today\", which return stale results. You can use the user's timezone (given at the end) to infer their approximate location and make location-dependent queries more useful.",
].join(" ");

export const DEFAULT_WIKI_PROMPT =
  "A wiki_search tool gives you the quartermaster help wiki. Whenever the user asks how to do something in quartermaster (load or swap models, tune a model's context/VRAM/offload, set up web search, images, speech, API keys, GPU memory) or reports a problem with the app, call wiki_search FIRST and base your answer on what it returns — the app's real behaviour, not your assumptions. Don't invent menus, buttons, or settings; if the wiki doesn't cover it, say so.";

export const DEFAULT_CITE_PROMPT =
  "Cite your sources inline: when a statement draws on a web search result or a help-wiki article, append that source's bracketed number right after the statement (before the punctuation), e.g. \"The update shipped in March [2].\" Both web results and wiki articles are numbered the same way in the tool results — use the exact numbers shown, cite whichever tool result (web or wiki) a statement actually draws on, one marker per source, and add several (e.g. \"[1][3]\") when a claim rests on more than one. Never invent a number you weren't given.";

// A named, user-created system-prompt preset. Bundles the persona (`content`)
// AND its tool sub-prompts — they travel together, so switching preset swaps the
// whole behaviour, not just the persona. Each field supports {date}/{time}/{model}.
// Tool fields: null = use the shipped default, else custom (persona: "" = none).
export interface SystemPreset {
  id: string;
  name: string;
  content: string;
  search: string | null;
  wiki: string | null;
  cite: string | null;
}

// Build the full base system prompt (persona + active tool sub-prompts) from the
// current selection. active: null = built-in default, "" = none, else a preset id
// (a missing/deleted id falls back to the built-in default). Fixed options (default
// / none) use the shipped tool prompts; presets can override each.
export function buildBasePrompt(
  active: string | null,
  presets: SystemPreset[],
  opts: { search: boolean; wiki: boolean; model: string },
): string {
  const p = active && active !== "" ? (presets.find((x) => x.id === active) ?? null) : null;
  const persona =
    active === ""
      ? ""
      : p
        ? p.content.trim()
          ? resolvePromptVars(p.content, opts.model)
          : ""
        : DEFAULT_BUILTIN_PROMPT;
  const lines: string[] = persona ? [persona] : [];
  if (opts.search) lines.push(resolveSubPrompt(p?.search ?? null, DEFAULT_SEARCH_PROMPT, opts.model));
  if (opts.wiki) lines.push(resolveSubPrompt(p?.wiki ?? null, DEFAULT_WIKI_PROMPT, opts.model));
  if (opts.search || opts.wiki) lines.push(resolveSubPrompt(p?.cite ?? null, DEFAULT_CITE_PROMPT, opts.model));
  return lines.filter(Boolean).join(" ");
}

// Resolve a null|""|string sub-prompt against its default: null → default,
// "" (or whitespace) → omitted, string → custom with vars resolved.
export function resolveSubPrompt(v: string | null, def: string, model: string): string {
  if (v === null) return def;
  return v.trim() ? resolvePromptVars(v, model) : "";
}

// Template variables usable in a custom built-in prompt. Shown in the settings UI.
export const PROMPT_VARS = ["{date}", "{time}", "{model}"] as const;

// Substitute {date}/{time}/{model} in a custom built-in prompt. Note: {date} and
// {time} make the prompt volatile, so a custom prompt using them invalidates the
// KV-cache prefix (the default block avoids this by carrying no variables).
export function resolvePromptVars(s: string, model: string): string {
  const now = new Date();
  const date = now.toLocaleDateString(undefined, {
    weekday: "long",
    year: "numeric",
    month: "long",
    day: "numeric",
  });
  const time = now.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return s
    .replace(/\{date\}/g, date)
    .replace(/\{time\}/g, time)
    .replace(/\{model\}/g, model || "the selected model");
}
