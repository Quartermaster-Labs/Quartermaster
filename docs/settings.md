<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Playground settings

Playground **Settings** (gear in the side rail) has four sections, all saved to your account:

**General**

- **Theme**: System, Light or Dark.
- **Max Tokens**: cap on how long a single response can be.
- **Thinking Budget**: max reasoning tokens before a thinking model is forced to answer - 0 = unlimited. It's a soft cut at a round boundary, so the KV cache stays warm. Models that expose their own **reasoning-effort levels** ignore it: the field is disabled for them and you pick the level in the chat composer instead.
- **Read-aloud voice**: which TTS model and voice the speaker button on a message uses, with a preview button. Hidden when no TTS model is installed; refreshing the voice list loads that model.

**Memory**: a switch plus the list of lasting facts the assistant keeps about you. It writes here when you tell it to remember something, and every entry is editable or deletable. These facts are prepended to every chat's system prompt, so a short list keeps chats fast and a wrong entry is worth deleting.

**Web Search**: the provider chain and its limits - see the Web search article.

**System Prompt**: choose **Built-in default** (Quartermaster's shipped persona), **None** (no system prompt at all), or one of your own presets. A preset bundles the persona plus its own web-search, wiki, YouTube and citation instructions, each editable section by section; the built-in default and its shipped tool prompts are never modified.

**Temperature** is not here - it's per-chat, in the composer's config popover, because it's a per-conversation choice.

These are the **playground** per-user settings (the assistant's Quartermaster tools can adjust them for you). The **dashboard** has a separate Settings modal for server-wide config - memory budget (Target VRAM / Headroom / Max RAM), idle unload, KV-cache disk save, and Backends - each covered in its own article.
