import type { ToolDef } from "./types";

// Advertised to playground models so a link to a talk, stream or episode in the
// conversation is something they can actually read. The fetch happens
// server-side (yt-dlp, dispatched in turns.go) — nothing here touches the
// network; this file is only the contract shown to the model.
//
// yt-dlp is not a YouTube client: it extracts from ~1800 sites, and captions are
// captions wherever they come from. The tool is named for what it does rather
// than for one site, so the model actually tries it on a Vimeo or TED link
// instead of announcing it cannot watch video. The two tools below stay
// YouTube-specific because their mechanisms are: `ytsearch:` is a YouTube-only
// scheme, and comment extraction exists for barely any other site.
export const YOUTUBE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "media_transcript",
    description:
      "Fetch the transcript (captions/subtitles) of a video, talk, stream or podcast episode so you can analyse, summarise, quote or fact-check it. Works on YouTube and on hundreds of other sites - Vimeo, TED, Dailymotion, Twitch VODs, Rumble, PeerTube, SoundCloud, conference and news-site players, most podcast episode pages. Call this whenever the user gives a link to a recording or asks about a specific one - you cannot watch or listen, but this gives you what was said, in timestamped paragraphs. Not every page has captions and long recordings may come back truncated; the result says so explicitly, and you say so too rather than guessing.",
    parameters: {
      type: "object",
      properties: {
        url: {
          type: "string",
          description:
            "The page URL of the video or audio (any site, not just YouTube), or a bare 11-character YouTube video id",
        },
        lang: {
          type: "string",
          description:
            "Optional caption language code, e.g. 'en' (default), 'de', 'pt-BR'. Only use when the recording is not in English.",
        },
      },
      required: ["url"],
    },
  },
};

// Discovery. Without these the model can only read a video the user already
// pasted — it cannot find one, cannot see what a channel posted, and cannot tell
// whether a video is worth watching. Search and channel listing are one tool on
// purpose: they return the same thing (a video list) and a weak local model
// picking between two near-identical tools mostly picks wrong.
export const YOUTUBE_SEARCH_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "youtube_search",
    description:
      "Find YouTube videos: pass `query` to search all of YouTube, or `channel` to list what one channel has posted (newest first). Returns titles, links, channel, duration, upload date and view count - metadata only, never what was said in a video. Use it to find a video when the user has not given a link, to check what a channel recently covered, or to pick a video worth reading; then call media_transcript on the one you chose.",
    parameters: {
      type: "object",
      properties: {
        query: {
          type: "string",
          description:
            "Free-text search, as typed into YouTube's own search box. Include the year when the question is time-sensitive.",
        },
        channel: {
          type: "string",
          description:
            "A channel to list instead of searching: a @handle, a channel URL, or a playlist URL. Takes priority over `query`.",
        },
        tab: {
          type: "string",
          description:
            "Which part of the channel to list: 'videos' (default), 'shorts', or 'streams' (past live streams). Ignored for a playlist.",
        },
        limit: {
          type: "integer",
          description: "How many videos to return, 1-10 (default 8).",
        },
      },
      required: [],
    },
  },
};

export const YOUTUBE_COMMENTS_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "youtube_comments",
    description:
      "Read the top 10 most-liked comments on a YouTube video (replies excluded). Use it when the user asks what people thought of a video, whether its claims are disputed, or for corrections and context the video itself does not carry. Comments are individual opinions, not facts and not a representative sample - attribute them to commenters, never present them as the consensus or as verified. This is the slowest tool available, so call it only when the reaction is what is actually being asked about.",
    parameters: {
      type: "object",
      properties: {
        url: {
          type: "string",
          description:
            "The YouTube video URL (watch, youtu.be, shorts or embed form) or its 11-character video id",
        },
        limit: {
          type: "integer",
          description: "How many comments to read, 1-10 (default 10).",
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
