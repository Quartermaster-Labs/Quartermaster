<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Video & audio transcripts (YouTube and beyond)

Paste a link to a video, talk, stream or podcast episode in Chat and the model can read its captions and summarise, quote or fact-check it. It calls a `media_transcript` tool; Quartermaster fetches the captions server-side (yt-dlp) and hands them back as `[m:ss]` paragraphs, so the model can point at a moment ("at 14:32 they claim ...") and, on YouTube, link it back with `&t=872s`.

**Not just YouTube.** yt-dlp extracts from around 1800 sites, so Vimeo, TED, Dailymotion, Twitch VODs, Rumble, PeerTube, SoundCloud, conference players and most podcast episode pages work the same way - anywhere subtitles are published. Pages with no captions at all come back saying so, and the model should tell you that rather than guess.

**Finding videos.** The model can also *search* YouTube (`youtube_search`) and list a channel's or playlist's videos, so "find me a recent video about X and tell me what it says" works without you pasting a link first.

**Comments.** With `youtube_comments` the model can read a video's top comments - useful for "what do people say about this?", spotting corrections the creator never made, or gauging whether a tutorial actually worked for anyone. Comments are presented to the model as *audience opinions*, explicitly not as the video's own claims, so it shouldn't quote a comment as fact.

**Requirements**
- The model must support **tool calling**.
- **yt-dlp** must be present. Easiest: install it from **Settings -> Backends** (the *Tools* tab), which keeps it updatable. A copy on `PATH`, or the one the Windows installer drops in `bin/yt-dlp` when you tick the *yt-dlp* helper, also works. Without it the tools answer with an install hint instead of a transcript.

**Limits**
- Transcripts only exist for videos that actually have captions (manual or auto-generated). If captions are off, the tool says so rather than inventing content - there is no audio-transcription fallback.
- A long video is truncated to fit the context window; the result is marked `INCOMPLETE` with the timestamp it stopped at, and the model is told to say so.
- Comment fetching returns the top comments, not the whole thread.
- Per-turn call counts and result caching are capped server-side so a looping model can't grind through videos; repeated fetches of the same video are served from cache.
- Non-English captions: ask for them by language (e.g. "get the German transcript").
