// Landing-page copy and data. Split from build.mjs so editing a headline or
// adding a feature card never means reading render code.
//
// The docs half of the site is NOT here: those pages come from
// internal/server/wiki_articles.json, the same corpus the app's Help modal and
// the `wiki_search` tool read. Anything a user might also need while the app is
// open belongs in an article, not on this page.

export const REPO = "https://github.com/Quartermaster-Labs/Quartermaster";
export const UPSTREAM = "https://github.com/mostlygeek/llama-swap";

export const HERO = {
  // Left empty on purpose. An eyebrow above the <h1> is a boxed, monospaced,
  // accented line, so it wins the first glance whatever it says, which is the
  // wrong order: the headline is the claim. The reach it carried (text, image
  // and audio) is in the pills and in the lede.
  eyebrow: "",
  // `accent` is rendered as gradient text inside the <h1>.
  title: ["Run any model", "without tuning", "a single flag"],
  lede:
    "Quartermaster is an all-in-one local inference platform. Point it at your models folder: it " +
    "works out what fits in your VRAM, launches each model with computed flags, and hot-swaps " +
    "between them on demand behind one OpenAI- and Anthropic-compatible API.",
};

// Under the hero CTAs: the five things someone is scanning for before they
// decide to read further. Short enough to take in without reading.
export const PILLS = [
  "Runs on your hardware",
  "Bring your own models",
  "OpenAI + Anthropic API",
  "Text · image · audio",
  "No telemetry",
];

// Each section opens with an accent eyebrow, a centred heading and a sub-lede.
// Keyed by the section's DOM id so build.mjs can render them uniformly.
export const SECTIONS = {
  features: {
    eyebrow: "",
    icon: "box",
    title: "It works the machine out for you",
    sub:
      "One endpoint in front of every model you own, the sizing decisions made for you instead " +
      "of by you, and a front end for all of it.",
  },
  more: {
    eyebrow: "",
    icon: "box",
    title: "And much more",
    sub: "A plethora of features to make it a breeze to run your models the way you want to",
  },
  install: {
    eyebrow: "",
    icon: "terminal",
    title: "Installations",
    sub:
      "Windows, Linux, macOS and Docker, from the same single binary. The installer and the Docker " +
      "image bring the inference backends with them; everywhere else you install them from Settings " +
      "on first run, or point at ones you already have.",
  },
  story: {
    eyebrow: "How it started",
    icon: "compass",
    title: "A config file I got tired of writing",
    sub: null,
  },
  docs: {
    eyebrow: "Documentation",
    icon: "book",
    title: "The manual ships inside the app",
    sub:
      "These are the same help articles behind the Help button in the sidebar, and the assistant in " +
      "the playground searches them as a tool, so the quickest way to learn Quartermaster is to open " +
      "the chat and ask. Why did my model get evicted, what does this flag do, how do I cap the VRAM " +
      "budget: it reads the manual and answers with your setup in front of it.",
  },
};

// First person, on purpose: the one place on the site with a voice rather than a
// feature list. Rewrite this in your own words, it is the part nobody else can
// generate for you.
//
// This is also the ONLY place in the body copy that names llama-swap, and it is
// linked automatically by renderStory(). The attribution belongs here and in the
// footer; repeating it in the feature grid just told a first-time reader to go
// evaluate a different project before this one.
export const STORY = [
  "Every model meant another hand-written block of config: how much context, how many layers on " +
    "the GPU, how big the KV cache, whether the experts go on the CPU. Then a new quant lands and " +
    "you do it again. Sometimes I wanted a configuration with high context, other times I wanted a " +
    "very lean model load to allow something else on my GPU at the same time. The solution I came " +
    "up with is the variants system.",
  "It started as a fork of llama-swap, which had the swapping right, and grew in the obvious " +
    "direction from there: read the GGUF header, measure the VRAM that is actually free, and " +
    "compute the numbers instead of typing them. Once that worked the rest followed. Image and " +
    "audio backends in the same catalog, several ports sharing one scheduler, a KV-cache that " +
    "survives being evicted, and a UI that shows what the box is doing rather than a log you have " +
    "to tail.",
  "It is its own thing now.",
];

// Lucide (ISC) path data, inlined so the page makes no third-party request.
// Same icon set the app itself uses, so a feature card and its screen match.
export const ICONS = {
  wand: '<path d="m15 4 1 2 2 1-2 1-1 2-1-2-2-1 2-1z"/><path d="M9 11 3 17l4 4 6-6"/><path d="m14 7 3 3"/><path d="M5 6h.01M19 13h.01"/>',
  gauge: '<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  save: '<path d="M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M17 21v-7a1 1 0 0 0-1-1H8a1 1 0 0 0-1 1v7"/><path d="M7 3v4a1 1 0 0 0 1 1h7"/>',
  network: '<rect x="9" y="2" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="16" y="16" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>',
  layers: '<path d="m12.8 2.5 8.7 4.4a1 1 0 0 1 0 1.8l-8.7 4.3a2 2 0 0 1-1.6 0L2.5 8.7a1 1 0 0 1 0-1.8l8.7-4.4a2 2 0 0 1 1.6 0Z"/><path d="m2.5 12.6 8.7 4.4a2 2 0 0 0 1.6 0l8.7-4.4"/><path d="m2.5 17.1 8.7 4.4a2 2 0 0 0 1.6 0l8.7-4.4"/>',
  play: '<circle cx="12" cy="12" r="10"/><path d="m10 8 6 4-6 4z"/>',
  shield: '<path d="M20 13c0 5-3.5 7.5-7.7 9a1 1 0 0 1-.6 0C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.2-2.7a1 1 0 0 1 1.5 0C14.5 3.8 17 5 19 5a1 1 0 0 1 1 1z"/>',
  container: '<path d="M22 7.7c0-.6-.4-1.2-.8-1.5l-6.3-3.9a1.7 1.7 0 0 0-1.7 0l-6.3 3.9C6.4 6.5 6 7.1 6 7.7v8.6c0 .6.4 1.2.8 1.5l6.3 3.9a1.7 1.7 0 0 0 1.7 0l6.3-3.9c.4-.3.9-.9.9-1.5z"/><path d="M10 21.9V14L2.2 9.5"/><path d="m10 14 11.7-6.8"/>',
  terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
  book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/>',
  box: '<path d="M21 8a2 2 0 0 0-1-1.7l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.7l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>',
  compass: '<circle cx="12" cy="12" r="10"/><path d="m16.2 7.8-2.9 6.5-6.5 2.9 2.9-6.5z"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  monitor: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>',
  activity: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
  image: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-4.6-4.6a2 2 0 0 0-2.8 0L3 21"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
};

// Platform marks. These are FILLED closed paths, not stroke outlines, so
// build.mjs renders anything named here with `fill` instead of `stroke`: the
// two cannot share one <svg> element's attributes.
//
// linux/apple/docker are Simple Icons (CC0). `windows` is our own four-pane
// mark on purpose: Simple Icons carries no Microsoft glyph (they were removed
// at Microsoft's request), and the vendors' marks are used here only to label
// which build a download is.
export const BRAND_ICONS = {
  windows: '<path d="M3 5.5 10 4.5v7H3z"/><path d="M11.5 4.3 21 3v8.5h-9.5z"/><path d="M3 12.5h7v7L3 18.5z"/><path d="M11.5 12.5H21V21l-9.5-1.3z"/>',
  linux: '<path d="M12.504 0c-.155 0-.315.008-.48.021-4.226.333-3.105 4.807-3.17 6.298-.076 1.092-.3 1.953-1.05 3.02-.885 1.051-2.127 2.75-2.716 4.521-.278.832-.41 1.684-.287 2.489a.424.424 0 00-.11.135c-.26.268-.45.6-.663.839-.199.199-.485.267-.797.4-.313.136-.658.269-.864.68-.09.189-.136.394-.132.602 0 .199.027.4.055.536.058.399.116.728.04.97-.249.68-.28 1.145-.106 1.484.174.334.535.47.94.601.81.2 1.91.135 2.774.6.926.466 1.866.67 2.616.47.526-.116.97-.464 1.208-.946.587-.003 1.23-.269 2.26-.334.699-.058 1.574.267 2.577.2.025.134.063.198.114.333l.003.003c.391.778 1.113 1.132 1.884 1.071.771-.06 1.592-.536 2.257-1.306.631-.765 1.683-1.084 2.378-1.503.348-.199.629-.469.649-.853.023-.4-.2-.811-.714-1.376v-.097l-.003-.003c-.17-.2-.25-.535-.338-.926-.085-.401-.182-.786-.492-1.046h-.003c-.059-.054-.123-.067-.188-.135a.357.357 0 00-.19-.064c.431-1.278.264-2.55-.173-3.694-.533-1.41-1.465-2.638-2.175-3.483-.796-1.005-1.576-1.957-1.56-3.368.026-2.152.236-6.133-3.544-6.139zm.529 3.405h.013c.213 0 .396.062.584.198.19.135.33.332.438.533.105.259.158.459.166.724 0-.02.006-.04.006-.06v.105a.086.086 0 01-.004-.021l-.004-.024a1.807 1.807 0 01-.15.706.953.953 0 01-.213.335.71.71 0 00-.088-.042c-.104-.045-.198-.064-.284-.133a1.312 1.312 0 00-.22-.066c.05-.06.146-.133.183-.198.053-.128.082-.264.088-.402v-.02a1.21 1.21 0 00-.061-.4c-.045-.134-.101-.2-.183-.333-.084-.066-.167-.132-.267-.132h-.016c-.093 0-.176.03-.262.132a.8.8 0 00-.205.334 1.18 1.18 0 00-.09.4v.019c.002.089.008.179.02.267-.193-.067-.438-.135-.607-.202a1.635 1.635 0 01-.018-.2v-.02a1.772 1.772 0 01.15-.768c.082-.22.232-.406.43-.533a.985.985 0 01.594-.2zm-2.962.059h.036c.142 0 .27.048.399.135.146.129.264.288.344.465.09.199.14.4.153.667v.004c.007.134.006.2-.002.266v.08c-.03.007-.056.018-.083.024-.152.055-.274.135-.393.2.012-.09.013-.18.003-.267v-.015c-.012-.133-.04-.2-.082-.333a.613.613 0 00-.166-.267.248.248 0 00-.183-.064h-.021c-.071.006-.13.04-.186.132a.552.552 0 00-.12.27.944.944 0 00-.023.33v.015c.012.135.037.2.08.334.046.134.098.2.166.268.01.009.02.018.034.024-.07.057-.117.07-.176.136a.304.304 0 01-.131.068 2.62 2.62 0 01-.275-.402 1.772 1.772 0 01-.155-.667 1.759 1.759 0 01.08-.668 1.43 1.43 0 01.283-.535c.128-.133.26-.2.418-.2zm1.37 1.706c.332 0 .733.065 1.216.399.293.2.523.269 1.052.468h.003c.255.136.405.266.478.399v-.131a.571.571 0 01.016.47c-.123.31-.516.643-1.063.842v.002c-.268.135-.501.333-.775.465-.276.135-.588.292-1.012.267a1.139 1.139 0 01-.448-.067 3.566 3.566 0 01-.322-.198c-.195-.135-.363-.332-.612-.465v-.005h-.005c-.4-.246-.616-.512-.686-.71-.07-.268-.005-.47.193-.6.224-.135.38-.271.483-.336.104-.074.143-.102.176-.131h.002v-.003c.169-.202.436-.47.839-.601.139-.036.294-.065.466-.065zm2.8 2.142c.358 1.417 1.196 3.475 1.735 4.473.286.534.855 1.659 1.102 3.024.156-.005.33.018.513.064.646-1.671-.546-3.467-1.089-3.966-.22-.2-.232-.335-.123-.335.59.534 1.365 1.572 1.646 2.757.13.535.16 1.104.021 1.67.067.028.135.06.205.067 1.032.534 1.413.938 1.23 1.537v-.043c-.06-.003-.12 0-.18 0h-.016c.151-.467-.182-.825-1.065-1.224-.915-.4-1.646-.336-1.77.465-.008.043-.013.066-.018.135-.068.023-.139.053-.209.064-.43.268-.662.669-.793 1.187-.13.533-.17 1.156-.205 1.869v.003c-.02.334-.17.838-.319 1.35-1.5 1.072-3.58 1.538-5.348.334a2.645 2.645 0 00-.402-.533 1.45 1.45 0 00-.275-.333c.182 0 .338-.03.465-.067a.615.615 0 00.314-.334c.108-.267 0-.697-.345-1.163-.345-.467-.931-.995-1.788-1.521-.63-.4-.986-.87-1.15-1.396-.165-.534-.143-1.085-.015-1.645.245-1.07.873-2.11 1.274-2.763.107-.065.037.135-.408.974-.396.751-1.14 2.497-.122 3.854a8.123 8.123 0 01.647-2.876c.564-1.278 1.743-3.504 1.836-5.268.048.036.217.135.289.202.218.133.38.333.59.465.21.201.477.335.876.335.039.003.075.006.11.006.412 0 .73-.134.997-.268.29-.134.52-.334.74-.4h.005c.467-.135.835-.402 1.044-.7zm2.185 8.958c.037.6.343 1.245.882 1.377.588.134 1.434-.333 1.791-.765l.211-.01c.315-.007.577.01.847.268l.003.003c.208.199.305.53.391.876.085.4.154.78.409 1.066.486.527.645.906.636 1.14l.003-.007v.018l-.003-.012c-.015.262-.185.396-.498.595-.63.401-1.746.712-2.457 1.57-.618.737-1.37 1.14-2.036 1.191-.664.053-1.237-.2-1.574-.898l-.005-.003c-.21-.4-.12-1.025.056-1.69.176-.668.428-1.344.463-1.897.037-.714.076-1.335.195-1.814.12-.465.308-.797.641-.984l.045-.022zm-10.814.049h.01c.053 0 .105.005.157.014.376.055.706.333 1.023.752l.91 1.664.003.003c.243.533.754 1.064 1.189 1.637.434.598.77 1.131.729 1.57v.006c-.057.744-.48 1.148-1.125 1.294-.645.135-1.52.002-2.395-.464-.968-.536-2.118-.469-2.857-.602-.369-.066-.61-.2-.723-.4-.11-.2-.113-.602.123-1.23v-.004l.002-.003c.117-.334.03-.752-.027-1.118-.055-.401-.083-.71.043-.94.16-.334.396-.4.69-.533.294-.135.64-.202.915-.47h.002v-.002c.256-.268.445-.601.668-.838.19-.201.38-.336.663-.336zm7.159-9.074c-.435.201-.945.535-1.488.535-.542 0-.97-.267-1.28-.466-.154-.134-.28-.268-.373-.335-.164-.134-.144-.333-.074-.333.109.016.129.134.199.2.096.066.215.2.36.333.292.2.68.467 1.167.467.485 0 1.053-.267 1.398-.466.195-.135.445-.334.648-.467.156-.136.149-.267.279-.267.128.016.034.134-.147.332a8.097 8.097 0 01-.69.468zm-1.082-1.583V5.64c-.006-.02.013-.042.029-.05.074-.043.18-.027.26.004.063 0 .16.067.15.135-.006.049-.085.066-.135.066-.055 0-.092-.043-.141-.068-.052-.018-.146-.008-.163-.065zm-.551 0c-.02.058-.113.049-.166.066-.047.025-.086.068-.14.068-.05 0-.13-.02-.136-.068-.01-.066.088-.133.15-.133.08-.031.184-.047.259-.005.019.009.036.03.03.05v.02h.003z"/>',
  apple: '<path d="M12.152 6.896c-.948 0-2.415-1.078-3.96-1.04-2.04.027-3.91 1.183-4.961 3.014-2.117 3.675-.546 9.103 1.519 12.09 1.013 1.454 2.208 3.09 3.792 3.039 1.52-.065 2.09-.987 3.935-.987 1.831 0 2.35.987 3.96.948 1.637-.026 2.676-1.48 3.676-2.948 1.156-1.688 1.636-3.325 1.662-3.415-.039-.013-3.182-1.221-3.22-4.857-.026-3.04 2.48-4.494 2.597-4.559-1.429-2.09-3.623-2.324-4.39-2.376-2-.156-3.675 1.09-4.61 1.09zM15.53 3.83c.843-1.012 1.4-2.427 1.245-3.83-1.207.052-2.662.805-3.532 1.818-.78.896-1.454 2.338-1.273 3.714 1.338.104 2.715-.688 3.559-1.701"/>',
  docker: '<path d="M13.983 11.078h2.119a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.119a.185.185 0 00-.185.185v1.888c0 .102.083.185.185.185m-2.954-5.43h2.118a.186.186 0 00.186-.186V3.574a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m0 2.716h2.118a.187.187 0 00.186-.186V6.29a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.887c0 .102.082.185.185.186m-2.93 0h2.12a.186.186 0 00.184-.186V6.29a.185.185 0 00-.185-.185H8.1a.185.185 0 00-.185.185v1.887c0 .102.083.185.185.186m-2.964 0h2.119a.186.186 0 00.185-.186V6.29a.185.185 0 00-.185-.185H5.136a.186.186 0 00-.186.185v1.887c0 .102.084.185.186.186m5.893 2.715h2.118a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m-2.93 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.083.185.185.185m-2.964 0h2.119a.185.185 0 00.185-.185V9.006a.185.185 0 00-.184-.186h-2.12a.186.186 0 00-.186.186v1.887c0 .102.084.185.186.185m-2.92 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.082.185.185.185M23.763 9.89c-.065-.051-.672-.51-1.954-.51-.338.001-.676.03-1.01.087-.248-1.7-1.653-2.53-1.716-2.566l-.344-.199-.226.327c-.284.438-.49.922-.612 1.43-.23.97-.09 1.882.403 2.661-.595.332-1.55.413-1.744.42H.751a.751.751 0 00-.75.748 11.376 11.376 0 00.692 4.062c.545 1.428 1.355 2.48 2.41 3.124 1.18.723 3.1 1.137 5.275 1.137.983.003 1.963-.086 2.93-.266a12.248 12.248 0 003.823-1.389c.98-.567 1.86-1.288 2.61-2.136 1.252-1.418 1.998-2.997 2.553-4.4h.221c1.372 0 2.215-.549 2.68-1.009.309-.293.55-.65.707-1.046l.098-.288Z"/>',
};

// The claims worth a section of their own, each next to the screenshot that
// proves it. A claim beside its evidence beats a strip of anonymous screenshots
// twenty lines further down, which is what this replaced. An entry with no
//  at all is fine: it renders as a centred copy-only block.
//
// `shots` are names under docs/assets/. A shot that isn't there yet is dropped
// at build time with a warning rather than shipped as a broken image, so a
// section degrades to its copy and the page still holds together. One shot
// renders as a framed still beside the copy; two or more render as a gallery
// under it, one at a time, with labelled tabs.
//
// `hero: true` marks the shot that also appears full-width under the hero, so
// the same picture is not shown twice.
export const SHOWCASE = [
  {
    id: "config",
    icon: "wand",
    eyebrow: "Automatic configuration",
    title: "Config that writes itself, then hands you the pen",
    body:
      "Point it at a folder. Every GGUF is identified from its own header, and context length, GPU " +
      "offload, CPU-MoE split and KV-cache sizing are computed per model and per architecture. No " +
      "hand-written config block per model, and no second block when a new quant lands.",
    points: [
      "Every computed number is an editable field, not a wall you have to work around.",
      "Save a tuned set as a named variant and run it alongside the default: a long-context one for " +
        "documents, a lean one for quick calls.",
      "Changed your mind? Reset one field, or the whole model, back to what it computed.",
    ],
    shots: [
      {
        file: "model-config.webp",
        label: "Per-model parameters",
        caption: "Context, KV cache, offload and speculative decoding, with the computed default one click away.",
      },
      {
        file: "model-config-args.webp",
        label: "Fully customizable",
        caption:
          "The whole llama-server command line is right there and editable. Edits fold back into the fields " +
          "above, and flags Quartermaster doesn't model are kept verbatim, the UI is a layer over the flags, " +
          "not a replacement for them.",
      },
    ],
  },
  {
    id: "vram",
    icon: "gauge",
    eyebrow: "Load planning",
    title: "It knows what will fit before it loads it",
    body:
      "Free VRAM is sampled at startup and every model is sized against what is actually left, not " +
      "against the number on the box. The gauge breaks a load down the way the card sees it, so an " +
      "estimate that is about to go wrong is visible before you press load rather than after the " +
      "driver kills the process.",
    points: [
      "Weights, KV cache and compute buffer are accounted separately, per architecture.",
      "The compute buffer is the one large-vocab models silently spill on. It is priced in.",
      "System usage is part of the budget, so the number is what is free for you, not what is free in theory.",
    ],
    shots: [
      {
        file: "vram-gauge.webp",
        label: "VRAM breakdown",
        caption: "One bar per segment: weights, KV cache, compute buffer, and what the rest of the system already holds.",
      },
    ],
  },
  {
    id: "playground",
    icon: "play",
    eyebrow: "Text, image and audio",
    title: "A playground, not just a proxy",
    body:
      "It orchestrates llama-server, stable-diffusion.cpp, TTS and transcription servers, rerank and " +
      "embedding models, upscaling and segmentation, all behind one OpenAI-compatible surface. Then " +
      "it gives you a front end for them, on its own port with per-user login and server-side " +
      "history, so a model is not just reachable the moment it is discovered, it is useful, with " +
      "nothing else installed in front of it.",
    points: [
      "An everyday helper: a shopping assistant mode to help you browse the internet and compare prices, then lists you the options, tell " +
        "you what the weather does tomorrow, rewrite a piece of text according to your instructions, and inspect the diff, " +
        "or even tell you to help you with your model config",
      "Web search and tool calling are wired in, so the answer isn't limited to what the weights " +
        "happen to remember, and the reasoning stream can be collapsed or hidden.",
      "It can explain Quartermaster itself. The help articles are one of its tools, so \"why did my " +
        "model get evicted?\" is a question you can ask in the chat.",
      "Generate and edit images against the same catalog, LoRAs and reference images included.",
      "Speak and transcribe without leaving the tab.",
    ],
    shots: [
      { file: "pg-chat.webp", label: "Chat", caption: "An ordinary conversation, with the thinking stream kept out of the answer." },
      { file: "pg-tools.webp", label: "Tools and web search", caption: "The model calling out mid-conversation and reading a result in full." },
      { file: "pg-image.webp", label: "Image generation", caption: "Diffusion models in the same catalog, driven from the same UI." },
      { file: "pg-speech.webp", label: "Speech", caption: "Text to speech and transcription against your local voices." },
    ],
  },
  {
    id: "catalog",
    icon: "layers",
    eyebrow: "Getting and keeping models",
    title: "Find a model, download it, run it",
    body:
      "Search Hugging Face from inside the app, compare quants against the VRAM you actually have, " +
      "and download into your models folder with the transfer resumable if it breaks. What lands is " +
      "picked up and configured without a restart.",
    points: [
      "Quants are listed with the fit already worked out, so the pick is not a guess.",
      "Variants of one GGUF group together instead of flooding the list.",
      "Text and image models share the catalog and the same management surface.",
    ],
    shots: [
      { file: "browse.webp", label: "Model hub", caption: "Search, pick a quant, download into your models folder." },
      { file: "models.webp", label: "Manage models", caption: "Every discovered model, grouped by GGUF, with its variants under it." },
    ],
  },
  {
    id: "backends",
    icon: "box",
    eyebrow: "Bring your own backend",
    title: "Any inference server you have, and any repo you follow",
    body:
      "The backends it ships with install themselves from their upstream GitHub releases, but nothing " +
      "is hard-wired to them. Point a row at a binary you built yourself and it joins the same " +
      "registry: picked per model class, launched with the flags the config generates, no different " +
      "from a managed install. Or hand it a GitHub repo and it follows that project's releases for you.",
    points: [
      "Track any repo: pick one real asset out of one real release and the match pattern is derived " +
        "from it, build numbers and dates become wildcards, so next week's build of the same flavour " +
        "still resolves. There is no regex to write.",
      "Builds are installed side by side and versioned. Switch which one a backend runs, or roll back " +
        "to the last one that worked, without reinstalling anything.",
      "Locally compiled binaries coexist with managed ones, and an install never quietly steals the " +
        "default from a backend you set up yourself.",
    ],
    shots: [
      {
        file: "backends.webp",
        label: "Settings, Backends",
        caption:
          "Managed installs on top, the registry they write into underneath: every row is a binary " +
          "Quartermaster can spawn, whether it installed it or you did.",
      },
    ],
  },
];

// Everything that does not need a picture to land. Kept as compact cards under
// "and much more" so the showcase above stays four claims rather than twelve.
export const MORE = [
  {
    icon: "refresh",
    title: "On-demand model swapping",
    body:
      "One endpoint, every model. A request naming a model that isn't loaded swaps it in, evicting " +
      "whatever no longer fits, and holds the group together when several have to coexist.",
  },
  {
    icon: "save",
    title: "KV-cache that survives eviction",
    body:
      "Snapshots a slot's KV-cache to disk before the model is evicted and restores it when the " +
      "conversation comes back, so a long chat isn't re-prefilled because a throwaway request " +
      "borrowed the GPU.",
  },
  {
    icon: "network",
    title: "Multi-port catalogs",
    body:
      "Bind several listeners on one shared scheduler, each with its own /v1/models view. Loading " +
      "on one port can evict on another: one process, one GPU accounting.",
  },
  {
    icon: "activity",
    title: "Observe what it is doing",
    body:
      "Activity, streaming logs, per-model performance and context use on one page, so a slow " +
      "request is something you can look at rather than guess about.",
  },
  {
    icon: "shield",
    title: "Safe to put on your LAN",
    body:
      "API keys can be scoped to individual models. Bind the API to your tailnet and the dashboard " +
      "and config endpoints answer to localhost only unless you widen them yourself.",
  },
  {
    icon: "terminal",
    title: "Drivable from the outside",
    body:
      "Prometheus metrics, a log stream you can pipe, ops endpoints to load and unload a model on " +
      "demand, and a config file that hot-reloads when you edit it. No plugin system to learn: the " +
      "surface is HTTP and YAML.",
  },
];

export const INSTALL = [
  {
    icon: "windows",
    title: "Windows installer",
    body:
      "A per-user install, no admin rights needed. The first-run wizard fetches the inference backends, " +
      "asks for your models folder, and generates a config before the window opens.",
    // Filled in by build.mjs from the latest GitHub release.
    download: true,
  },
  {
    icons: ["linux", "apple"],
    title: "Linux and macOS",
    body:
      "The same wizard, in your browser: it fetches a verified binary, the backends for your GPU, and " +
      "a config built from your models folder. Or take the bare static binary instead, amd64 and arm64 " +
      "for Linux and Apple silicon for macOS, and install the backends later from Settings.",
    link: { href: REPO + "/releases/latest", label: "Downloads on the releases page" },
  },
  {
    icon: "docker",
    title: "Docker",
    body:
      "One image, for linux/amd64 and linux/arm64, and it serves the moment it starts: llama-server " +
      "and sd-server ship inside it, the same upstream Vulkan builds a desktop install downloads. " +
      "Point it at your models folder and open the dashboard.",
    code:
      "docker run -p 127.0.0.1:1250:8080 -v ./models:/data/models \\\n" +
      "  ghcr.io/quartermaster-labs/quartermaster:edge",
  },
];

// Building from source is the fourth way in, but it is a fourth AUDIENCE only in
// the sense that it is the same people who already read the README. It lives as
// a line under the cards rather than a card of its own: the grid fits three
// across at this width, and a lone fourth on its own row reads as an omission.
export const INSTALL_NOTE = {
  before: "Prefer to build it yourself? ",
  href: REPO + "#building-from-source",
  link: "Building from source",
  after: " takes Go 1.26+ and Node 24 for the UI, one binary out the other end.",
};
