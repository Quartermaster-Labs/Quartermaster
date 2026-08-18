// Auto-attaches an API key to the local Playground's inference requests so the
// in-browser playground keeps working when keys gate the inference API.
//
// Key sources, in order:
//  1. GET /api/apikeys — the open key list. Works for the dashboard (local/
//     admin by design) and for the server host's own browser; a REMOTE
//     playground browser gets 403 (admin surface is this-host only).
//  2. GET /api/inference-key — the server hands logged-in playground users a
//     working key (the same one its turn runner uses). This is what keeps the
//     Mac-on-the-LAN case working: without it the browser's direct /v1 calls
//     (chat titles, auto-compaction, images, speech) would 401.
//
// Both prefer a full-access (unscoped) key so the playground can reach every
// model. No keys (or auth off) => no header, requests go through
// unauthenticated as before.

let key: string | null = null;

export async function refreshInferenceKey(): Promise<void> {
  // 1. The open list (local/admin).
  try {
    const res = await fetch("/api/apikeys");
    if (res.ok) {
      const keys: { name: string; key: string; models: string[] }[] = (await res.json()) || [];
      const unscoped = keys.find((k) => !k.models || k.models.length === 0);
      key = (unscoped ?? keys[0])?.key ?? null;
      return;
    }
  } catch {
    // fall through to the playground key
  }
  // 2. Logged-in playground user (remote LAN client).
  try {
    const res = await fetch("/api/inference-key");
    if (res.ok) {
      const j = (await res.json()) as { key?: string };
      key = j.key || null;
    } else {
      key = null;
    }
  } catch {
    key = null;
  }
}

// Merge the bearer auth header into a headers object when a key is cached.
export function inferenceHeaders(base: Record<string, string> = {}): Record<string, string> {
  return key ? { ...base, Authorization: `Bearer ${key}` } : base;
}
