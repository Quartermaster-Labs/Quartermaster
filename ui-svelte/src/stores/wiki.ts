import { writable } from "svelte/store";

// A chat wiki-citation chip ("[n]" resolving to a help article) asks the
// playground to open WikiModal at that article. PlaygroundShell watches this and
// opens the modal, then resets it to null (so re-clicking the same chip after
// closing the modal re-triggers). ponytail: tiny cross-component signal store —
// ChatInterface mounts propless, so prop-drilling a callback down to ChatMessage
// would be uglier than this.
export const openWikiArticle = writable<string | null>(null);
