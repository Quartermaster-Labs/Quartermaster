import { writable, derived, type Readable } from "svelte/store";

// Per-user playground settings (system prompt, temperature, web-search /
// reasoning toggles, etc.), server-backed and keyed by the logged-in user so
// they follow the person rather than the browser. Mirrors chatHistory: one
// opaque blob the client owns, hydrated before the UI mounts and pushed back
// debounced. Individual settings are exposed as userPref() stores below.
type PrefMap = Record<string, unknown>;

const prefs = writable<PrefMap>({});

// synced gates the auto-save subscriber so it never overwrites the server
// during/before the initial load (or while logged out).
let synced = false;

export async function loadPrefs(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/prefs");
    const o = r.ok ? await r.json() : {};
    prefs.set(o && typeof o === "object" && !Array.isArray(o) ? o : {});
  } catch {
    prefs.set({});
  }
  synced = true;
}

export function clearPrefs(): void {
  synced = false;
  prefs.set({});
}

let timer: ReturnType<typeof setTimeout> | null = null;
prefs.subscribe((o) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/prefs", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(o),
    }).catch(() => {});
  }, 800);
});

// userPref returns a writable bound to one key in the shared prefs blob. Reads
// fall back to `def` until hydrated; writes merge back and trigger the debounced
// save. Shaped like a Svelte store (subscribe/set/update) so $store and
// bind:value work unchanged.
export function userPref<T>(key: string, def: T): Readable<T> & {
  set: (v: T) => void;
  update: (fn: (v: T) => T) => void;
} {
  const view = derived(prefs, ($p) => (key in $p ? ($p[key] as T) : def));
  return {
    subscribe: view.subscribe,
    set: (v: T) => prefs.update((p) => ({ ...p, [key]: v })),
    update: (fn: (v: T) => T) =>
      prefs.update((p) => ({ ...p, [key]: fn(key in p ? (p[key] as T) : def) })),
  };
}
