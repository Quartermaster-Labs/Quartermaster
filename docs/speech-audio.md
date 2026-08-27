<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Speech and transcription

Two playground tabs cover audio.

**Speech (TTS)** is a small studio: type text on the left, pick a voice, and each generation becomes a waveform card in the clip library on the right. Cards can be played, edited (which regenerates), regenerated, downloaded (WAV), or deleted; a volume slider and Auto-play toggle sit pinned at the bottom, and clips are saved per user as threads.

How voices work depends on the model kind (auto-detected):
- **Base model** - one default voice, plus **voice cloning**: click *Clone a voice* and either read the on-screen passage aloud (live mic) or upload a WAV/MP3 sample; an optional reference transcript improves the clone. Cloned voices are saved and reusable, and can be deleted.
- **Custom-voice model** - ships its own set of named speakers; pick one from the list (no default).
- **Voice-design model** - no fixed speakers. Instead you pick a **style preset** (a written description like "a calm elderly man with a slight rasp"). Several presets ship built-in, and you can *Design a voice* to save your own.

Use the refresh button by the voice list to reload the voices a model actually offers.

**Transcription (STT)** - upload an audio file and get the transcript from a speech-recognition model.

Both use the same on-demand loading as every other model - the first request loads the model.

**Pronunciation**: TTS quality depends on how the model turns text into sounds. Kokoro builds ship in two flavours - one that phonemizes with **espeak-ng** and one with a smaller rule set baked into the model file (usually marked `no_espeak`). The baked-in one mangles a whole class of everyday words ("messages" comes out as "messi"), so prefer the espeak build if you have the choice. It also matters for **non-English voices**: Kokoro ships packs for British English, Spanish, French, Hindi, Italian, Japanese, Portuguese and Mandarin, and only the espeak build can phonemize them - the baked-in rule set is English-only, so those voices read foreign text with an English accent.

Kokoro takes **no emotion or pause markup**. SSML tags, `<break>`, or bracketed stage directions are not commands to it - they get read out loud as words. Punctuation is the only control you have: commas and semicolons produce a real pause, and sentence-ending marks split the text into separate spoken chunks. For expressive speech you need a model built for it (a voice-design or style-prompt model) rather than markup.
