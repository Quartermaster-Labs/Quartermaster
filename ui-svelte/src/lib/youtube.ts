import type { ToolDef } from "./types";

// Advertised to playground models so a YouTube link in the conversation is
// something they can actually read. The fetch happens server-side (yt-dlp,
// dispatched in turns.go) — nothing here touches the network; this file is only
// the contract shown to the model.
export const YOUTUBE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "youtube_transcript",
    description:
      "Fetch the transcript (captions) of a YouTube video so you can analyse, summarise, quote or fact-check it. Call this whenever the user gives a YouTube link or asks about a specific video — you cannot watch video, but this gives you what was said, in timestamped paragraphs. Long videos may come back truncated; the result says so explicitly when it does.",
    parameters: {
      type: "object",
      properties: {
        url: {
          type: "string",
          description:
            "The YouTube video URL (watch, youtu.be, shorts or embed form) or its 11-character video id",
        },
        lang: {
          type: "string",
          description:
            "Optional caption language code, e.g. 'en' (default), 'de', 'pt-BR'. Only use when the video is not in English.",
        },
      },
      required: ["url"],
    },
  },
};

// ---------------------------------------------------------------------------
// Link unfurling (the Discord-style card under a message that contains a link)
// ---------------------------------------------------------------------------

// Card fields, as returned by GET /api/youtube/meta (internal/server/youtube_meta.go).
export interface YouTubeMeta {
  id: string;
  title: string;
  uploader: string;
  thumb: string;
  url: string;
}

// Matches a YouTube video URL anywhere in a message. Kept deliberately narrow:
// it must not fire on youtube.com/@channel, /results?search_query=, or a bare
// 11-char word that merely looks like an id (the tool's parseYouTubeID accepts
// bare ids because a model passes them on purpose; a chat message does not).
// The trailing (?![\w-]) stops a longer id-like token being truncated to 11.
const YT_LINK_RE =
  /https?:\/\/(?:www\.|m\.|music\.)?(?:youtube\.com\/(?:watch\?(?:[^\s]*&)?v=|shorts\/|embed\/|live\/|v\/)|youtu\.be\/|youtube-nocookie\.com\/embed\/)([A-Za-z0-9_-]{11})(?![\w-])/g;

// Video ids linked in a message, in order, deduped. Cap keeps a message that
// pastes a whole playlist from rendering a wall of cards.
export const MAX_UNFURLS = 3;

export function extractYouTubeIds(text: string): string[] {
  if (!text) return [];
  const out: string[] = [];
  for (const m of text.matchAll(YT_LINK_RE)) {
    const id = m[1];
    if (!out.includes(id)) out.push(id);
    if (out.length >= MAX_UNFURLS) break;
  }
  return out;
}

// One in-flight/resolved promise per video id, shared by every card on screen:
// the same link quoted across several messages resolves once, and a re-render
// (or a scroll back) never refetches. Misses are cached as null so a private or
// deleted video isn't retried on every repaint.
const metaCache = new Map<string, Promise<YouTubeMeta | null>>();

export function fetchYouTubeMeta(id: string): Promise<YouTubeMeta | null> {
  let p = metaCache.get(id);
  if (!p) {
    p = fetch(`/api/youtube/meta?id=${encodeURIComponent(id)}`)
      .then((r) => (r.ok ? (r.json() as Promise<YouTubeMeta>) : null))
      .catch(() => null);
    metaCache.set(id, p);
  }
  return p;
}
