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

// Deliberately not a conversation about quartermaster. The shot is meant to show
// what the app is FOR -- a local model you actually ask things -- and an app
// demoing itself talking about itself photographs as a hall of mirrors.
const CHAT_TURNS = [
  {
    role: "user",
    content: "I have a 40 GB CSV of taxi trips and pandas OOMs on read_csv. What are my options, ranked by how much I have to rewrite?",
  },
  {
    role: "assistant",
    reasoning_content:
      "The file is far bigger than RAM, so the question is really about streaming versus columnar storage. Cheapest first: chunked reads change almost no code, dtype narrowing is a one-line win, Polars/DuckDB are a rewrite but a small one, and converting to Parquet pays off if this file gets read more than once.",
    reasoningTitle: "Ordering the options by rewrite cost",
    reasoningTimeMs: 4200,
    genTimeMs: 19400,
    content: `Ranked by how much of your code has to change:

**1. Chunk it (no rewrite).** \`read_csv\` takes an iterator. You aggregate per chunk and combine at the end:

\`\`\`python
totals = None
for chunk in pd.read_csv("trips.csv", chunksize=1_000_000):
    part = chunk.groupby("pickup_zone")["fare"].sum()
    totals = part if totals is None else totals.add(part, fill_value=0)
\`\`\`

Works for anything associative — sums, counts, min/max. Medians and exact distinct counts do not survive this.

**2. Narrow the dtypes (one line).** Pandas reads every integer as int64 and every string as object. Passing \`dtype=\` with \`int32\`/\`float32\` and \`category\` for the low-cardinality columns routinely cuts the footprint by half or better, which may be all you need.

**3. Switch the reader (small rewrite).** Polars streams and DuckDB queries the CSV in place:

\`\`\`sql
SELECT pickup_zone, avg(fare) FROM 'trips.csv' GROUP BY 1;
\`\`\`

Neither loads the file into memory. DuckDB is the shortest path if what you have is really a SQL question.

**4. Convert to Parquet once (rewrite, then never again).** Columnar and compressed, so a query that touches three columns reads three columns. Worth it the moment you are going to read this file more than twice.

If you tell me what the query actually is, I can tell you which of these is enough — most trip-data questions are group-bys, and those are fine at step 1.`,
  },
];

// ---------------------------------------------------------------------------
// Shopping thread: tools + web search, ending in a report.
// ---------------------------------------------------------------------------

// This one carries a `searches` block (the collapsible web-search sections) AND
// a ```products fence (the card grid), because they are the same story: the
// model went and looked, then reported. Product images are left empty on
// purpose -- a card with no image renders its monogram, and inventing product
// photography for a screenshot is a line worth not crossing.
const SHOP_TURNS = [
  { role: "user", content: "I need a 27\" 1440p monitor for programming, under 400 EUR, ideally height-adjustable. I'm in Germany." },
  {
    role: "assistant",
    content: `**Brief:** 27" 1440p, primarily text work, budget 400 EUR, height-adjustable stand, shipping to Germany. Prioritising panel uniformity and stand ergonomics over refresh rate.

I checked current listings and opened the product pages for the three that fit.

\`\`\`products
{
  "pick": "The Dell S2722QC is the one I would buy: the stand does everything and the panel is the calmest of the three for text.",
  "products": [
    {
      "name": "Dell S2721DGF",
      "price": "349,00 EUR",
      "shop": "Dell Germany",
      "url": "https://www.dell.com",
      "image": "",
      "specs": ["27\\" IPS, 2560x1440", "165 Hz, 1 ms", "Height / tilt / swivel / pivot", "USB-C 65 W"],
      "why": "Best stand of the three and the only one with a single-cable laptop setup.",
      "badge": "Best overall",
      "cite": 1
    },
    {
      "name": "LG 27QN880-B",
      "price": "319,00 EUR",
      "shop": "MediaMarkt",
      "url": "https://www.mediamarkt.de",
      "image": "",
      "specs": ["27\\" IPS, 2560x1440", "75 Hz", "Ergo arm, clamps to the desk", "USB-C 90 W"],
      "why": "The arm frees the whole desk surface; 75 Hz is the trade-off.",
      "badge": "Best for desk space",
      "cite": 2
    },
    {
      "name": "AOC Q27G3XMN",
      "price": "289,00 EUR",
      "shop": "Amazon.de",
      "url": "https://www.amazon.de",
      "image": "",
      "specs": ["27\\" VA, 2560x1440", "180 Hz, mini-LED", "Height / tilt / pivot", "HDR1000"],
      "why": "Cheapest of the three and by far the best HDR, but VA text is softer than IPS.",
      "badge": "Cheapest",
      "cite": 3
    }
  ]
}
\`\`\`

What I could not verify: none of the three pages state panel uniformity per unit, and the AOC listing does not say which mini-LED revision ships in the EU.`,
    model: "",
    genTimeMs: 41800,
    searches: [
      {
        query: "27 zoll 1440p monitor höhenverstellbar test 2026",
        kind: "web",
        at: 0,
        results: "12 results",
        sources: [
          { title: "Monitor-Testberichte 2026 — Prad.de", url: "https://www.prad.de" },
          { title: "Die besten 27-Zoll-Monitore — heise online", url: "https://www.heise.de" },
        ],
      },
      {
        query: "Dell S2721DGF Preis Deutschland",
        kind: "page",
        at: 0,
        results: "dell.de — product page",
        sources: [{ title: "Dell S2721DGF — Dell Deutschland", url: "https://www.dell.com" }],
      },
    ],
    citations: [
      { n: 1, title: "Dell S2721DGF — Dell Deutschland", url: "https://www.dell.com" },
      { n: 2, title: "LG 27QN880-B — MediaMarkt", url: "https://www.mediamarkt.de" },
      { n: 3, title: "AOC Q27G3XMN — Amazon.de", url: "https://www.amazon.de" },
    ],
  },
];

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
      title: "Reading a 40 GB CSV without OOMing",
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
    { id: "shot-old-1", title: "Draft release notes for 0.7", titled: true, model: id, updatedAt: now - 3 * 3600_000, messages: CHAT_TURNS.map(stamp) },
    { id: "shot-old-2", title: "Explain this stack trace", titled: true, model: id, updatedAt: now - 26 * 3600_000, messages: CHAT_TURNS.map(stamp) },
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
