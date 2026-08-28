// Playground fixtures for the screenshot harness (`npm run shots -- --playground`).
//
// The playground is a second app on its own port, behind a per-user login, whose
// entire content is server-backed per user: chats, image threads, speech threads,
// prefs, memories. Photographing it therefore has two problems the dashboard
// shots never had.
//
// 1. There is nothing to photograph until somebody has USED it, and what they
//    used it for is their own conversation.
// 2. Producing something to photograph means running inference -- which on this
//    machine means a model swap, evicting whatever the box is actually serving.
//
// So this does what shot-demo.mjs does for the dashboard: it fakes the boundary
// the app reaches for, one layer above the network. `/auth/me` answers "logged
// in", the four history endpoints answer with canned threads, and the model
// catalog still arrives over the same faked EventSource the dashboard shots use
// (--demo), so every model id in the composer's dropdown is real. Nothing is
// sent to a model and nobody has to log in.
//
// ## Reads are faked, writes are REFUSED
//
// This one is not optional. Every history store hydrates from the server and
// then auto-PUTs the whole array back whenever it changes -- and a PUT is
// destructive: the client owns the blob, so the server takes it as the new
// truth and garbage-collects the media of any session that vanished. Point the
// harness at a real playground, let one canned fixture reach one PUT, and the
// user's entire chat history is replaced by this file's four demo threads.
//
// `installPlayground` therefore intercepts every non-GET request to the
// playground origin and answers it locally, whether or not it recognizes the
// route, and reports each one it caught. Read-only by construction rather than
// by being careful.
//
// ## What cannot be faked
//
// A generated image. Everything else here is text we can write, but a picture
// out of a diffusion model is the one artifact that has to have been generated.
// `--playground-image <file>` supplies one; without it the image thread is left
// out and the shot is skipped with a note, rather than photographing an empty
// studio or a broken image icon.

/** Username the fixture is "logged in" as. */
export const PG_USER = "demo";

// ---------------------------------------------------------------------------
// Chat thread: an ordinary conversation.
// ---------------------------------------------------------------------------

// Deliberately not a conversation about quartermaster, and deliberately not a
// coding question either. Quartermaster serves whatever model you point it at;
// it is not a coding assistant, and a chat shot full of Python teaches the
// opposite. An ordinary household question shows the same thing the shot is
// actually about -- reasoning kept out of the answer, a structured reply -- with
// no claim about what the product is for riding on it.
const CHAT_TURNS = [
  {
    role: "user",
    content: "I'm cooking risotto for eight on Saturday and I've only ever made it for two. What actually changes when I scale it up?",
  },
  {
    role: "assistant",
    reasoning_content:
      "Scaling risotto is not a straight multiplication: the rice-to-liquid ratio holds, but the pan does not. Eight portions is roughly four times the rice, and in a normal pan that means a deeper bed, slower evaporation and rice that steams instead of agitating. The other thing that breaks is timing -- you cannot plate eight bowls at the same eighteen-minute mark unless you stop the cooking partway, which restaurants do routinely.",
    reasoningTitle: "Working out what does and does not scale linearly",
    reasoningTimeMs: 3800,
    genTimeMs: 16200,
    content: `Three things change, and only one of them is the shopping list.

**The ratio holds, the pan does not.** Rice and stock scale straight up — about 80 g of rice a head, so 640 g, and roughly a litre of stock per 250 g of rice. What does not scale is the surface area. Four times the rice in the same pan gives you a deep bed that steams rather than simmers, and the grains stop rubbing against each other, which is where the starch comes from. Use your widest pan, and if the rice is more than about 2 cm deep, cook it in two pans.

**Stir less, not more.** The constant-stirring rule is really "keep the grains moving", and a wide, full pan does much of that on its own. Stirring a large batch as hard as a small one breaks the grains and turns it gluey.

**Split the cooking.** This is the part that saves Saturday. Cook it to roughly 15 minutes — the grain still chalky in the middle — then spread it thin on a tray and let it stop. It will hold for a couple of hours. Ten minutes before you eat, back in the pan with hot stock for the last stretch, then the butter and cheese off the heat. Restaurants do exactly this, and it is the only way eight bowls arrive at once.

Two smaller ones: warm the stock, or it drops the pan temperature every ladle; and salt the stock rather than the finished dish, because 640 g of rice takes a surprising amount and you cannot fix it at the end.

If you tell me which risotto, I can tell you where in that timeline the other ingredients go — mushrooms and seafood want opposite ends of it.`,
  },
];

// ---------------------------------------------------------------------------
// Shopping thread: tools + web search, ending in a report.
// ---------------------------------------------------------------------------

// This one carries a `searches` block (the collapsible web-search sections) AND
// a ```products fence (the card grid), because they are the same story: the
// model went and looked, then reported.
//
// Every brand, shop, review site and URL below is INVENTED, on the `.example`
// TLD that RFC 2606 reserves for exactly this. A demo shot of a shopping report
// necessarily puts prices and opinions next to product names; hanging those on
// real monitors from real retailers would mean publishing claims we made up
// about somebody's actual product. Fictional ones demonstrate the feature just
// as well and assert nothing about anyone.
//
// The pictures are drawn here too -- see PRODUCT_ART -- because a fictional
// monitor has no photograph, and the empty-image monogram the cards fell back to
// was the thing that looked broken in the shot.
const SHOP_TURNS = [
  { role: "user", content: "I need a 27\" 1440p monitor for programming, under 400 EUR, ideally height-adjustable. I'm in Germany." },
  {
    role: "assistant",
    content: `**Brief:** 27" 1440p, primarily text work, budget 400 EUR, height-adjustable stand, shipping to Germany. Prioritising panel uniformity and stand ergonomics over refresh rate.

I checked current listings and opened the product pages for the two that came in under budget with a stand worth having.

\`\`\`products
{
  "pick": "The Lumeva PX27-Q is the one I would buy: the stand does everything and the panel is the calmer of the two for text.",
  "products": [
    {
      "name": "Lumeva PX27-Q",
      "price": "349,00 EUR",
      "shop": "Nordkauf",
      "url": "https://www.nordkauf.example/p/lumeva-px27q",
      "image": "https://cdn.nordkauf.example/lumeva-px27q.png",
      "specs": ["27\\" IPS, 2560x1440", "165 Hz, 1 ms", "Height / tilt / swivel / pivot", "USB-C 65 W"],
      "why": "Better stand, and the only one with a single-cable laptop setup.",
      "badge": "Best overall",
      "cite": 1
    },
    {
      "name": "Kalder Q27 Vivid",
      "price": "289,00 EUR",
      "shop": "elektro24",
      "url": "https://www.elektro24.example/dp/kalder-q27-vivid",
      "image": "https://cdn.elektro24.example/kalder-q27-vivid.png",
      "specs": ["27\\" VA, 2560x1440", "180 Hz, mini-LED", "Height / tilt / pivot", "HDR1000"],
      "why": "Cheaper, and by far the better HDR, but VA text is softer than IPS.",
      "badge": "Cheapest",
      "cite": 2
    }
  ]
}
\`\`\`

What I could not verify: neither page states panel uniformity per unit, and the Kalder listing does not say which mini-LED revision ships in the EU.`,
    model: "",
    genTimeMs: 41800,
    searches: [
      {
        query: "27 zoll 1440p monitor h\u00f6henverstellbar test 2026",
        kind: "web",
        at: 0,
        results: "12 results",
        sources: [
          { title: "Monitor-Testberichte 2026 \u2014 Displaytest.de", url: "https://www.displaytest.example" },
          { title: "Die besten 27-Zoll-Monitore \u2014 Technikbrief", url: "https://www.technikbrief.example" },
        ],
      },
      {
        query: "Lumeva PX27-Q Preis Deutschland",
        kind: "page",
        at: 0,
        results: "nordkauf.example \u2014 product page",
        sources: [{ title: "Lumeva PX27-Q \u2014 Nordkauf", url: "https://www.nordkauf.example/p/lumeva-px27q" }],
      },
    ],
    citations: [
      { n: 1, title: "Lumeva PX27-Q \u2014 Nordkauf", url: "https://www.nordkauf.example/p/lumeva-px27q" },
      { n: 2, title: "Kalder Q27 Vivid \u2014 elektro24", url: "https://www.elektro24.example/dp/kalder-q27-vivid" },
    ],
  },
];

// ---------------------------------------------------------------------------
// Product pictures.
// ---------------------------------------------------------------------------

// The cards resolve `image` through `/api/imgproxy` (productBlock.ts), so the
// harness answers that endpoint for these three URLs rather than let the server
// dial a domain that deliberately does not exist. SVG rather than a raster: it
// is text in a fixture file, it stays sharp at whatever deviceScaleFactor the
// shot uses, and there is no binary blob to carry in the repo.
//
// Deliberately a flat studio render and not an attempt at a photograph. The shot
// is about the card grid, and three obviously different silhouettes photograph
// better than three near-identical black rectangles would.

const SHELL = `<linearGradient id="shell" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#565d66"/><stop offset="1" stop-color="#2b2f36"/></linearGradient>`;
const GLASS = `<linearGradient id="glass" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#2d4356"/><stop offset="0.55" stop-color="#1c2a38"/><stop offset="1" stop-color="#243a4c"/></linearGradient>`;

/** Wraps a drawing in the shared canvas, defs and contact shadow. */
const art = (body, defs = "") =>
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 200" width="320" height="200">` +
  `<defs>${SHELL}${GLASS}` +
  `<radialGradient id="floor" cx="0.5" cy="0.5"><stop offset="0" stop-color="#000" stop-opacity="0.42"/><stop offset="1" stop-color="#000" stop-opacity="0"/></radialGradient>` +
  `${defs}</defs>` +
  `<ellipse cx="160" cy="190" rx="96" ry="9" fill="url(#floor)"/>${body}</svg>`;

/** One screen: frame, glass, and the diagonal sheen that reads as "display off". */
const panel = (x, y, w, h) =>
  `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="7" fill="url(#shell)"/>` +
  `<rect x="${x + 5}" y="${y + 5}" width="${w - 10}" height="${h - 14}" rx="3" fill="url(#glass)"/>` +
  `<path d="M${x + 5} ${y + h - 9} L${x + w - 32} ${y + 5} L${x + w - 5} ${y + 5} L${x + 5} ${y + h - 9} Z" fill="#fff" opacity="0.05"/>` +
  `<rect x="${x + w / 2 - 14}" y="${y + h - 7}" width="28" height="2" rx="1" fill="#7e8794" opacity="0.7"/>`;

export const PRODUCT_ART = {
  // Pedestal stand: the plain one, and the widest panel of the three.
  "https://cdn.nordkauf.example/lumeva-px27q.png": art(
    panel(28, 22, 264, 128) +
      `<rect x="150" y="150" width="20" height="26" rx="3" fill="url(#shell)"/>` +
      `<rect x="112" y="176" width="96" height="8" rx="4" fill="url(#shell)"/>`,
  ),
  // Curved mini-LED, V-foot, and the only lit one -- it is the HDR pick.
  "https://cdn.elektro24.example/kalder-q27-vivid.png": art(
    `<path d="M32 24 q128 -12 256 0 l0 118 q-128 12 -256 0 Z" fill="url(#shell)"/>` +
      `<path d="M40 31 q120 -11 240 0 l0 104 q-120 11 -240 0 Z" fill="url(#hdr)"/>` +
      `<path d="M40 128 L272 34 L280 34 L40 135 Z" fill="#fff" opacity="0.06"/>` +
      `<rect x="152" y="146" width="16" height="22" rx="3" fill="url(#shell)"/>` +
      `<path d="M160 166 L106 186 L124 186 L160 173 L196 186 L214 186 Z" fill="url(#shell)"/>`,
    `<linearGradient id="hdr" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#3b2a4e"/><stop offset="0.5" stop-color="#1d2740"/><stop offset="1" stop-color="#4a2f3a"/></linearGradient>`,
  ),
};

// ---------------------------------------------------------------------------
// Speech thread.
// ---------------------------------------------------------------------------

// Kokoro's own English speakers. The voice rail is drawn from a CACHED list
// (lib/voices.ts) precisely so opening the tab cannot force a model load, and on
// a cold instance that cache is empty -- the panel photographs as one "Default"
// row. Seeding the cache is the only way to show the rail without loading the
// model, and these are the real names, not invented ones: the shot's clips are
// already labelled af_heart.
//
// Keyed by model id, so a box whose speech model is something else gets the
// honest empty rail rather than Kokoro's speakers under another model's name.
const KOKORO_VOICES = [
  "af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky",
  "am_adam", "am_michael", "bf_emma", "bf_isabella", "bm_george", "bm_lewis",
];

const SPEECH_LINES = [
  { text: "The harbour was quiet by the time we tied up, and the only light came from the chandlery on the far quay.", voice: "af_heart", secs: 1.8 },
  { text: "Second paragraph, same voice, so the two clips can be compared back to back.", voice: "af_heart", secs: 1.2 },
];

/**
 * A short WAV that photographs as speech.
 *
 * AudioPlayer decodes the clip and draws its real peaks, so silence renders as a
 * dead flat line -- which is a worse picture than no picture. This synthesizes a
 * syllabic envelope (roughly four syllables a second) over a voiced tone plus
 * noise, which draws the bar pattern a spoken sentence actually has. It is a
 * fixture, exactly like the synthesized traffic in shot-demo.mjs: it stands in
 * for the shape of a clip, and no claim about audio quality rides on it.
 */
function speechWav(seconds, seed = 1) {
  const rate = 24000;
  const n = Math.round(rate * seconds);
  const data = Buffer.alloc(n * 2);
  // Deterministic PRNG: a screenshot that changes shape between runs cannot be
  // diffed against the one before it.
  let s = seed >>> 0;
  const rnd = () => ((s = (s * 1664525 + 1013904223) >>> 0) / 4294967296) * 2 - 1;
  for (let i = 0; i < n; i++) {
    const t = i / rate;
    // Syllables, with a slow phrase-level swell over the top and a gap before
    // the end so the clip reads as a sentence rather than a tone.
    const syl = Math.max(0, Math.sin(2 * Math.PI * 4.1 * t)) ** 0.6;
    const phrase = 0.55 + 0.45 * Math.sin(2 * Math.PI * 0.31 * t + 1);
    const tail = Math.min(1, (seconds - t) * 4);
    const env = syl * phrase * tail;
    const voiced = Math.sin(2 * Math.PI * 118 * t) * 0.5 + Math.sin(2 * Math.PI * 236 * t) * 0.25;
    const v = env * (voiced * 0.7 + rnd() * 0.3) * 0.6;
    data.writeInt16LE(Math.max(-32768, Math.min(32767, Math.round(v * 32767))), i * 2);
  }
  const head = Buffer.alloc(44);
  head.write("RIFF", 0);
  head.writeUInt32LE(36 + data.length, 4);
  head.write("WAVEfmt ", 8);
  head.writeUInt32LE(16, 16);
  head.writeUInt16LE(1, 20); // PCM
  head.writeUInt16LE(1, 22); // mono
  head.writeUInt32LE(rate, 24);
  head.writeUInt32LE(rate * 2, 28);
  head.writeUInt16LE(2, 32);
  head.writeUInt16LE(16, 34);
  head.write("data", 36);
  head.writeUInt32LE(data.length, 40);
  return `data:audio/wav;base64,${Buffer.concat([head, data]).toString("base64")}`;
}

// Ids are fixed strings rather than the app's own time-based ones: the harness
// seeds localStorage with the id of the thread each shot opens, so it has to
// know it in advance, and a stable id keeps two runs diffable.
export const CHAT_ID = "shot-chat";
export const SHOP_ID = "shot-shop";
export const IMAGE_ID = "shot-image";
export const SPEECH_ID = "shot-speech";

/**
 * Builds every fixture a playground run needs.
 *
 * `model`/`imageModel`/`speechModel` come from the live catalog (see
 * shot-demo.mjs) so the model names in the picture are models this box has.
 * `imageTurn` is `{ prompt, dataUrl }` or null -- see the note at the top about
 * the one thing that cannot be faked.
 */
export function buildPlayground({ model, imageModel, speechModel, imageTurn } = {}) {
  const now = Date.now();
  const id = model?.id ?? "";
  const stamp = (m) => (m.role === "assistant" ? { ...m, model: m.model || id } : m);

  const chats = [
    {
      id: CHAT_ID,
      title: "Risotto for eight instead of two",
      titled: true,
      model: id,
      updatedAt: now - 4 * 60_000,
      messages: CHAT_TURNS.map(stamp),
    },
    {
      id: SHOP_ID,
      title: "27\" 1440p monitor under 400 EUR",
      titled: true,
      model: id,
      updatedAt: now - 90_000,
      messages: SHOP_TURNS.map(stamp),
    },
    // Two more in the rail, so the history list is not two rows long. No
    // messages worth photographing -- they exist to look like a used app.
    { id: "shot-old-1", title: "Rewrite this email so it sounds less annoyed", titled: true, model: id, updatedAt: now - 3 * 3600_000, messages: CHAT_TURNS.map(stamp) },
    { id: "shot-old-2", title: "What is actually in mineral sunscreen?", titled: true, model: id, updatedAt: now - 26 * 3600_000, messages: CHAT_TURNS.map(stamp) },
  ];

  const imageChats = imageTurn
    ? [
        {
          id: IMAGE_ID,
          title: imageTurn.prompt.slice(0, 48),
          titled: true,
          updatedAt: now - 2 * 60_000,
          turns: [
            {
              prompt: imageTurn.prompt,
              refs: [],
              images: [imageTurn.dataUrl],
              secs: imageTurn.secs ?? 21.4,
              model: imageModel?.id,
            },
          ],
        },
      ]
    : [];

  const speechChats = [
    {
      id: SPEECH_ID,
      title: SPEECH_LINES[0].text.slice(0, 48),
      titled: true,
      updatedAt: now - 6 * 60_000,
      turns: SPEECH_LINES.map((l, i) => ({
        text: l.text,
        voice: l.voice,
        secs: l.secs,
        audio: speechWav(3.4 + i * 0.8, i + 1),
      })),
    },
  ];

  // Only what changes the picture. Everything else falls back to the store's own
  // default, which is what a real user sees on their first day anyway.
  const prefs = {
    "playground-selected-model": id,
    "playground-image-model": imageModel?.id ?? "",
    // Two different keys, on purpose: the Speech studio's own picker
    // (playground-speech-model) and the read-aloud voice the chat tab uses
    // (playground-chat-tts-model). Seeding only the latter left the studio
    // captioned "Select a speech model…".
    "playground-speech-model": speechModel?.id ?? "",
    "playground-chat-tts-model": speechModel?.id ?? "",
    "playground-speech-voice": SPEECH_LINES[0].voice,
    ...(speechModel && /kokoro/i.test(speechModel.id)
      ? { "playground-speech-voices-cache-v4": { [speechModel.id]: ["", ...KOKORO_VOICES] } }
      : {}),
    "playground-shopping-prefs": "Germany, EUR, prefer shops that ship within the EU",
    // The Images tab photographs whatever image model the catalog offers, and an
    // SDXL-anime model auto-fills the booru quality-tag negative (SDXL_ANIME_NEG
    // in imageGen.ts) -- which opens with the word "nsfw" and lands, at 1440px,
    // in the middle of a public landing page. Seeding an ordinary negative keeps
    // the row honest (it IS what that field holds) without the vocabulary.
    "playground-sdapi-negative-prompt": "blurry, low quality, watermark, text",
  };

  return { user: PG_USER, chats, imageChats, speechChats, prefs, memories: [] };
}

/**
 * localStorage seed for one playground shot: which tab to open.
 *
 * Which THREAD is open is deliberately not here. It looks like it should be --
 * the three "playground-active-*" keys are exactly that pointer -- but the seed
 * cannot survive the load: PlaygroundApp's hydration effect runs once with `$me`
 * still null (before `checkMe()` has resolved) and calls `clearChats()`, which
 * sets the active id to "". Whatever was persisted is gone before the history
 * arrives, and each interface then falls back to its most recently updated
 * session. So the fixture picks the thread by BEING the most recent one --
 * `focus()` below -- and the seed only has to carry the tab.
 */
export function pgStorage(tab) {
  return { "playground-selected-tab": JSON.stringify(tab) };
}

/**
 * Installs the fixture on a browser context.
 *
 * `onWrite` is called with every mutating request that was refused, so the run
 * can report them: a new PUT appearing here means a store started saving
 * something a shot did not intend, and on a real playground that would have been
 * a destructive write.
 *
 * Returns `{ focus }`. Call `focus(id)` before navigating to choose which thread
 * the next load opens on: it rewrites the served history so that session is the
 * most recently updated one, which is the only lever that survives hydration
 * (see pgStorage). The route reads the arrays at request time, so this takes
 * effect on the next load without reinstalling anything.
 */
export async function installPlayground(context, fixture, onWrite = () => {}) {
  // ONE handler for every request, rather than a catch-all plus a handler per
  // endpoint. Playwright resolves overlapping routes by registration order, and
  // getting that backwards here would not throw -- it would quietly let
  // `GET /api/chats` through to the real server and photograph the user's own
  // conversations. Dispatching inside a single route depends on no such rule.
  const canned = {
    "/auth/me": { username: fixture.user },
    "/api/chats": fixture.chats,
    "/api/imagechats": fixture.imageChats,
    "/api/speechchats": fixture.speechChats,
    "/api/prefs": fixture.prefs,
    "/api/memories": fixture.memories,
    // Whether a turn is running is asked on mount. The honest answer for a
    // canned history is "nothing"; letting it through would have the page
    // reconnect to whatever the real user has generating right now.
    "/api/chats/turn/state": { chatId: "" },
  };

  await context.route("**/*", (route) => {
    const req = route.request();
    const method = req.method();
    const { pathname } = new URL(req.url());

    // Read-only by construction, not by being careful: anything mutating is
    // answered locally whether or not this file knows the route. Every history
    // store PUTs its WHOLE array back on any change, and the server takes that
    // as the new truth -- one escaped PUT replaces a real user's history with
    // these four demo threads and GCs the media of everything that vanished.
    if (method !== "GET" && method !== "HEAD") {
      onWrite(`${method} ${pathname}`);
      return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    }

    // Product pictures. The cards ask the server to fetch these (productBlock
    // proxies rather than hotlinks); the URLs are invented, so answer them here
    // instead of watching three DNS failures become three broken-image icons.
    if (pathname === "/api/imgproxy") {
      const target = new URL(req.url()).searchParams.get("url") ?? "";
      if (target in PRODUCT_ART) {
        return route.fulfill({ contentType: "image/svg+xml", body: PRODUCT_ART[target] });
      }
    }

    if (pathname in canned) {
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(canned[pathname]) });
    }
    // The bundle, its assets and any read-only endpoint nobody canned (the
    // voice list, /api/mode) still come from the real instance.
    return route.continue();
  });

  // Bump one session to the top of its list. Relative order among the others is
  // preserved by spacing the rewritten stamps a minute apart, so the history
  // rail still reads as a plausible week of work rather than four threads all
  // touched in the same second.
  return {
    focus(id) {
      if (!id) return;
      for (const list of [fixture.chats, fixture.imageChats, fixture.speechChats]) {
        if (!list.some((s) => s.id === id)) continue;
        const rest = list.filter((s) => s.id !== id).sort((a, b) => b.updatedAt - a.updatedAt);
        const top = Math.max(...list.map((s) => s.updatedAt));
        list.find((s) => s.id === id).updatedAt = top + 60_000;
        rest.forEach((s, i) => (s.updatedAt = top - i * 60_000));
      }
    },
  };
}
